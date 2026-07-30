package ethereum

import (
	"context"
	"encoding/json"
	"errors"
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
	// The fallback is the second attempt, so the request id has moved on; a
	// compliant endpoint echoes whatever id the request carried.
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":"0x2a"}`))
	}))
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 1)
	got, err := client.BlockNumber(context.Background())
	if err != nil || got != 42 {
		t.Fatalf("got %d, %v", got, err)
	}
}

// A response whose id does not match the request must error: trusting it
// would attribute another request's result — or another request's error — to
// this call.
func TestResponseIDMismatchRejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":999,"result":"0x1"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	if _, err := client.ChainID(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "does not match request id") {
		t.Fatalf("err = %v, want a response id mismatch", err)
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

func TestTransactionByHashPending(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0x` + strings.Repeat("ab", 32) + `","blockNumber":null}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	tx, err := client.TransactionByHash(context.Background(), "0x"+strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if tx == nil || tx.BlockNumber != nil {
		t.Fatalf("tx = %+v, want pending", tx)
	}
}

func TestTransactionByHashUnknown(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	tx, err := client.TransactionByHash(context.Background(), "0x"+strings.Repeat("ab", 32))
	if err != nil || tx != nil {
		t.Fatalf("got %v, %v", tx, err)
	}
}

func TestBlockByNumber(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"hash":"0x` + strings.Repeat("cd", 32) + `","number":"0x10",
			"baseFeePerGas":"0x3b9aca00"}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	number := uint64(16)
	block, err := client.BlockByNumber(context.Background(), &number)
	if err != nil {
		t.Fatalf("block: %v", err)
	}
	if block.Hash != "0x"+strings.Repeat("cd", 32) || block.Number != 16 || block.BaseFee != "1000000000" {
		t.Fatalf("block = %+v", block)
	}
}

// The revert-versus-transient distinction decides whether a payment is abandoned
// or retried, so it is deliberately conservative: only an explicit revert counts.
func TestCallClassifiesRevertsAndTransientFailures(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		status   int
		reverted bool
		wantErr  bool
	}{
		{name: "success", body: `{"jsonrpc":"2.0","id":1,"result":"0x"}`, status: 200},
		{
			name: "geth revert code", status: 200, reverted: true, wantErr: true,
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted"}}`,
		},
		{
			// Provider that reports a revert without the conventional code.
			name: "revert by message", status: 200, reverted: true, wantErr: true,
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"VM Exception: execution reverted"}}`,
		},
		{
			// Must NOT be treated as a revert: abandoning a payment over a rate
			// limit or a node bug would lose money that could have settled.
			name: "rate limited", status: 200, wantErr: true,
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"limit exceeded"}}`,
		},
		{name: "transport failure", body: "", status: 503, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			client := NewClient(server.URL, "", 2*time.Second, 0)
			err := client.Call(context.Background(), "0x1111111111111111111111111111111111111111",
				"0x2222222222222222222222222222222222222222", []byte{0xde, 0xad, 0xbe, 0xef})
			if testCase.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, testCase.wantErr)
			}
			if got := errors.Is(err, ErrSimulationReverted); got != testCase.reverted {
				t.Fatalf("reverted = %v, want %v (err %v)", got, testCase.reverted, err)
			}
		})
	}
}

func TestValidateProvidersChecksBothIndependently(t *testing.T) {
	t.Parallel()
	server := func(result string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + result + `"}`))
		}))
	}
	primary := server("0x1")
	defer primary.Close()
	fallback := server("0x1")
	defer fallback.Close()
	if err := ValidateProviders(context.Background(), primary.URL, fallback.URL, time.Second, 1); err != nil {
		t.Fatalf("valid providers rejected: %v", err)
	}
	wrong := server("0x2105")
	defer wrong.Close()
	err := ValidateProviders(context.Background(), primary.URL, wrong.URL, time.Second, 1)
	if err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("wrong-chain fallback error = %v", err)
	}
}
