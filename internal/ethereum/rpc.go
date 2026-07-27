package ethereum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) read(ctx context.Context, method string) (string, error) {
	urls := []string{c.url}
	if c.fallback != "" {
		urls = append(urls, c.fallback)
	}
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		target := urls[attempt%len(urls)]
		result, err := c.call(ctx, target, method)
		if err == nil {
			return result, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return "", fmt.Errorf("RPC read %s failed: %w", method, last)
}

func (c *Client) call(ctx context.Context, target, method string) (string, error) {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: []any{}, ID: c.id.Add(1)})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		_ = resp.Body.Close()
		return "", err
	}
	if err := resp.Body.Close(); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RPC HTTP status %d", resp.StatusCode)
	}
	var decoded rpcResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("RPC error %d: %s", decoded.Error.Code, decoded.Error.Message)
	}
	if decoded.Result == "" {
		return "", errors.New("RPC result is empty")
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
