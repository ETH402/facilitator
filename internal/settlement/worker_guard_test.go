package settlement

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
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

type recordingHeartbeat struct {
	mu    sync.Mutex
	beats map[string]int
}

func (h *recordingHeartbeat) Heartbeat(worker string, _ time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.beats == nil {
		h.beats = map[string]int{}
	}
	h.beats[worker]++
}

func (h *recordingHeartbeat) count(worker string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beats[worker]
}

// Health is derived from worker heartbeats, so they must come from the real Run
// loop rather than being reported by whatever starts it.
func TestWorkerRunHeartbeats(t *testing.T) {
	beats := &recordingHeartbeat{}
	service := NewService(&fakeStore{}, nil, nil, Config{
		LeaseDuration: time.Minute, WorkerInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Observe(beats)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	service.BroadcastWorker().Run(ctx)

	if got := beats.count("broadcast"); got < 2 {
		t.Fatalf("broadcast heartbeats = %d, want at least 2 ticks", got)
	}
}

// A panicking tick must still beat: the loop survived and will try again, so
// reporting it dead would be wrong. A wedged tick is what must stop beating, and
// that follows from the beat happening after process returns.
func TestPanickingTickStillHeartbeats(t *testing.T) {
	beats := &recordingHeartbeat{}
	service := NewService(panickingClaimStore{}, nil, nil, Config{
		LeaseDuration: time.Minute, WorkerInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.Observe(beats)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.worker("broadcast", []State{StateBroadcasting},
		func(context.Context, string, string) error { return nil }).Run(ctx)
	if got := beats.count("broadcast"); got != 1 {
		t.Fatalf("heartbeats after a panicking tick = %d, want 1", got)
	}
}
