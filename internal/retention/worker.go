package retention

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/ETH402/facilitator/internal/store"
)

type Store interface {
	ApplyRetention(context.Context, store.RetentionRequest) (store.RetentionResult, error)
}

type Observer interface {
	ObserveRetention(redacted int64, failed bool, at time.Time)
}

type Config struct {
	Store           Store
	PaymentAfter    time.Duration
	EphemeralAfter  time.Duration
	RevokedKeyAfter time.Duration
	Interval        time.Duration
	BatchSize       int
	Logger          *slog.Logger
	Observer        Observer
}

type Worker struct {
	config Config
	now    func() time.Time
}

func New(config Config) *Worker {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Worker{config: config, now: time.Now}
}

func (w *Worker) Run(ctx context.Context) {
	w.process(ctx)
	ticker := time.NewTicker(w.config.Interval)
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

func (w *Worker) process(ctx context.Context) {
	// Workers run as bare goroutines, where an unrecovered panic terminates the
	// whole process (see guard in internal/settlement/worker.go). Recover per
	// pass so one bad pass neither kills the process nor stops future passes.
	defer func() {
		if recovered := recover(); recovered != nil {
			w.config.Logger.ErrorContext(ctx, "retention worker panic recovered",
				"panic", recovered, "stack", string(debug.Stack()))
			if w.config.Observer != nil {
				w.config.Observer.ObserveRetention(0, true, w.now().UTC())
			}
		}
	}()
	now := w.now().UTC()
	result, err := w.config.Store.ApplyRetention(ctx, store.RetentionRequest{
		Now: now, PaymentAfter: w.config.PaymentAfter,
		EphemeralAfter:  w.config.EphemeralAfter,
		RevokedKeyAfter: w.config.RevokedKeyAfter, BatchSize: w.config.BatchSize,
	})
	if err != nil {
		if w.config.Observer != nil {
			w.config.Observer.ObserveRetention(0, true, now)
		}
		if !errors.Is(err, context.Canceled) {
			w.config.Logger.WarnContext(ctx, "retention pass failed", "error", err)
		}
		return
	}
	if w.config.Observer != nil {
		w.config.Observer.ObserveRetention(result.RedactedPayments, false, now)
	}
	if result.Total() > 0 {
		w.config.Logger.InfoContext(ctx, "retention pass complete",
			"expired_payments", result.ExpiredPayments,
			"redacted_payments", result.RedactedPayments,
			"email_tokens", result.EmailTokens,
			"wallet_challenges", result.WalletChallenges,
			"revoked_api_keys", result.RevokedAPIKeys)
	}
}
