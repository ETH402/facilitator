package ethereum

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

var errTransactionsDisabled = errors.New("transaction submission is disabled in the verification client")

// ErrProviderDisagreement means independently configured RPCs returned
// different answers to the same payment-critical read. Availability fallback
// is unsafe for verification: one equivocating provider must not be able to
// decide whether an authorization is settleable merely by answering first.
var ErrProviderDisagreement = errors.New("ethereum RPC providers disagree")

// VerificationClient implements the official x402 EVM facilitator signer
// interface for read-only verification. Its transaction methods deliberately
// fail closed; settlement uses a separate signer in Milestone 3.
type VerificationClient struct {
	clients  []*ethclient.Client
	observer ProviderDisagreementObserver
}

func NewVerificationClient(primary, fallback string) (*VerificationClient, error) {
	urls := []string{primary}
	if fallback != "" && fallback != primary {
		urls = append(urls, fallback)
	}
	clients := make([]*ethclient.Client, 0, len(urls))
	for _, rpcURL := range urls {
		client, err := ethclient.Dial(rpcURL)
		if err != nil {
			for _, opened := range clients {
				opened.Close()
			}
			return nil, fmt.Errorf("dial verification RPC: %w", err)
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, errors.New("at least one verification RPC is required")
	}
	return &VerificationClient{clients: clients}, nil
}

func (c *VerificationClient) Close() {
	for _, client := range c.clients {
		client.Close()
	}
}

// ObserveProviderDisagreements attaches the shared production metric sink.
// Verification uses go-ethereum's client directly, so it cannot report the
// settlement client's per-attempt counters, but disagreement is a common
// safety signal across both read paths.
func (c *VerificationClient) ObserveProviderDisagreements(observer ProviderDisagreementObserver) {
	c.observer = observer
}

func (c *VerificationClient) GetAddresses() []string { return []string{} }

func (c *VerificationClient) ReadContract(
	ctx context.Context,
	address string,
	abiJSON []byte,
	functionName string,
	args ...interface{},
) (interface{}, error) {
	contractABI, err := abi.JSON(strings.NewReader(string(abiJSON)))
	if err != nil {
		return nil, fmt.Errorf("parse contract ABI: %w", err)
	}
	data, err := contractABI.Pack(functionName, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s call: %w", functionName, err)
	}
	target := common.HexToAddress(address)
	result, err := readAgreement(ctx, c.clients, func(client *ethclient.Client) ([]byte, error) {
		return client.CallContract(ctx, ethereum.CallMsg{To: &target, Data: data}, nil)
	}, bytes.Equal, c.observer)
	if err != nil {
		return nil, fmt.Errorf("eth_call %s: %w", functionName, err)
	}
	outputs, err := contractABI.Unpack(functionName, result)
	if err != nil {
		return nil, fmt.Errorf("unpack %s result: %w", functionName, err)
	}
	switch len(outputs) {
	case 0:
		return nil, nil
	case 1:
		return outputs[0], nil
	default:
		return outputs, nil
	}
}

func (c *VerificationClient) VerifyTypedData(
	_ context.Context,
	address string,
	domain x402evm.TypedDataDomain,
	types map[string][]x402evm.TypedDataField,
	primaryType string,
	message map[string]interface{},
	signature []byte,
) (bool, error) {
	return x402evm.VerifyEOATypedData(address, domain, types, primaryType, message, signature)
}

func (c *VerificationClient) WriteContract(
	context.Context, string, []byte, string, []byte, ...interface{},
) (string, error) {
	return "", errTransactionsDisabled
}

func (c *VerificationClient) SendTransaction(context.Context, string, []byte) (string, error) {
	return "", errTransactionsDisabled
}

func (c *VerificationClient) WaitForTransactionReceipt(context.Context, string) (*x402evm.TransactionReceipt, error) {
	return nil, errTransactionsDisabled
}

func (c *VerificationClient) GetBalance(ctx context.Context, address, tokenAddress string) (*big.Int, error) {
	const balanceABI = `[{"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`
	result, err := c.ReadContract(ctx, tokenAddress, []byte(balanceABI), "balanceOf", common.HexToAddress(address))
	if err != nil {
		return nil, err
	}
	balance, ok := result.(*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected balanceOf result %T", result)
	}
	return balance, nil
}

func (c *VerificationClient) GetChainID(ctx context.Context) (*big.Int, error) {
	return readAgreement(ctx, c.clients, func(client *ethclient.Client) (*big.Int, error) {
		return client.ChainID(ctx)
	}, func(left, right *big.Int) bool { return left.Cmp(right) == 0 }, c.observer)
}

func (c *VerificationClient) GetCode(ctx context.Context, address string) ([]byte, error) {
	target := common.HexToAddress(address)
	return readAgreement(ctx, c.clients, func(client *ethclient.Client) ([]byte, error) {
		return client.CodeAt(ctx, target, nil)
	}, bytes.Equal, c.observer)
}

// readAgreement requires every configured provider to answer and agree. A
// single-provider client remains useful for local development, while production
// config supplies two independently operated providers. Reads run concurrently
// so the safety check costs one provider latency rather than their sum.
func readAgreement[T any](
	ctx context.Context,
	clients []*ethclient.Client,
	read func(*ethclient.Client) (T, error),
	equal func(T, T) bool,
	observer ProviderDisagreementObserver,
) (T, error) {
	var zero T
	if len(clients) == 0 {
		return zero, errors.New("no RPC clients configured")
	}
	type result struct {
		index int
		value T
		err   error
	}
	results := make(chan result, len(clients))
	for index, client := range clients {
		go func() {
			value, err := read(client)
			results <- result{index: index, value: value, err: err}
		}()
	}
	outcomes := make([]result, len(clients))
	for range clients {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case outcome := <-results:
			outcomes[outcome.index] = outcome
		}
	}

	successes := 0
	for _, outcome := range outcomes {
		if outcome.err == nil {
			successes++
		}
	}
	if successes != 0 && successes != len(outcomes) {
		observeProviderDisagreement(observer)
		return zero, fmt.Errorf("%w: providers returned divergent success and error outcomes", ErrProviderDisagreement)
	}
	if successes == 0 {
		// Every provider failed, so there is no successful chain-state value to
		// disagree with. Do not wrap the provider error: go-ethereum transport
		// errors can include the authenticated endpoint URL, which callers log.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, errors.New("ethereum RPC providers unavailable")
	}

	values := make([]T, len(outcomes))
	for index, outcome := range outcomes {
		values[index] = outcome.value
	}
	for index := 1; index < len(values); index++ {
		if !equal(values[0], values[index]) {
			observeProviderDisagreement(observer)
			return zero, fmt.Errorf("%w: provider 1 and provider %d", ErrProviderDisagreement, index+1)
		}
	}
	return values[0], nil
}

func observeProviderDisagreement(observer ProviderDisagreementObserver) {
	if observer != nil {
		observer.ObserveRPCDisagreement()
	}
}
