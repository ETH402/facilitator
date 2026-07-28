package ethereum

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChainID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	got, err := client.ChainID(context.Background())
	if err != nil || got != 1 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestReadFallback(t *testing.T) {
	t.Parallel()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2a"}`))
	}))
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 1)
	got, err := client.BlockNumber(context.Background())
	if err != nil || got != 42 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestSendRawTransaction(t *testing.T) {
	t.Parallel()
	var gotMethod string
	var gotParams []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotMethod, gotParams = request.Method, request.Params
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("ab", 32) + `"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	hash, err := client.SendRawTransaction(context.Background(), "0xdeadbeef")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if hash != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("hash = %q", hash)
	}
	if gotMethod != "eth_sendRawTransaction" || len(gotParams) != 1 || gotParams[0] != "0xdeadbeef" {
		t.Fatalf("request = %s %v", gotMethod, gotParams)
	}
}

func TestSendRawTransactionDoesNotRotate(t *testing.T) {
	t.Parallel()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallbackCalled := false
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalled = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("ab", 32) + `"}`))
	}))
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 2)
	if _, err := client.SendRawTransaction(context.Background(), "0xdeadbeef"); err == nil {
		t.Fatal("expected an error")
	}
	// A failed broadcast is ambiguous; retrying the fallback could double-spend
	// the nonce if the primary actually accepted the transaction.
	if fallbackCalled {
		t.Fatal("broadcast rotated to the fallback provider")
	}
}

func TestTransactionReceiptPending(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	receipt, err := client.TransactionReceipt(context.Background(), "0x"+strings.Repeat("ab", 32))
	if err != nil || receipt != nil {
		t.Fatalf("got %v, %v", receipt, err)
	}
}

func TestTransactionReceipt(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"status":"0x1","blockNumber":"0x10",
			"blockHash":"0x` + strings.Repeat("cd", 32) + `",
			"gasUsed":"0xfb50","effectiveGasPrice":"0x3b9aca00"}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	receipt, err := client.TransactionReceipt(context.Background(), "0x"+strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if receipt.Status != 1 || receipt.BlockNumber != 16 || receipt.GasUsed != 64336 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.BlockHash != "0x"+strings.Repeat("cd", 32) {
		t.Fatalf("block hash = %q", receipt.BlockHash)
	}
	if receipt.EffectiveGasPrice != "1000000000" {
		t.Fatalf("gas price = %q", receipt.EffectiveGasPrice)
	}
}
