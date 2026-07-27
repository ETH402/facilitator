package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/stats"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

type fakeRPC struct {
	chain uint64
	err   error
}

func (f fakeRPC) ChainID(context.Context) (uint64, error)     { return f.chain, f.err }
func (f fakeRPC) BlockNumber(context.Context) (uint64, error) { return 0, f.err }

type statsSource struct{}

func (statsSource) AggregateStats(context.Context) (stats.Aggregate, error) {
	return stats.Aggregate{}, nil
}

func testServer(dbErr, rpcErr error, chain uint64) http.Handler {
	registry := metrics.New()
	return New(Dependencies{
		Logger: slog.Default(), Database: fakeDB{dbErr}, Ethereum: fakeRPC{chain, rpcErr},
		Stats: stats.NewService(statsSource{}, time.Now(), 0), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100,
	}).Handler()
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()
	handler := testServer(nil, nil, 1)
	for _, path := range []string{"/health/live", "/health/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
	}
}

func TestReadinessFailure(t *testing.T) {
	t.Parallel()
	handler := testServer(errors.New("down"), nil, 1)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestStatsSchema(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "service", "version", "network", "asset", "total_payment_volume_atomic", "status"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing stable field %q", key)
		}
	}
}

func TestWrongRPCChainNotReady(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 8453).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("wrong chain returned %d", rec.Code)
	}
}
