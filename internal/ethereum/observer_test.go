package ethereum

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type countingObserver struct{ requests, failures int }

func (c *countingObserver) ObserveRPC(failed bool) {
	c.requests++
	if failed {
		c.failures++
	}
}

// Counts must come from the real request path, not from a hand-called method.
func TestObserverCountsRealRequestsAndRetries(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()

	o := &countingObserver{}
	c := NewClient(ok.URL, "", 2*time.Second, 0)
	c.Observe(o)
	if _, err := c.ChainID(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.requests != 1 || o.failures != 0 {
		t.Fatalf("success: requests=%d failures=%d", o.requests, o.failures)
	}

	// Retries must each be counted: a read that only succeeds on its fallback has
	// still exercised a failing provider, which is what an operator needs to see.
	o2 := &countingObserver{}
	c2 := NewClient(bad.URL, ok.URL, 2*time.Second, 1)
	c2.Observe(o2)
	if _, err := c2.ChainID(context.Background()); err != nil {
		t.Fatalf("fallback should have succeeded: %v", err)
	}
	if o2.requests != 2 || o2.failures != 1 {
		t.Fatalf("fallback: requests=%d failures=%d, want 2 and 1", o2.requests, o2.failures)
	}
}
