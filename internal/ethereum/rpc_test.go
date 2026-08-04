package ethereum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func staticRPCServer(t *testing.T, result string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))
	}))
}

func staticRPCErrorServer(t *testing.T, code int, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": code, "message": message},
		})
	}))
}

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

func TestReadRequiresEveryConfiguredProvider(t *testing.T) {
	t.Parallel()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	var fallbackCalled atomic.Bool
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalled.Store(true)
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
	if _, err := client.BlockNumber(context.Background()); err == nil || errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider failure", err)
	}
	if !fallbackCalled.Load() {
		t.Fatal("healthy provider was not queried concurrently")
	}
}

func TestConfiguredProvidersAreQueriedConcurrently(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			started <- struct{}{}
			<-release
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":"0x1"}`))
		}))
	}
	primary, fallback := server(), server()
	defer primary.Close()
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	done := make(chan error, 1)
	go func() {
		_, err := client.ChainID(context.Background())
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("providers were not called concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestProviderDisagreementFailsClosed(t *testing.T) {
	t.Parallel()
	primary := staticRPCServer(t, `"0x1"`)
	defer primary.Close()
	fallback := staticRPCServer(t, `"0x2"`)
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	if _, err := client.ChainID(context.Background()); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestLatestHeadAllowsBoundedSkewAndUsesLowerHeight(t *testing.T) {
	t.Parallel()
	primary := staticRPCServer(t, `"0x64"`)
	defer primary.Close()
	fallback := staticRPCServer(t, `"0x66"`)
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	head, err := client.BlockNumber(context.Background())
	if err != nil || head != 100 {
		t.Fatalf("head = %d, err = %v", head, err)
	}
}

func TestLatestHeadRejectsExcessiveSkew(t *testing.T) {
	t.Parallel()
	primary := staticRPCServer(t, `"0x64"`)
	defer primary.Close()
	fallback := staticRPCServer(t, `"0x67"`)
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	if _, err := client.BlockNumber(context.Background()); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestLatestBlockPinsLowerAgreedHead(t *testing.T) {
	t.Parallel()
	var fixedReads atomic.Int64
	server := func(head string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params []any           `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			result := `"` + head + `"`
			if request.Method == "eth_getBlockByNumber" {
				if len(request.Params) != 2 || request.Params[0] != "0x64" {
					t.Errorf("fixed block params = %v", request.Params)
				}
				fixedReads.Add(1)
				result = `{"hash":"0x` + strings.Repeat("ab", 32) + `","number":"0x64","baseFeePerGas":"0x1"}`
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))
		}))
	}
	primary, fallback := server("0x64"), server("0x66")
	defer primary.Close()
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	block, err := client.BlockByNumber(context.Background(), nil)
	if err != nil || block.Number != 100 || block.BaseFee != "1" {
		t.Fatalf("block = %+v, err = %v", block, err)
	}
	if fixedReads.Load() != 2 {
		t.Fatalf("fixed reads = %d, want 2", fixedReads.Load())
	}
}

func TestLatestStateRequiresExactAgreement(t *testing.T) {
	t.Run("transaction count", func(t *testing.T) {
		primary := staticRPCServer(t, `"0x1"`)
		defer primary.Close()
		fallback := staticRPCServer(t, `"0x2"`)
		defer fallback.Close()
		client := NewClient(primary.URL, fallback.URL, time.Second, 0)
		if _, err := client.TransactionCount(context.Background(), "0x1111111111111111111111111111111111111111"); !errors.Is(err, ErrProviderDisagreement) {
			t.Fatalf("err = %v, want provider disagreement", err)
		}
	})
	t.Run("balance", func(t *testing.T) {
		primary := staticRPCServer(t, `"0x1"`)
		defer primary.Close()
		fallback := staticRPCServer(t, `"0x2"`)
		defer fallback.Close()
		client := NewClient(primary.URL, fallback.URL, time.Second, 0)
		if _, err := client.Balance(context.Background(), "0x1111111111111111111111111111111111111111"); !errors.Is(err, ErrProviderDisagreement) {
			t.Fatalf("err = %v, want provider disagreement", err)
		}
	})
	t.Run("simulation", func(t *testing.T) {
		primary := staticRPCServer(t, `"0x01"`)
		defer primary.Close()
		fallback := staticRPCServer(t, `"0x02"`)
		defer fallback.Close()
		client := NewClient(primary.URL, fallback.URL, time.Second, 0)
		err := client.Call(context.Background(),
			"0x1111111111111111111111111111111111111111",
			"0x2222222222222222222222222222222222222222", []byte{1})
		if !errors.Is(err, ErrProviderDisagreement) {
			t.Fatalf("err = %v, want provider disagreement", err)
		}
	})
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

func TestTransactionReceiptNullValueDivergenceFailsClosed(t *testing.T) {
	t.Parallel()
	txHash := "0x" + strings.Repeat("ab", 32)
	primary := staticRPCServer(t, `null`)
	defer primary.Close()
	receipt := `{"transactionHash":"` + txHash + `","status":"0x1","blockNumber":"0x10",` +
		`"blockHash":"0x` + strings.Repeat("cd", 32) + `","gasUsed":"0x1","effectiveGasPrice":"0x1"}`
	fallback := staticRPCServer(t, receipt)
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	if _, err := client.TransactionReceipt(context.Background(), txHash); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestTransactionReceiptValueDivergenceFailsClosed(t *testing.T) {
	t.Parallel()
	txHash := "0x" + strings.Repeat("ab", 32)
	receipt := func(blockHash string) string {
		return `{"transactionHash":"` + txHash + `","status":"0x1","blockNumber":"0x10",` +
			`"blockHash":"` + blockHash + `","gasUsed":"0x1","effectiveGasPrice":"0x1"}`
	}
	primary := staticRPCServer(t, receipt("0x"+strings.Repeat("cd", 32)))
	defer primary.Close()
	fallback := staticRPCServer(t, receipt("0x"+strings.Repeat("ef", 32)))
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	if _, err := client.TransactionReceipt(context.Background(), txHash); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestTransactionReceipt(t *testing.T) {
	t.Parallel()
	txHash := "0x" + strings.Repeat("ab", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"transactionHash":"` + txHash + `","status":"0x1","blockNumber":"0x10",
			"blockHash":"0x` + strings.Repeat("cd", 32) + `",
			"gasUsed":"0xfb50","effectiveGasPrice":"0x3b9aca00"}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	receipt, err := client.TransactionReceipt(context.Background(), txHash)
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

func TestTransactionReceiptRejectsUnboundOrInvalidEvidence(t *testing.T) {
	t.Parallel()
	requested := "0x" + strings.Repeat("ab", 32)
	validBlock := "0x" + strings.Repeat("cd", 32)
	cases := []struct {
		name            string
		transactionHash string
		status          string
		blockHash       string
		want            string
	}{
		{name: "wrong transaction", transactionHash: "0x" + strings.Repeat("ef", 32), status: "0x1", blockHash: validBlock, want: "does not match requested hash"},
		{name: "missing transaction", transactionHash: "", status: "0x1", blockHash: validBlock, want: "receipt transaction hash"},
		{name: "non-hex transaction", transactionHash: "0x" + strings.Repeat("gg", 32), status: "0x1", blockHash: validBlock, want: "receipt transaction hash"},
		{name: "invalid status", transactionHash: requested, status: "0x2", blockHash: validBlock, want: "neither 0 nor 1"},
		{name: "malformed block hash", transactionHash: requested, status: "0x1", blockHash: "0xblock", want: "receipt block hash"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				response := map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]string{
					"transactionHash": testCase.transactionHash, "status": testCase.status,
					"blockNumber": "0x10", "blockHash": testCase.blockHash,
					"gasUsed": "0xfb50", "effectiveGasPrice": "0x3b9aca00",
				}}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := NewClient(server.URL, "", time.Second, 0)
			if _, err := client.TransactionReceipt(context.Background(), requested); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
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

func TestTransactionByHashNullValueDivergenceFailsClosed(t *testing.T) {
	t.Parallel()
	txHash := "0x" + strings.Repeat("ab", 32)
	primary := staticRPCServer(t, `null`)
	defer primary.Close()
	fallback := staticRPCServer(t, `{"hash":"`+txHash+`","blockNumber":null}`)
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	if _, err := client.TransactionByHash(context.Background(), txHash); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestTransactionByHashRejectsDifferentOrMalformedHash(t *testing.T) {
	t.Parallel()
	requested := "0x" + strings.Repeat("ab", 32)
	for _, returned := range []string{"0x" + strings.Repeat("cd", 32), "0xnot-a-hash"} {
		returned := returned
		t.Run(returned[:min(len(returned), 10)], func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{"hash": returned, "blockNumber": nil},
				})
			}))
			defer server.Close()
			client := NewClient(server.URL, "", time.Second, 0)
			if _, err := client.TransactionByHash(context.Background(), requested); err == nil {
				t.Fatal("unbound transaction response was accepted")
			}
		})
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

func TestBlockByNumberRejectsDifferentNumberAndMalformedHash(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, number, hash, want string
	}{
		{name: "different number", number: "0x11", hash: "0x" + strings.Repeat("cd", 32), want: "does not match requested number"},
		{name: "malformed hash", number: "0x10", hash: "0xblock", want: "block hash"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]string{"hash": testCase.hash, "number": testCase.number, "baseFeePerGas": "0x1"},
				})
			}))
			defer server.Close()
			client := NewClient(server.URL, "", time.Second, 0)
			number := uint64(16)
			if _, err := client.BlockByNumber(context.Background(), &number); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestFixedBlockRequiresExactAgreement(t *testing.T) {
	t.Parallel()
	block := func(hash string) string {
		return `{"hash":"` + hash + `","number":"0x10","baseFeePerGas":"0x1"}`
	}
	primary := staticRPCServer(t, block("0x"+strings.Repeat("ab", 32)))
	defer primary.Close()
	fallback := staticRPCServer(t, block("0x"+strings.Repeat("cd", 32)))
	defer fallback.Close()
	client := NewClient(primary.URL, fallback.URL, time.Second, 0)
	number := uint64(16)
	if _, err := client.BlockByNumber(context.Background(), &number); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want provider disagreement", err)
	}
}

func TestSendRawTransactionRejectsNonHexHash(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("gg", 32) + `"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "", time.Second, 0)
	if _, err := client.SendRawTransaction(context.Background(), "0xdeadbeef"); err == nil {
		t.Fatal("non-hex transaction hash was accepted")
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

func TestCallRequiresEveryProviderToProveRevert(t *testing.T) {
	call := func(client *Client) error {
		return client.Call(context.Background(),
			"0x1111111111111111111111111111111111111111",
			"0x2222222222222222222222222222222222222222", []byte{1})
	}
	t.Run("matching reverts", func(t *testing.T) {
		primary := staticRPCErrorServer(t, 3, "execution reverted")
		defer primary.Close()
		fallback := staticRPCErrorServer(t, 3, "execution reverted")
		defer fallback.Close()
		err := call(NewClient(primary.URL, fallback.URL, time.Second, 0))
		if !errors.Is(err, ErrSimulationReverted) {
			t.Fatalf("err = %v, want proven simulation revert", err)
		}
	})
	t.Run("revert and success disagree", func(t *testing.T) {
		primary := staticRPCErrorServer(t, 3, "execution reverted")
		defer primary.Close()
		fallback := staticRPCServer(t, `"0x"`)
		defer fallback.Close()
		err := call(NewClient(primary.URL, fallback.URL, time.Second, 0))
		if !errors.Is(err, ErrProviderDisagreement) || errors.Is(err, ErrSimulationReverted) {
			t.Fatalf("err = %v, want disagreement only", err)
		}
	})
	for _, revertFirst := range []bool{true, false} {
		name := "transport then revert"
		if revertFirst {
			name = "revert then transport"
		}
		t.Run(name, func(t *testing.T) {
			revert := staticRPCErrorServer(t, 3, "execution reverted")
			defer revert.Close()
			unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer unavailable.Close()
			primary, fallback := unavailable.URL, revert.URL
			if revertFirst {
				primary, fallback = revert.URL, unavailable.URL
			}
			err := call(NewClient(primary, fallback, time.Second, 0))
			if err == nil || errors.Is(err, ErrSimulationReverted) {
				t.Fatalf("err = %v, transport failure must not retire the payment as reverted", err)
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

func TestRPCTransportErrorsDoNotExposeEndpointCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + "/private-project-token?api-key=query-credential"
	server.Close()
	client := NewClient(endpoint, "", time.Second, 0)
	_, err := client.ChainID(context.Background())
	if err == nil {
		t.Fatal("closed authenticated endpoint unexpectedly answered")
	}
	for _, secret := range []string{"private-project-token", "query-credential", "api-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("RPC transport error exposed endpoint credential %q: %v", secret, err)
		}
	}
}
