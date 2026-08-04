package ethereum

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Reader interface {
	ChainID(context.Context) (uint64, error)
	BlockNumber(context.Context) (uint64, error)
}

// Broadcaster is the settlement write path. It is deliberately separate from
// Reader: a failed broadcast is ambiguous (ADR-0004 decision 4), so it must
// never rotate to a fallback provider and disguise the outcome.
type Broadcaster interface {
	SendRawTransaction(context.Context, string) (string, error)
}

// ReceiptReader observes broadcast transactions.
type ReceiptReader interface {
	TransactionReceipt(context.Context, string) (*Receipt, error)
}

// ChainTransaction is the subset of eth_getTransactionByHash recovery uses:
// whether the transaction is known at all, and whether it is still pending.
type ChainTransaction struct {
	Hash        string
	BlockNumber *uint64 // nil while the transaction sits in the mempool
}

// Block carries the canonical identity of a block and its base fee, used for
// reorg checks and fee estimation.
type Block struct {
	Hash   string
	Number uint64
	// BaseFee is a decimal wei string, keeping money in integer arithmetic.
	// Empty for pre-London blocks, which mainnet no longer produces.
	BaseFee string
}

// Receipt is the subset of eth_getTransactionReceipt settlement decisions use.
// Gas price is a decimal wei string, keeping money in integer arithmetic.
type Receipt struct {
	Status            uint64
	BlockNumber       uint64
	BlockHash         string
	GasUsed           uint64
	EffectiveGasPrice string
}

type Client struct {
	url      string
	fallback string
	client   *http.Client
	retries  int
	id       atomic.Uint64
	observer Observer
}

func NewClient(url, fallback string, timeout time.Duration, retries int) *Client {
	return &Client{url: url, fallback: fallback, client: &http.Client{Timeout: timeout}, retries: retries}
}

// ValidateProviders checks each configured provider independently before the
// ordinary read path begins requiring concurrent agreement. Reporting the
// provider name here makes startup failures actionable before workers start.
func ValidateProviders(ctx context.Context, primary, fallback string, timeout time.Duration, expectedChainID uint64) error {
	providers := []struct {
		name string
		url  string
	}{{name: "primary", url: primary}}
	if fallback != "" {
		providers = append(providers, struct {
			name string
			url  string
		}{name: "fallback", url: fallback})
	}
	for _, provider := range providers {
		chainID, err := NewClient(provider.url, "", timeout, 0).ChainID(ctx)
		if err != nil {
			return fmt.Errorf("%s Ethereum RPC validation failed: %w", provider.name, err)
		}
		if chainID != expectedChainID {
			return fmt.Errorf("%s Ethereum RPC reports chain ID %d, want %d",
				provider.name, chainID, expectedChainID)
		}
	}
	return nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      uint64 `json:"id"`
}

// RPCError is a JSON-RPC error object. It is typed rather than flattened into a
// string because the distinction between "the node executed this and it
// reverted" and "the request never got an answer" decides whether a payment is
// unsettleable or merely needs retrying.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message) }

// Reverted reports whether the node executed the call and the EVM reverted.
//
// Deliberately conservative: only an explicit revert counts. Misreading a
// transport hiccup as a revert would abandon a payment that could have settled,
// whereas misreading a revert as transient only costs a retry that never
// broadcasts. Geth and Anvil use code 3; the message is checked too because
// providers vary.
func (e *RPCError) Reverted() bool {
	return e.Code == 3 || strings.Contains(strings.ToLower(e.Message), "execution reverted")
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type providerResult struct {
	index int
	raw   json.RawMessage
	err   error
}

func (c *Client) providerURLs() []string {
	urls := []string{c.url}
	if c.fallback != "" && c.fallback != c.url {
		urls = append(urls, c.fallback)
	}
	return urls
}

// callProvider applies the configured retry budget to one provider. Agreement
// never substitutes one provider for another: every configured provider must
// eventually answer, and metrics retain one observation per actual attempt.
func (c *Client) callProvider(ctx context.Context, target, method string, params []any) (json.RawMessage, error) {
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		result, err := c.call(ctx, target, method, params)
		if c.observer != nil {
			c.observer.ObserveRPC(err != nil)
		}
		if err == nil {
			return result, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// readProviders calls independently configured providers concurrently. With a
// single URL this preserves local-development behavior, including retries. In
// production, two responses are mandatory; latency is bounded by the slower
// provider rather than the sum of both provider latencies.
func (c *Client) readProviders(ctx context.Context, method string, params []any) ([]providerResult, error) {
	urls := c.providerURLs()
	results := make(chan providerResult, len(urls))
	for index, target := range urls {
		go func() {
			raw, err := c.callProvider(ctx, target, method, params)
			results <- providerResult{index: index, raw: raw, err: err}
		}()
	}
	outcomes := make([]providerResult, len(urls))
	for range urls {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case outcome := <-results:
			outcomes[outcome.index] = outcome
		}
	}
	return outcomes, nil
}

func sameRPCError(left, right error) bool {
	var leftRPC, rightRPC *RPCError
	return errors.As(left, &leftRPC) && errors.As(right, &rightRPC) &&
		leftRPC.Code == rightRPC.Code && leftRPC.Message == rightRPC.Message
}

// readClientAgreement decodes and reconciles the answer from every configured
// provider. A transport/protocol failure is reported as provider failure. An
// executed JSON-RPC error (for example eth_call revert) is itself chain-state
// evidence: equal errors are returned to the caller, while error-vs-value or
// different errors are provider disagreement.
func readClientAgreement[T any](
	ctx context.Context,
	c *Client,
	method string,
	params []any,
	decode func(json.RawMessage) (T, error),
	reconcile func([]T) (T, bool),
) (T, error) {
	var zero T
	outcomes, err := c.readProviders(ctx, method, params)
	if err != nil {
		return zero, fmt.Errorf("RPC read %s failed: %w", method, err)
	}
	if len(outcomes) == 1 && outcomes[0].err != nil {
		return zero, fmt.Errorf("RPC read %s failed: %w", method, outcomes[0].err)
	}
	failed := -1
	succeeded := 0
	for index, outcome := range outcomes {
		if outcome.err != nil {
			if failed == -1 {
				failed = index
			}
			continue
		}
		succeeded++
	}
	if failed >= 0 {
		if succeeded > 0 {
			var rpcErr *RPCError
			if errors.As(outcomes[failed].err, &rpcErr) {
				return zero, c.providerDisagreement(method,
					fmt.Sprintf("provider %d returned an RPC error while another returned a value", failed+1))
			}
			return zero, fmt.Errorf("RPC read %s provider %d failed: %w", method, failed+1, outcomes[failed].err)
		}
		allRPC := true
		firstNonRPC := -1
		for index, outcome := range outcomes {
			var rpcErr *RPCError
			if !errors.As(outcome.err, &rpcErr) {
				allRPC = false
				if firstNonRPC == -1 {
					firstNonRPC = index
				}
			}
		}
		if !allRPC {
			// Never wrap an RPCError when another provider was unavailable. In
			// particular, Call must not classify one provider's revert as proven
			// chain state unless every provider independently returned that revert.
			return zero, fmt.Errorf("RPC read %s provider %d failed: %w",
				method, firstNonRPC+1, outcomes[firstNonRPC].err)
		}
		for index := 1; index < len(outcomes); index++ {
			if !sameRPCError(outcomes[0].err, outcomes[index].err) {
				return zero, c.providerDisagreement(method, "providers returned different RPC errors")
			}
		}
		return zero, fmt.Errorf("RPC read %s failed: %w", method, outcomes[0].err)
	}
	values := make([]T, len(outcomes))
	for index, outcome := range outcomes {
		value, err := decode(outcome.raw)
		if err != nil {
			return zero, fmt.Errorf("RPC read %s provider %d returned invalid data: %w", method, index+1, err)
		}
		values[index] = value
	}
	value, ok := reconcile(values)
	if !ok {
		return zero, c.providerDisagreement(method, "providers returned different values")
	}
	return value, nil
}

func exactComparable[T comparable](values []T) (T, bool) {
	first := values[0]
	for _, value := range values[1:] {
		if value != first {
			var zero T
			return zero, false
		}
	}
	return first, true
}

func decodeHexUintResult(raw json.RawMessage) (uint64, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return 0, errors.New("RPC result is empty")
	}
	return parseHexUint(value)
}

func (c *Client) call(ctx context.Context, target, method string, params []any) (json.RawMessage, error) {
	requestID := c.id.Add(1)
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: requestID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("RPC request endpoint is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, sanitizeRPCTransportError(ctx, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		_ = resp.Body.Close()
		return nil, errors.New("RPC response read failed")
	}
	if err := resp.Body.Close(); err != nil {
		return nil, errors.New("RPC response close failed")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RPC HTTP status %d", resp.StatusCode)
	}
	var decoded rpcResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	// The response must answer this request: a missing or mismatched id means
	// the endpoint is misbehaving (or a proxy crossed streams), and trusting
	// the payload would attribute another request's result to this one.
	var responseID uint64
	if err := json.Unmarshal(decoded.ID, &responseID); err != nil || responseID != requestID {
		return nil, fmt.Errorf("RPC response id %s does not match request id %d", decoded.ID, requestID)
	}
	if decoded.Error != nil {
		return nil, &RPCError{Code: decoded.Error.Code, Message: decoded.Error.Message}
	}
	if len(decoded.Result) == 0 {
		return nil, errors.New("RPC result is missing")
	}
	return decoded.Result, nil
}

// sanitizeRPCTransportError prevents authenticated endpoint URLs from reaching
// logs through net/http's *url.Error, whose Error string includes the full URL
// (including path and query credentials). JSON-RPC errors are handled after a
// response arrives and retain their typed code/message semantics.
func sanitizeRPCTransportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("RPC request timed out")
	}
	return errors.New("RPC transport failed")
}

func parseHexUint(value string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 64)
}

// normalizeHash validates the fixed-width DATA value Ethereum JSON-RPC uses
// for transaction and block hashes. Shape-only checks are not enough: a
// provider response is payment evidence, so malformed bytes must not enter the
// settlement state machine.
func normalizeHash(field, value string) (string, error) {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return "", fmt.Errorf("%s %q is not a 32-byte hex value", field, value)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", fmt.Errorf("%s %q is not a 32-byte hex value: %w", field, value, err)
	}
	return strings.ToLower(value), nil
}

func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	return readClientAgreement(ctx, c, "eth_chainId", []any{}, decodeHexUintResult, exactComparable[uint64])
}

// maxLatestBlockSkew is the only deliberately tolerated provider difference.
// Independent nodes can observe adjacent heads at the same instant. Two blocks
// allows normal propagation delay without letting one provider manufacture
// confirmation depth. Callers receive the lower head, so finality is never
// advanced by the faster provider.
const maxLatestBlockSkew uint64 = 2

func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	return readClientAgreement(ctx, c, "eth_blockNumber", []any{}, decodeHexUintResult,
		func(values []uint64) (uint64, bool) {
			lowest, highest := values[0], values[0]
			for _, value := range values[1:] {
				lowest = min(lowest, value)
				highest = max(highest, value)
			}
			return lowest, highest-lowest <= maxLatestBlockSkew
		})
}

// TransactionCount is the account's mined transaction count. Its only use is
// seeding and reconciling the durable nonce allocator at startup — never live
// allocation, where two concurrent reads would hand out the same nonce
// (ADR-0004 decision 1).
func (c *Client) TransactionCount(ctx context.Context, address string) (uint64, error) {
	return readClientAgreement(ctx, c, "eth_getTransactionCount", []any{address, "latest"},
		decodeHexUintResult, exactComparable[uint64])
}

// SendRawTransaction broadcasts a signed transaction and returns its hash.
//
// It makes a single attempt against the primary provider. A timeout or
// connection failure leaves the outcome unknown — the transaction may be
// mined — and ADR-0004 decision 4 requires that case to surface as ambiguous
// rather than be retried, so there is no rotation and no retry here.
func (c *Client) SendRawTransaction(ctx context.Context, rawHex string) (string, error) {
	result, err := c.call(ctx, c.url, "eth_sendRawTransaction", []any{rawHex})
	if err != nil {
		return "", fmt.Errorf("eth_sendRawTransaction failed: %w", err)
	}
	var txHash string
	if err := json.Unmarshal(result, &txHash); err != nil {
		return "", fmt.Errorf("eth_sendRawTransaction returned invalid hash %q", txHash)
	}
	normalized, err := normalizeHash("eth_sendRawTransaction hash", txHash)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

type rpcReceipt struct {
	TransactionHash   string `json:"transactionHash"`
	Status            string `json:"status"`
	BlockNumber       string `json:"blockNumber"`
	BlockHash         string `json:"blockHash"`
	GasUsed           string `json:"gasUsed"`
	EffectiveGasPrice string `json:"effectiveGasPrice"`
}

// TransactionReceipt returns the receipt for a broadcast transaction, or
// (nil, nil) while the transaction is not yet mined. Every configured provider
// must agree that it is pending or return the same decoded receipt.
func (c *Client) TransactionReceipt(ctx context.Context, txHash string) (*Receipt, error) {
	requestedHash, err := normalizeHash("requested transaction hash", txHash)
	if err != nil {
		return nil, err
	}
	return readClientAgreement(ctx, c, "eth_getTransactionReceipt", []any{requestedHash},
		func(result json.RawMessage) (*Receipt, error) { return decodeReceipt(result, requestedHash) },
		reconcileReceipts)
}

func decodeReceipt(result json.RawMessage, requestedHash string) (*Receipt, error) {
	if string(result) == "null" {
		return nil, nil
	}
	var raw rpcReceipt
	if err := json.Unmarshal(result, &raw); err != nil {
		return nil, fmt.Errorf("decode receipt: %w", err)
	}
	status, err := parseHexUint(raw.Status)
	if err != nil {
		return nil, fmt.Errorf("receipt status %q: %w", raw.Status, err)
	}
	if status > 1 {
		return nil, fmt.Errorf("receipt status %q is neither 0 nor 1", raw.Status)
	}
	returnedHash, err := normalizeHash("receipt transaction hash", raw.TransactionHash)
	if err != nil {
		return nil, err
	}
	if returnedHash != requestedHash {
		return nil, fmt.Errorf("receipt transaction hash %s does not match requested hash %s", returnedHash, requestedHash)
	}
	blockNumber, err := parseHexUint(raw.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("receipt block number %q: %w", raw.BlockNumber, err)
	}
	gasUsed, err := parseHexUint(raw.GasUsed)
	if err != nil {
		return nil, fmt.Errorf("receipt gas used %q: %w", raw.GasUsed, err)
	}
	gasPrice, ok := new(big.Int).SetString(strings.TrimPrefix(raw.EffectiveGasPrice, "0x"), 16)
	if !ok || gasPrice.Sign() < 0 {
		return nil, fmt.Errorf("receipt effective gas price %q is not unsigned hex", raw.EffectiveGasPrice)
	}
	blockHash, err := normalizeHash("receipt block hash", raw.BlockHash)
	if err != nil {
		return nil, err
	}
	return &Receipt{
		Status: status, BlockNumber: blockNumber, BlockHash: blockHash,
		GasUsed: gasUsed, EffectiveGasPrice: gasPrice.String(),
	}, nil
}

func reconcileReceipts(values []*Receipt) (*Receipt, bool) {
	first := values[0]
	for _, value := range values[1:] {
		if first == nil || value == nil {
			if first != value {
				return nil, false
			}
			continue
		}
		if *first != *value {
			return nil, false
		}
	}
	return first, true
}

type rpcTransaction struct {
	Hash        string  `json:"hash"`
	BlockNumber *string `json:"blockNumber"`
}

// TransactionByHash looks a transaction up by hash, returning (nil, nil)
// when the provider does not know it. A known transaction with a null block
// number is pending in the mempool.
func (c *Client) TransactionByHash(ctx context.Context, txHash string) (*ChainTransaction, error) {
	requestedHash, err := normalizeHash("requested transaction hash", txHash)
	if err != nil {
		return nil, err
	}
	return readClientAgreement(ctx, c, "eth_getTransactionByHash", []any{requestedHash},
		func(result json.RawMessage) (*ChainTransaction, error) {
			return decodeTransaction(result, requestedHash)
		},
		reconcileTransactions)
}

func decodeTransaction(result json.RawMessage, requestedHash string) (*ChainTransaction, error) {
	if string(result) == "null" {
		return nil, nil
	}
	var raw rpcTransaction
	if err := json.Unmarshal(result, &raw); err != nil {
		return nil, fmt.Errorf("decode transaction: %w", err)
	}
	returnedHash, err := normalizeHash("transaction hash", raw.Hash)
	if err != nil {
		return nil, err
	}
	if returnedHash != requestedHash {
		return nil, fmt.Errorf("transaction hash %s does not match requested hash %s", returnedHash, requestedHash)
	}
	tx := &ChainTransaction{Hash: returnedHash}
	if raw.BlockNumber != nil {
		number, err := parseHexUint(*raw.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("transaction block number %q: %w", *raw.BlockNumber, err)
		}
		tx.BlockNumber = &number
	}
	return tx, nil
}

func reconcileTransactions(values []*ChainTransaction) (*ChainTransaction, bool) {
	first := values[0]
	for _, value := range values[1:] {
		if first == nil || value == nil {
			if first != value {
				return nil, false
			}
			continue
		}
		if first.Hash != value.Hash || (first.BlockNumber == nil) != (value.BlockNumber == nil) {
			return nil, false
		}
		if first.BlockNumber != nil && *first.BlockNumber != *value.BlockNumber {
			return nil, false
		}
	}
	return first, true
}

type rpcBlock struct {
	Hash    string `json:"hash"`
	Number  string `json:"number"`
	BaseFee string `json:"baseFeePerGas"`
}

// BlockByNumber fetches a canonical block by number; "latest" is used for fee
// estimation. A nil number means the latest block.
func (c *Client) BlockByNumber(ctx context.Context, number *uint64) (*Block, error) {
	if number == nil && len(c.providerURLs()) > 1 {
		// Pin fee estimation to the lower agreed head. Comparing two independent
		// providers' moving "latest" blocks would create false disagreement during
		// normal propagation, while trusting either block's base fee would let one
		// provider control pricing. The second, fixed-height read must agree exactly.
		head, err := c.BlockNumber(ctx)
		if err != nil {
			return nil, err
		}
		return c.BlockByNumber(ctx, &head)
	}
	tag := "latest"
	if number != nil {
		tag = "0x" + strconv.FormatUint(*number, 16)
	}
	block, err := readClientAgreement(ctx, c, "eth_getBlockByNumber", []any{tag, false},
		func(result json.RawMessage) (Block, error) { return decodeBlock(result, number, tag) },
		exactComparable[Block])
	if err != nil {
		return nil, err
	}
	return &block, nil
}

func decodeBlock(result json.RawMessage, number *uint64, tag string) (Block, error) {
	if string(result) == "null" {
		return Block{}, fmt.Errorf("block %s does not exist", tag)
	}
	var raw rpcBlock
	if err := json.Unmarshal(result, &raw); err != nil {
		return Block{}, fmt.Errorf("decode block: %w", err)
	}
	blockNumber, err := parseHexUint(raw.Number)
	if err != nil {
		return Block{}, fmt.Errorf("block number %q: %w", raw.Number, err)
	}
	if number != nil && blockNumber != *number {
		return Block{}, fmt.Errorf("block number %d does not match requested number %d", blockNumber, *number)
	}
	blockHash, err := normalizeHash("block hash", raw.Hash)
	if err != nil {
		return Block{}, err
	}
	baseFee := "0"
	if raw.BaseFee != "" {
		parsed, ok := new(big.Int).SetString(strings.TrimPrefix(raw.BaseFee, "0x"), 16)
		if !ok || parsed.Sign() < 0 {
			return Block{}, fmt.Errorf("base fee %q is not unsigned hex", raw.BaseFee)
		}
		baseFee = parsed.String()
	}
	return Block{Hash: blockHash, Number: blockNumber, BaseFee: baseFee}, nil
}

// Observer receives one call per RPC attempt. *metrics.Registry satisfies it; the
// interface keeps this package free of a metrics dependency.
type Observer interface {
	ObserveRPC(failed bool)
}

// ProviderDisagreementObserver receives logical read disagreements separately
// from per-attempt transport failures. A pair of successful HTTP/RPC attempts
// can still be unsafe, so folding this into ObserveRPC would either hide the
// event or corrupt the attempt/error-rate counters.
type ProviderDisagreementObserver interface {
	ObserveRPCDisagreement()
}

func (c *Client) providerDisagreement(method, detail string) error {
	if observer, ok := c.observer.(ProviderDisagreementObserver); ok {
		observer.ObserveRPCDisagreement()
	}
	return fmt.Errorf("%w for %s: %s", ErrProviderDisagreement, method, detail)
}

// Observe attaches an observer. Set once at startup, before any request, so no
// synchronisation is needed on the field itself. Implementations must be safe
// for concurrent calls when two providers are configured.
func (c *Client) Observe(o Observer) { c.observer = o }

// Balance returns an address's ether balance in wei at the latest block.
//
// A big.Int rather than uint64: a funded account can exceed 2^64 wei, and
// silently truncating the number that bounds the operator's loss exposure would
// be the worst possible place to do it.
func (c *Client) Balance(ctx context.Context, address string) (*big.Int, error) {
	return readClientAgreement(ctx, c, "eth_getBalance", []any{address, "latest"}, decodeBalance,
		func(values []*big.Int) (*big.Int, bool) {
			first := values[0]
			for _, value := range values[1:] {
				if first.Cmp(value) != 0 {
					return nil, false
				}
			}
			return first, true
		})
}

func decodeBalance(result json.RawMessage) (*big.Int, error) {
	var value string
	if err := json.Unmarshal(result, &value); err != nil || value == "" {
		return nil, errors.New("balance result is empty")
	}
	wei, ok := new(big.Int).SetString(strings.TrimPrefix(value, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("balance %q is not hexadecimal", value)
	}
	return wei, nil
}

// ErrSimulationReverted means the EVM executed the call and reverted, so
// broadcasting the same transaction would burn gas to reach the same outcome.
var ErrSimulationReverted = errors.New("transaction simulation reverted")

// Call simulates a transaction with eth_call against the latest block.
//
// It simulates the exact calldata that would be broadcast rather than
// reconstructing it, so what is checked is literally what would be sent. A
// revert is reported as ErrSimulationReverted; anything else is transient and the
// caller should retry rather than abandon the payment.
func (c *Client) Call(ctx context.Context, from, to string, data []byte) error {
	params := []any{
		map[string]string{"from": from, "to": to, "data": "0x" + hex.EncodeToString(data)},
		"latest",
	}
	_, err := readClientAgreement(ctx, c, "eth_call", params, decodeHexData,
		func(values [][]byte) ([]byte, bool) {
			for _, value := range values[1:] {
				if !bytes.Equal(values[0], value) {
					return nil, false
				}
			}
			return values[0], true
		})
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Reverted() {
			return fmt.Errorf("%w: %s", ErrSimulationReverted, rpcErr.Message)
		}
		return err
	}
	return nil
}

func decodeHexData(result json.RawMessage) ([]byte, error) {
	var value string
	if err := json.Unmarshal(result, &value); err != nil || !strings.HasPrefix(value, "0x") {
		return nil, errors.New("RPC data result is not hexadecimal")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil {
		return nil, fmt.Errorf("RPC data result is not hexadecimal: %w", err)
	}
	return decoded, nil
}
