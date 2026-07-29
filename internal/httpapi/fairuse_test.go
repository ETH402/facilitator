package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/store"
)

type fakeAccountant struct {
	usage  store.FairUse
	err    error
	calls  int
	lastID string
	limit  int64
	window time.Duration
}

func (f *fakeAccountant) CountMerchantRequest(_ context.Context, merchantID string, limit int64, window time.Duration, _ time.Time) (store.FairUse, error) {
	f.calls++
	f.lastID, f.limit, f.window = merchantID, limit, window
	return f.usage, f.err
}

func fairUseDeps(accountant FairUseAccountant, limit int64, window time.Duration) Dependencies {
	return Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Metrics: metrics.New(),
		FairUse: accountant, MerchantRequestsPerWindow: limit, FairUseWindow: window,
	}
}

func runFairUse(t *testing.T, deps Dependencies) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	served := false
	handler := deps.fairUse(func(w http.ResponseWriter, _ *http.Request, _ merchant.Merchant) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/v1/me", nil),
		merchant.Merchant{ID: "11111111-1111-1111-1111-111111111111"})
	return recorder, served
}

func TestFairUseServesWithinTheAllowance(t *testing.T) {
	t.Parallel()
	resets := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	accountant := &fakeAccountant{usage: store.FairUse{
		Limit: 100, Used: 3, Remaining: 97, ResetsAt: resets,
	}}
	recorder, served := runFairUse(t, fairUseDeps(accountant, 100, time.Hour))
	if !served || recorder.Code != http.StatusOK {
		t.Fatalf("served=%v code=%d, want served with 200", served, recorder.Code)
	}
	if got := recorder.Header().Get("X-RateLimit-Remaining"); got != "97" {
		t.Errorf("remaining header = %q, want 97", got)
	}
	if got := recorder.Header().Get("X-RateLimit-Limit"); got != "100" {
		t.Errorf("limit header = %q, want 100", got)
	}
	if recorder.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("a client cannot back off correctly without a reset time")
	}
}

func TestFairUseRefusesPastTheAllowance(t *testing.T) {
	t.Parallel()
	accountant := &fakeAccountant{usage: store.FairUse{
		Limit: 10, Used: 11, Remaining: 0, ResetsAt: time.Now().Add(5 * time.Minute), Exceeded: true,
	}}
	recorder, served := runFairUse(t, fairUseDeps(accountant, 10, time.Hour))
	if served {
		t.Fatal("a merchant past its allowance was served")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Errorf("code = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells the client to guess")
	}
	if !strings.Contains(recorder.Body.String(), "fair_use_exceeded") {
		t.Errorf("body should name the control that refused: %s", recorder.Body)
	}
}

// TestFairUseFailsOpenWhenAccountingBreaks is a deliberate choice, not an
// oversight. This is a fair-use control, not an authorization decision: failing
// closed would turn a bookkeeping outage into an outage for every paying merchant,
// which is a worse failure than briefly not enforcing a courtesy limit.
func TestFairUseFailsOpenWhenAccountingBreaks(t *testing.T) {
	t.Parallel()
	accountant := &fakeAccountant{err: errors.New("connection reset")}
	_, served := runFairUse(t, fairUseDeps(accountant, 10, time.Hour))
	if !served {
		t.Error("accounting failure must not deny a merchant its request")
	}
}

// TestFairUseKeysOnTheMerchant is the detail that decides whether the control works
// at all. Keying on the API key would let a merchant multiply its allowance by
// minting keys, which is a self-service operation.
func TestFairUseKeysOnTheMerchant(t *testing.T) {
	t.Parallel()
	accountant := &fakeAccountant{usage: store.FairUse{Limit: 10, Remaining: 9}}
	deps := fairUseDeps(accountant, 10, 15*time.Minute)
	if _, served := runFairUse(t, deps); !served {
		t.Fatal("expected the request to be served")
	}
	if accountant.lastID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("accounted against %q, want the merchant id", accountant.lastID)
	}
	if accountant.limit != 10 || accountant.window != 15*time.Minute {
		t.Errorf("passed limit=%d window=%s, want 10 and 15m", accountant.limit, accountant.window)
	}
}

func TestFairUseDisabledWhenUnconfigured(t *testing.T) {
	t.Parallel()
	for name, deps := range map[string]Dependencies{
		"no accountant": fairUseDeps(nil, 10, time.Hour),
		"zero limit":    fairUseDeps(&fakeAccountant{}, 0, time.Hour),
		"zero window":   fairUseDeps(&fakeAccountant{}, 10, 0),
	} {
		t.Run(name, func(t *testing.T) {
			recorder, served := runFairUse(t, deps)
			if !served || recorder.Code != http.StatusOK {
				t.Errorf("served=%v code=%d, want the request served", served, recorder.Code)
			}
			if accountant, ok := deps.FairUse.(*fakeAccountant); ok && accountant.calls != 0 {
				t.Errorf("accounted %d requests while disabled", accountant.calls)
			}
		})
	}
}
