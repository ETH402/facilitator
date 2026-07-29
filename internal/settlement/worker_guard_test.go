package settlement

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestWorkerPanicDoesNotEscapeOrStrandLease pins the guard around worker steps.
//
// Workers run as bare goroutines, so before the guard an unrecovered panic
// terminated the whole process — taking HTTP serving down with it, even though
// the HTTP path has its own recovery middleware. The lease must still be
// released, or the payment would be stranded until it lapsed.
func TestWorkerPanicDoesNotEscapeOrStrandLease(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store := &fakeStore{leases: []Lease{
		{PaymentID: "payment-1", PaymentIdentity: "pay_one"},
		{PaymentID: "payment-2", PaymentIdentity: "pay_two"},
	}}
	service := NewService(store, nil, nil, Config{
		LeaseDuration: time.Minute, WorkerInterval: time.Minute,
	}, logger)
	advanced := 0
	worker := &Worker{
		name: "panicking", identity: "test", service: service,
		states: []State{StateBroadcasting},
		advance: func(context.Context, string, string) error {
			advanced++
			panic("simulated worker fault")
		},
		logger: logger, now: time.Now,
	}

	// Must not panic out of process.
	worker.process(context.Background())

	// Both payments attempted: a panic on the first must not skip the rest of
	// the batch, and both leases must be released.
	if advanced != 2 {
		t.Fatalf("advance calls = %d, want 2", advanced)
	}
	if store.released != 2 {
		t.Fatalf("releases = %d, want 2", store.released)
	}
	if got := logs.String(); !bytes.Contains([]byte(got), []byte("panic recovered")) {
		t.Fatalf("panic was not logged: %s", got)
	}
}

// A panic in the claim step must not escape Run's tick either.
func TestWorkerTickGuardsClaimPanic(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service := NewService(panickingClaimStore{}, nil, nil, Config{
		LeaseDuration: time.Minute, WorkerInterval: time.Hour,
	}, logger)
	worker := service.worker("claim-panic", []State{StateBroadcasting},
		func(context.Context, string, string) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Run's first tick happens before the context is checked.
	worker.Run(ctx)
	if got := logs.String(); !bytes.Contains([]byte(got), []byte("panic recovered")) {
		t.Fatalf("claim panic was not logged: %s", got)
	}
}

type panickingClaimStore struct{ Store }

func (panickingClaimStore) ClaimPayments(context.Context, ClaimRequest) ([]Lease, error) {
	panic("simulated claim fault")
}
