package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/verification"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// globalSettlementLockKey serialises the facilitator-wide settlement ceiling. It
// is a fixed key rather than a derived one because it must be the same value for
// every merchant — that is the entire point. Chosen to be nowhere near the range
// authorizationLockKey produces, which is a full 63-bit hash.
const globalSettlementLockKey int64 = 0x455448343032_01

// authorizationLockKey derives the advisory-lock key that serialises writers for
// one buyer authorization. Collisions only cost extra serialisation, never
// correctness.
func authorizationLockKey(payer, nonce string) int64 {
	digest := sha256.Sum256([]byte(strings.ToLower(payer) + "|" + strings.ToLower(nonce)))
	// Masking to 63 bits keeps the conversion provably in range; any non-negative
	// int64 is an equally valid advisory-lock key.
	return int64(binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64)
}

// retryableWrite reports whether PostgreSQL aborted the transaction for a reason
// that is transient by definition, so retrying the whole transaction is correct.
func retryableWrite(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	// 40P01 deadlock_detected, 40001 serialization_failure.
	return postgresError.Code == "40P01" || postgresError.Code == "40001"
}

// RecordVerification persists a verification attempt, and the payment identity
// when the attempt succeeded.
//
// Concurrent duplicates converge rather than failing: two simultaneous verify
// requests for the same authorization must both observe the same payment row.
// The retry is defence in depth behind the advisory lock taken below, because a
// transient abort would otherwise surface to the caller as a 503.
func (s *Store) RecordVerification(ctx context.Context, attempt verification.Attempt) error {
	const maxAttempts = 3
	var err error
	for range maxAttempts {
		err = s.recordVerification(ctx, attempt)
		if err == nil || !retryableWrite(err) {
			return err
		}
		slog.WarnContext(ctx, "retrying verification record after transient database abort", "error", err)
	}
	return err
}

func (s *Store) recordVerification(ctx context.Context, attempt verification.Attempt) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var paymentID any
	if attempt.Result == "verified" {
		if attempt.Payment == nil {
			return errors.New("verified attempt requires payment data")
		}
		// Serialise every writer for this authorization before inserting. The
		// duplicate row violates payment_identity and the
		// (network, asset, payer, nonce) uniqueness simultaneously; ON CONFLICT
		// resolves only its arbiter index speculatively while the other index takes
		// an ordinary uniqueness wait, and two concurrent inserts can then deadlock.
		// Settlement must take this same lock for the authorizations it writes.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`,
			authorizationLockKey(attempt.Payment.Payer, attempt.Payment.Nonce)); err != nil {
			return err
		}
		const insertPayment = `
INSERT INTO payment_records (
  payment_identity, merchant_id, x402_version, scheme, network, asset,
  payer_address, recipient_address, amount_atomic, authorization_nonce,
  valid_after, valid_before, payload_hash, verification_status, state
)
VALUES (
  $1,
  -- Recipient addresses are not unique across merchants, so attribution is
  -- pinned to the oldest active claimant to stay stable across retries.
  (SELECT id FROM merchants WHERE recipient_address = $2 AND status = 'active'
     ORDER BY created_at, id LIMIT 1),
  2, 'exact', 'eip155:1', $3, $4, $2, $5, $6, $7, $8, $9, 'pending', 'received'
)
ON CONFLICT (payment_identity) DO NOTHING
RETURNING id`
		var id string
		err = tx.QueryRow(ctx, insertPayment,
			attempt.Payment.Identity, attempt.Payment.Recipient, attempt.Payment.Asset,
			attempt.Payment.Payer, attempt.Payment.Amount, attempt.Payment.Nonce,
			attempt.Payment.ValidAfter, attempt.Payment.ValidBefore, attempt.Payment.PayloadHash,
		).Scan(&id)
		inserted := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx,
				`SELECT id FROM payment_records WHERE payment_identity = $1`,
				attempt.Payment.Identity,
			).Scan(&id)
		}
		if err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "23505" {
				_ = tx.Rollback(ctx)
				if _, recordErr := s.Pool.Exec(ctx, `
INSERT INTO verification_attempts(payment_identity,result,reason_code)
VALUES ($1,'failed',$2)`,
					attempt.PaymentIdentity, verification.ReasonNonceAlreadyUsed,
				); recordErr != nil {
					return recordErr
				}
				return verification.ErrAuthorizationConflict
			}
			return err
		}
		paymentID = id
		if inserted {
			if _, err := tx.Exec(ctx, `
UPDATE payment_records
SET verification_status = 'verified', state = 'verified', updated_at = now()
WHERE id = $1 AND state = 'received'`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO payment_transitions(payment_id,from_state,to_state,actor_type,metadata)
VALUES ($1,'received','verified','http','{}'::jsonb)`, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO verification_attempts(payment_id,payment_identity,result,reason_code)
VALUES ($1,$2,$3,NULLIF($4,''))`,
		paymentID, nullString(attempt.PaymentIdentity), attempt.Result, attempt.ReasonCode,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
