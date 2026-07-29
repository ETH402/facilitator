package settlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"
)

// guard converts a panic into a logged error.
//
// Workers run as bare goroutines, where an unrecovered panic terminates the
// whole process and takes HTTP serving down with it — the HTTP path has
// recovery middleware, so without this a single malformed row is a worse
// outage than a failed request. Every worker tick and every per-payment step
// runs inside one, so one bad payment neither kills the process nor skips the
// rest of its batch.
func guard(ctx context.Context, logger *slog.Logger, worker, stage string, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorContext(ctx, "settlement worker panic recovered",
				"worker", worker, "stage", stage, "panic", recovered,
				"stack", string(debug.Stack()))
		}
	}()
	fn()
}

// workerBatch bounds how many payments a worker leases per tick. Small on
// purpose: settlement latency is dominated by block times, and a bounded batch
// keeps a slow RPC provider from holding many leases at once.
const workerBatch = 8

// Heartbeater records that a worker completed a tick. *metrics.Registry satisfies
// it; the interface keeps settlement free of a metrics dependency.
type Heartbeater interface {
	Heartbeat(worker string, at time.Time)
}

// Worker repeatedly claims leased payments and advances them. One instance
// serves one concern (broadcast or confirmation); both share the claim loop
// because ADR-0004 decision 10 makes the lease the unit of ownership.
type Worker struct {
	name     string
	identity string
	service  *Service
	states   []State
	advance  func(ctx context.Context, paymentID, actor string) error
	logger   *slog.Logger
	now      func() time.Time
}

// BroadcastWorker drives committed intents to the network: it re-runs the
// broadcast pipeline for payments whose inline broadcast never happened or
// never finished.
func (s *Service) BroadcastWorker() *Worker {
	return s.worker("broadcast", []State{StateBroadcasting}, func(ctx context.Context, paymentID, actor string) error {
		work, err := s.store.LoadSettlementWork(ctx, paymentID)
		if err != nil {
			return fmt.Errorf("load settlement work: %w", err)
		}
		_, err = s.broadcastClaimed(ctx, work, actor)
		return err
	})
}

// ConfirmationWorker watches broadcast transactions and advances payments to
// confirming, confirmed, or reverted from their receipts. Replaced payments
// are claimed too: the active replacement is what must be watched, while
// recovery watches the superseded original.
func (s *Service) ConfirmationWorker() *Worker {
	return s.worker("confirmation", []State{StateBroadcast, StateConfirming, StateReplaced}, s.Confirmation)
}

func (s *Service) worker(name string, states []State, advance func(context.Context, string, string) error) *Worker {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	return &Worker{
		name:     name,
		identity: fmt.Sprintf("%s/%s/%d", name, hostname, os.Getpid()),
		service:  s,
		states:   states,
		advance:  advance,
		logger:   s.logger,
		now:      s.now,
	}
}

// Run claims and advances payments until the context is cancelled. The first
// tick runs immediately so a restart does not idle for a full interval while
// intents wait.
func (w *Worker) Run(ctx context.Context) {
	tick := func() {
		guard(ctx, w.logger, w.name, "tick", func() { w.process(ctx) })
		// After the tick, not before: the heartbeat says work completed, so a
		// worker wedged inside process() stops reporting rather than reporting
		// health it does not have. The guard means a panicking tick still beats,
		// which is correct — the loop survived and will try again.
		w.service.beat(w.name)
	}
	tick()
	ticker := time.NewTicker(w.service.cfg.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// process claims one batch and advances each payment independently: a failure
// is logged and the loop continues, because one bad payment must never stall
// the rest of the queue.
func (w *Worker) process(ctx context.Context) {
	leases, err := w.service.store.ClaimPayments(ctx, ClaimRequest{
		Worker: w.identity, States: w.states,
		Duration: w.service.cfg.LeaseDuration, Limit: workerBatch, Now: w.now(),
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.logger.ErrorContext(ctx, "claim payments failed", "worker", w.name, "error", err)
		}
		return
	}
	for _, lease := range leases {
		if ctx.Err() != nil {
			return
		}
		// The batch was leased all at once, so later entries may have waited
		// behind slow RPC calls. Renew immediately before acting; an expired
		// lease must be re-claimed rather than touched by its former owner.
		if _, err := w.service.store.RenewLease(ctx, lease.PaymentID, w.identity,
			w.now(), w.service.cfg.LeaseDuration); err != nil {
			if !errors.Is(err, ErrLeaseLost) && !errors.Is(err, context.Canceled) {
				w.logger.WarnContext(ctx, "renew payment lease failed",
					"worker", w.name, "payment_id", lease.PaymentID, "error", err)
				w.service.release(ctx, lease.PaymentID, w.identity)
			}
			continue
		}
		// Transitions are audited with the coarse actor type the schema
		// allows; the full identity stays on the lease, where granularity
		// matters for ownership.
		guard(ctx, w.logger, w.name, "advance", func() {
			if err := w.advance(ctx, lease.PaymentID, "worker"); err != nil {
				w.logger.WarnContext(ctx, "advance payment failed",
					"worker", w.name, "payment_id", lease.PaymentID,
					"payment_identity", lease.PaymentIdentity, "error", err)
			}
		})
		// Released outside the guard so a panicking advance still frees its lease
		// instead of stranding the payment until the lease lapses.
		w.service.release(ctx, lease.PaymentID, w.identity)
	}
}
