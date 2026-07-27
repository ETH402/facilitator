package ethereum

import (
	"context"
	"net/http"
	"net/http/httptest"
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
