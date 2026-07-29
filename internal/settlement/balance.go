package settlement

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"time"
)

// BalanceReader reads an account's wei balance. *ethereum.Client satisfies it.
type BalanceReader interface {
	Balance(ctx context.Context, address string) (*big.Int, error)
}

// BalanceRecorder receives observations. *metrics.Registry satisfies it; the
// interface exists so settlement does not depend on the metrics package.
type BalanceRecorder interface {
	SetSignerBalance(wei *big.Int, at time.Time)
	IncSignerBalanceError()
}

// BalanceWorker publishes the settlement signer's ether balance.
//
// ADR-0004 decision 8 makes the bounded hot balance the final loss bound if the
// policy signing boundary or its KMS authority is compromised. The boundary
// structurally limits ordinary facilitator requests to canonical-USDC
// settlement, but the key holder is still capable of spending gas. A bound
// nobody observes is a convention, not a control — this makes it observable.
//
// It deliberately does not compute a burn rate. Prometheus derives that from the
// published gauge across restarts, which an in-process counter cannot.
type BalanceWorker struct {
	address  string
	interval time.Duration
	chain    BalanceReader
	recorder BalanceRecorder
	logger   *slog.Logger
}

// BalanceWorker builds the poller for this service's signer. It returns nil when
// there is nothing to watch, so the caller can start it unconditionally.
func (s *Service) BalanceWorker(chain BalanceReader, recorder BalanceRecorder) *BalanceWorker {
	if chain == nil || recorder == nil || s.cfg.SignerAddress == "" {
		return nil
	}
	interval := s.cfg.WorkerInterval
	if interval <= 0 {
		interval = time.Minute
	}
	return &BalanceWorker{
		address: s.cfg.SignerAddress, interval: interval,
		chain: chain, recorder: recorder, logger: s.logger,
	}
}

// Run publishes the balance until the context is cancelled. The first read
// happens immediately so a restart does not leave the gauge unset for a full
// interval, which would be indistinguishable from a failing read.
func (w *BalanceWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	w.observe(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.observe(ctx)
		}
	}
}

// observe records one reading. A failed read increments the error counter and
// leaves the previous value and its timestamp untouched: overwriting the balance
// with zero would look exactly like a drained account, and refreshing the
// timestamp would make a stale figure look current. Staleness is the signal.
func (w *BalanceWorker) observe(ctx context.Context) {
	balance, err := w.chain.Balance(ctx, w.address)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		w.recorder.IncSignerBalanceError()
		w.logger.WarnContext(ctx, "signer balance read failed",
			"signer_address", w.address, "error", err)
		return
	}
	w.recorder.SetSignerBalance(balance, time.Now())
}
