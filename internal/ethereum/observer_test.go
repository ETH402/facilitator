package ethereum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type countingObserver struct {
	requests, failures, disagreements atomic.Int64
}

func (c *countingObserver) ObserveRPC(failed bool) {
	c.requests.Add(1)
	if failed {
		c.failures.Add(1)
	}
}

func (c *countingObserver) ObserveRPCDisagreement() { c.disagreements.Add(1) }

// Counts must come from the real request path, not from a hand-called method.
func TestObserverCountsRealRequestsAndRetries(t *testing.T) {
	// The fallback attempt carries a fresh request id, so the server echoes
	// whatever id each request brought, as a compliant endpoint does.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":"0x1"}`))
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
	if o.requests.Load() != 1 || o.failures.Load() != 0 {
		t.Fatalf("success: requests=%d failures=%d", o.requests.Load(), o.failures.Load())
	}

	// Both providers are mandatory. The failing provider consumes its retry
	// budget while the healthy provider answers once; every actual attempt is
	// counted even though the logical read fails closed.
	o2 := &countingObserver{}
	c2 := NewClient(bad.URL, ok.URL, 2*time.Second, 1)
	c2.Observe(o2)
	if _, err := c2.ChainID(context.Background()); err == nil {
		t.Fatal("one-provider result was accepted")
	}
	if o2.requests.Load() != 3 || o2.failures.Load() != 2 {
		t.Fatalf("required providers: requests=%d failures=%d, want 3 and 2",
			o2.requests.Load(), o2.failures.Load())
	}
}

func TestObserverCountsProviderDisagreementSeparately(t *testing.T) {
	primary := staticRPCServer(t, `"0x1"`)
	defer primary.Close()
	fallback := staticRPCServer(t, `"0x2"`)
	defer fallback.Close()
	observer := &countingObserver{}
	client := NewClient(primary.URL, fallback.URL, 2*time.Second, 0)
	client.Observe(observer)
	if _, err := client.ChainID(context.Background()); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("err = %v, want disagreement", err)
	}
	if observer.requests.Load() != 2 || observer.failures.Load() != 0 || observer.disagreements.Load() != 1 {
		t.Fatalf("requests=%d failures=%d disagreements=%d",
			observer.requests.Load(), observer.failures.Load(), observer.disagreements.Load())
	}
}
