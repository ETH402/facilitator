package ethereum

import (
	"bytes"
	"context"
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
		return nil, fmt.Errorf("RPC error %d: %s", decoded.Error.Code, decoded.Error.Message)
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
