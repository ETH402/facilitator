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
}

func NewClient(url, fallback string, timeout time.Duration, retries int) *Client {
	return &Client{url: url, fallback: fallback, client: &http.Client{Timeout: timeout}, retries: retries}
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
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// read rotates across the configured providers and requires a non-empty string
// result. It is for chain reads, where retrying another provider is safe.
func (c *Client) read(ctx context.Context, method string) (string, error) {
	result, err := c.readRaw(ctx, method, []any{})
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(result, &value); err != nil || value == "" {
		return "", errors.New("RPC result is empty")
	}
	return value, nil
}

// readRaw rotates across the configured providers and returns the raw result,
// which may be JSON null for not-yet-existing objects such as receipts.
func (c *Client) readRaw(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	urls := []string{c.url}
	if c.fallback != "" {
		urls = append(urls, c.fallback)
	}
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		target := urls[attempt%len(urls)]
		result, err := c.call(ctx, target, method, params)
		if err == nil {
			return result, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("RPC read %s failed: %w", method, last)
}

func (c *Client) call(ctx context.Context, target, method string, params []any) (json.RawMessage, error) {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: c.id.Add(1)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if err := resp.Body.Close(); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RPC HTTP status %d", resp.StatusCode)
	}
	var decoded rpcResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, &RPCError{Code: decoded.Error.Code, Message: decoded.Error.Message}
	}
	if len(decoded.Result) == 0 {
		return nil, errors.New("RPC result is missing")
	}
	return decoded.Result, nil
}

func parseHexUint(value string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 64)
}

func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	value, err := c.read(ctx, "eth_chainId")
	if err != nil {
		return 0, err
	}
	return parseHexUint(value)
}

func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	value, err := c.read(ctx, "eth_blockNumber")
	if err != nil {
		return 0, err
	}
	return parseHexUint(value)
}

// TransactionCount is the account's mined transaction count. Its only use is
// seeding and reconciling the durable nonce allocator at startup — never live
// allocation, where two concurrent reads would hand out the same nonce
// (ADR-0004 decision 1).
func (c *Client) TransactionCount(ctx context.Context, address string) (uint64, error) {
	result, err := c.readRaw(ctx, "eth_getTransactionCount", []any{address, "latest"})
	if err != nil {
		return 0, err
	}
	var value string
	if err := json.Unmarshal(result, &value); err != nil || value == "" {
		return 0, errors.New("transaction count result is empty")
	}
	return parseHexUint(value)
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
	if err := json.Unmarshal(result, &txHash); err != nil || !strings.HasPrefix(txHash, "0x") || len(txHash) != 66 {
		return "", fmt.Errorf("eth_sendRawTransaction returned invalid hash %q", txHash)
	}
	return strings.ToLower(txHash), nil
}

type rpcReceipt struct {
	Status            string `json:"status"`
	BlockNumber       string `json:"blockNumber"`
	BlockHash         string `json:"blockHash"`
	GasUsed           string `json:"gasUsed"`
	EffectiveGasPrice string `json:"effectiveGasPrice"`
}

// TransactionReceipt returns the receipt for a broadcast transaction, or
// (nil, nil) while the transaction is not yet mined. Reads may rotate across
// providers; a pending transaction is not an error.
func (c *Client) TransactionReceipt(ctx context.Context, txHash string) (*Receipt, error) {
	result, err := c.readRaw(ctx, "eth_getTransactionReceipt", []any{txHash})
	if err != nil {
		return nil, err
	}
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
	return &Receipt{
		Status: status, BlockNumber: blockNumber, BlockHash: strings.ToLower(raw.BlockHash),
		GasUsed: gasUsed, EffectiveGasPrice: gasPrice.String(),
	}, nil
}

type rpcTransaction struct {
	Hash        string  `json:"hash"`
	BlockNumber *string `json:"blockNumber"`
}

// TransactionByHash looks a transaction up by hash, returning (nil, nil)
// when the provider does not know it. A known transaction with a null block
// number is pending in the mempool.
func (c *Client) TransactionByHash(ctx context.Context, txHash string) (*ChainTransaction, error) {
	result, err := c.readRaw(ctx, "eth_getTransactionByHash", []any{txHash})
	if err != nil {
		return nil, err
	}
	if string(result) == "null" {
		return nil, nil
	}
	var raw rpcTransaction
	if err := json.Unmarshal(result, &raw); err != nil {
		return nil, fmt.Errorf("decode transaction: %w", err)
	}
	tx := &ChainTransaction{Hash: strings.ToLower(raw.Hash)}
	if raw.BlockNumber != nil {
		number, err := parseHexUint(*raw.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("transaction block number %q: %w", *raw.BlockNumber, err)
		}
		tx.BlockNumber = &number
	}
	return tx, nil
}

type rpcBlock struct {
	Hash    string `json:"hash"`
	Number  string `json:"number"`
	BaseFee string `json:"baseFeePerGas"`
}

// BlockByNumber fetches a canonical block by number; "latest" is used for fee
// estimation. A nil number means the latest block.
func (c *Client) BlockByNumber(ctx context.Context, number *uint64) (*Block, error) {
	tag := "latest"
	if number != nil {
		tag = "0x" + strconv.FormatUint(*number, 16)
	}
	result, err := c.readRaw(ctx, "eth_getBlockByNumber", []any{tag, false})
	if err != nil {
		return nil, err
	}
	if string(result) == "null" {
		return nil, fmt.Errorf("block %s does not exist", tag)
	}
	var raw rpcBlock
	if err := json.Unmarshal(result, &raw); err != nil {
		return nil, fmt.Errorf("decode block: %w", err)
	}
	blockNumber, err := parseHexUint(raw.Number)
	if err != nil {
		return nil, fmt.Errorf("block number %q: %w", raw.Number, err)
	}
	baseFee := "0"
	if raw.BaseFee != "" {
		parsed, ok := new(big.Int).SetString(strings.TrimPrefix(raw.BaseFee, "0x"), 16)
		if !ok || parsed.Sign() < 0 {
			return nil, fmt.Errorf("base fee %q is not unsigned hex", raw.BaseFee)
		}
		baseFee = parsed.String()
	}
	return &Block{Hash: strings.ToLower(raw.Hash), Number: blockNumber, BaseFee: baseFee}, nil
}

// Balance returns an address's ether balance in wei at the latest block.
//
// A big.Int rather than uint64: a funded account can exceed 2^64 wei, and
// silently truncating the number that bounds the operator's loss exposure would
// be the worst possible place to do it.
func (c *Client) Balance(ctx context.Context, address string) (*big.Int, error) {
	result, err := c.readRaw(ctx, "eth_getBalance", []any{address, "latest"})
	if err != nil {
		return nil, err
	}
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
	if _, err := c.readRaw(ctx, "eth_call", params); err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Reverted() {
			return fmt.Errorf("%w: %s", ErrSimulationReverted, rpcErr.Message)
		}
		return err
	}
	return nil
}
