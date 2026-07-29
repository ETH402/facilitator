package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type RetentionRequest struct {
	Now             time.Time
	PaymentAfter    time.Duration
	EphemeralAfter  time.Duration
	RevokedKeyAfter time.Duration
	BatchSize       int
}

func (r RetentionRequest) Validate() error {
	if r.Now.IsZero() {
		return errors.New("retention clock is required")
	}
	if r.PaymentAfter <= 0 || r.EphemeralAfter <= 0 || r.RevokedKeyAfter <= 0 {
		return errors.New("retention periods must be positive")
	}
	if r.BatchSize < 1 || r.BatchSize > 10_000 {
		return errors.New("retention batch size must be between 1 and 10000")
	}
	return nil
}

type RetentionResult struct {
	ExpiredPayments  int64
	RedactedPayments int64
	EmailTokens      int64
	WalletChallenges int64
	RevokedAPIKeys   int64
}

func (r RetentionResult) Total() int64 {
	return r.ExpiredPayments + r.RedactedPayments + r.EmailTokens +
		r.WalletChallenges + r.RevokedAPIKeys
}

// ApplyRetention expires stale verified payments and removes high-sensitivity
// data in bounded batches. It never redacts an authorization that recovery may
// still need to consume a signer nonce: failed/expired payments are eligible
// only when they have no transaction or every transaction is already reverted.
//
// Payment identity, amount, state, and public transaction hashes remain. That
// preserves lifetime stats and lets a duplicate /settle recover the original
// hash without retaining the payer authorization.
func (s *Store) ApplyRetention(ctx context.Context, request RetentionRequest) (RetentionResult, error) {
	if err := request.Validate(); err != nil {
		return RetentionResult{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result RetentionResult
	expireTag, err := tx.Exec(ctx, `
WITH candidates AS (
    SELECT id
    FROM payment_records
    WHERE state = 'verified' AND valid_before < $1
    ORDER BY valid_before, id
    FOR UPDATE SKIP LOCKED
    LIMIT $2
), expired AS (
    UPDATE payment_records p
    SET state = 'expired', updated_at = $1
    FROM candidates c
    WHERE p.id = c.id AND p.state = 'verified'
    RETURNING p.id
)
INSERT INTO payment_transitions(payment_id,from_state,to_state,reason_code,actor_type,metadata)
SELECT id,'verified','expired','authorization_expired','system','{}'::jsonb
FROM expired`, request.Now, request.BatchSize)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("expire stale verified payments: %w", err)
	}
	result.ExpiredPayments = expireTag.RowsAffected()

	redactTag, err := tx.Exec(ctx, `
WITH candidates AS (
    SELECT p.id
    FROM payment_records p
    WHERE p.redacted_at IS NULL
      AND p.state IN ('confirmed','reverted','failed','expired','verification_failed')
      AND p.valid_before < $1
      AND coalesce(p.confirmed_at, p.updated_at) < $1
      AND (
          p.state IN ('confirmed','reverted','verification_failed')
          OR NOT EXISTS (
              SELECT 1
              FROM ethereum_transactions t
              WHERE t.payment_id = p.id AND t.status <> 'reverted'
          )
      )
    ORDER BY coalesce(p.confirmed_at, p.updated_at), p.id
    FOR UPDATE SKIP LOCKED
    LIMIT $2
), cleared_transactions AS (
    UPDATE ethereum_transactions t
    SET raw_transaction = NULL, updated_at = $3
    FROM candidates c
    WHERE t.payment_id = c.id AND t.raw_transaction IS NOT NULL
    RETURNING t.id
)
UPDATE payment_records p
SET merchant_id = NULL,
    payer_address = NULL,
    recipient_address = NULL,
    authorization_nonce = NULL,
    valid_after = NULL,
    valid_before = NULL,
    payload_hash = NULL,
    payer_signature = NULL,
    claimed_by = NULL,
    claimed_until = NULL,
    redacted_at = $3,
    updated_at = $3
FROM candidates c
WHERE p.id = c.id`, request.Now.Add(-request.PaymentAfter), request.BatchSize, request.Now)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("redact terminal payments: %w", err)
	}
	result.RedactedPayments = redactTag.RowsAffected()

	result.EmailTokens, err = deleteBatch(ctx, tx, `
DELETE FROM email_verification_tokens
WHERE ctid IN (
    SELECT ctid FROM email_verification_tokens
    WHERE expires_at < $1
    ORDER BY expires_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)`, request.Now.Add(-request.EphemeralAfter), request.BatchSize)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("prune email tokens: %w", err)
	}
	result.WalletChallenges, err = deleteBatch(ctx, tx, `
DELETE FROM wallet_verification_challenges challenge
WHERE challenge.ctid IN (
    SELECT candidate.ctid
    FROM wallet_verification_challenges candidate
    WHERE candidate.expires_at < $1
      AND NOT EXISTS (
          SELECT 1 FROM recipient_address_history history
          WHERE history.wallet_challenge_id = candidate.id
      )
    ORDER BY candidate.expires_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)`, request.Now.Add(-request.EphemeralAfter), request.BatchSize)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("prune wallet challenges: %w", err)
	}
	result.RevokedAPIKeys, err = deleteBatch(ctx, tx, `
DELETE FROM api_keys
WHERE ctid IN (
    SELECT ctid FROM api_keys
    WHERE revoked_at < $1
    ORDER BY revoked_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)`, request.Now.Add(-request.RevokedKeyAfter), request.BatchSize)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("prune revoked API keys: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RetentionResult{}, err
	}
	return result, nil
}

func deleteBatch(ctx context.Context, tx pgx.Tx, query string, cutoff time.Time, batchSize int) (int64, error) {
	tag, err := tx.Exec(ctx, query, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
