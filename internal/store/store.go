package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ETH402/facilitator/internal/stats"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.Pool == nil {
		return errors.New("database unavailable")
	}
	return s.Pool.Ping(ctx)
}

func (s *Store) AggregateStats(ctx context.Context) (stats.Aggregate, error) {
	const query = `
SELECT
  (SELECT count(*) FROM merchants),
  (SELECT count(*) FROM merchants WHERE status = 'active'),
  (SELECT count(*) FROM verification_attempts),
  (SELECT count(*) FROM verification_attempts WHERE result = 'verified'),
  (SELECT count(*) FROM verification_attempts WHERE result = 'failed'),
  (SELECT count(*) FROM settlement_attempts),
  (SELECT count(*) FROM payment_records WHERE state = 'confirmed'),
  (SELECT count(*) FROM payment_records WHERE state IN ('failed','reverted')),
  (SELECT coalesce(sum(amount_atomic), 0)::text FROM payment_records WHERE state = 'confirmed'),
  (SELECT count(*) FROM payment_records WHERE state = 'confirmed' AND confirmed_at >= now() - interval '24 hours'),
  (SELECT coalesce(sum(amount_atomic), 0)::text FROM payment_records WHERE state = 'confirmed' AND confirmed_at >= now() - interval '24 hours'),
  (SELECT coalesce(max(block_number), 0) FROM ethereum_transactions WHERE status = 'confirmed')`
	var a stats.Aggregate
	err := s.Pool.QueryRow(ctx, query).Scan(
		&a.RegisteredMerchants, &a.VerifiedMerchants,
		&a.TotalVerifications, &a.SuccessfulVerifications, &a.FailedVerifications,
		&a.TotalSettlementRequests, &a.ConfirmedSettlements, &a.FailedSettlements,
		&a.TotalPaymentVolumeAtomic, &a.SettlementsLast24h, &a.VolumeLast24hAtomic,
		&a.LastConfirmedBlock,
	)
	if err != nil {
		slog.ErrorContext(ctx, "aggregate stats query failed", "error", err)
		return stats.Aggregate{}, err
	}
	a.LatestIndexedBlock = a.LastConfirmedBlock
	return a, nil
}
