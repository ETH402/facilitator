package retention

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/store"
)

type captureStore struct {
	request store.RetentionRequest
	calls   int
}

type captureObserver struct {
	redacted int64
	failed   bool
	at       time.Time
}

func (o *captureObserver) ObserveRetention(redacted int64, failed bool, at time.Time) {
	o.redacted, o.failed, o.at = redacted, failed, at
}

func (s *captureStore) ApplyRetention(_ context.Context, request store.RetentionRequest) (store.RetentionResult, error) {
	s.request = request
	s.calls++
	return store.RetentionResult{}, nil
}

func TestWorkerRunsImmediately(t *testing.T) {
	t.Parallel()
	database := &captureStore{}
	observer := &captureObserver{}
	worker := New(Config{
		Store: database, PaymentAfter: 30 * 24 * time.Hour,
		EphemeralAfter: 24 * time.Hour, RevokedKeyAfter: 30 * 24 * time.Hour,
		Interval: time.Hour, BatchSize: 500, Logger: slog.Default(), Observer: observer,
	})
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker.Run(ctx)
	if database.calls != 1 {
		t.Fatalf("retention calls = %d, want 1", database.calls)
	}
	if database.request.Now != now || database.request.BatchSize != 500 {
		t.Fatalf("request = %+v", database.request)
	}
	if observer.failed || observer.at != now {
		t.Fatalf("observer = %+v", observer)
	}
}

type panicStore struct {
	calls int
}

func (s *panicStore) ApplyRetention(context.Context, store.RetentionRequest) (store.RetentionResult, error) {
	s.calls++
	panic("boom")
}

func TestProcessRecoversPanic(t *testing.T) {
	t.Parallel()
	database := &panicStore{}
	observer := &captureObserver{}
	worker := New(Config{
		Store: database, PaymentAfter: 30 * 24 * time.Hour,
		EphemeralAfter: 24 * time.Hour, RevokedKeyAfter: 30 * 24 * time.Hour,
		Interval: time.Hour, BatchSize: 500, Logger: slog.Default(), Observer: observer,
	})
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }
	// Two passes: the first must recover the panic, the second proves the
	// worker is still usable afterwards.
	worker.process(context.Background())
	worker.process(context.Background())
	if database.calls != 2 {
		t.Fatalf("retention calls = %d, want 2", database.calls)
	}
	if !observer.failed || observer.at != now {
		t.Fatalf("observer = %+v, want a failed observation at %v", observer, now)
	}
}
