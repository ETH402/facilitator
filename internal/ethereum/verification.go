package ethereum

import (
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

// VerificationClient implements the official x402 EVM facilitator signer
// interface for read-only verification. Its transaction methods deliberately
// fail closed; settlement uses a separate signer in Milestone 3.
type VerificationClient struct {
	clients []*ethclient.Client
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
	result, err := readFallback(c.clients, func(client *ethclient.Client) ([]byte, error) {
		return client.CallContract(ctx, ethereum.CallMsg{To: &target, Data: data}, nil)
	})
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
	return readFallback(c.clients, func(client *ethclient.Client) (*big.Int, error) {
		return client.ChainID(ctx)
	})
}

func (c *VerificationClient) GetCode(ctx context.Context, address string) ([]byte, error) {
	target := common.HexToAddress(address)
	return readFallback(c.clients, func(client *ethclient.Client) ([]byte, error) {
		return client.CodeAt(ctx, target, nil)
	})
}

func readFallback[T any](clients []*ethclient.Client, read func(*ethclient.Client) (T, error)) (T, error) {
	var zero T
	var last error
	for _, client := range clients {
		value, err := read(client)
		if err == nil {
			return value, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no RPC clients configured")
	}
	return zero, last
}
