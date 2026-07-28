package settlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// workerBatch bounds how many payments a worker leases per tick. Small on
// purpose: settlement latency is dominated by block times, and a bounded batch
// keeps a slow RPC provider from holding many leases at once.
const workerBatch = 8

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
	w.process(ctx)
	ticker := time.NewTicker(w.service.cfg.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.process(ctx)
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
		// Transitions are audited with the coarse actor type the schema
		// allows; the full identity stays on the lease, where granularity
		// matters for ownership.
		if err := w.advance(ctx, lease.PaymentID, "worker"); err != nil {
			w.logger.WarnContext(ctx, "advance payment failed",
				"worker", w.name, "payment_id", lease.PaymentID,
				"payment_identity", lease.PaymentIdentity, "error", err)
		}
		w.service.release(ctx, lease.PaymentID, w.identity)
	}
}
