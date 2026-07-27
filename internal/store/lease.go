package store

import (
	"context"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
)

// ClaimPayments leases up to Limit payments in the requested states and returns
// them. A payment is claimable when it holds no lease or when its lease has
// lapsed, which is what lets work survive a worker that dies mid-task.
//
// FOR UPDATE SKIP LOCKED appears inside the selection subquery only, so two
// workers claiming simultaneously step past each other instead of blocking. That
// is not the mechanism ADR-0004 rejected: the rejected design held a transaction
// open across Ethereum RPC. Here the transaction ends with this statement and the
// lease is what spans the RPC work.
func (s *Store) ClaimPayments(ctx context.Context, request settlement.ClaimRequest) ([]settlement.Lease, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	until := request.Now.Add(request.Duration)
	// updated_at is bumped so a claimed payment sorts to the back of the queue and
	// a repeatedly-failing one cannot starve the rest.
	rows, err := s.Pool.Query(ctx, `
UPDATE payment_records
SET claimed_by = $1, claimed_until = $2, updated_at = now()
WHERE id IN (
  SELECT id FROM payment_records
  WHERE state = ANY($3)
    AND (claimed_by IS NULL OR claimed_until <= $4)
  ORDER BY updated_at
  LIMIT $5
  FOR UPDATE SKIP LOCKED
)
  -- Re-checked under the row lock rather than trusting the subquery's snapshot,
  -- so a live lease can never be overwritten even if the read raced a claim.
  AND (claimed_by IS NULL OR claimed_until <= $4)
RETURNING id, payment_identity, state, claimed_until`,
		request.Worker, until, request.StateStrings(), request.Now, request.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leases []settlement.Lease
	for rows.Next() {
		var lease settlement.Lease
		var state string
		if err := rows.Scan(&lease.PaymentID, &lease.PaymentIdentity, &state, &lease.Until); err != nil {
			return nil, err
		}
		lease.State = settlement.State(state)
		lease.Worker = request.Worker
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

// RenewLease extends a lease the worker still holds.
//
// The claimed_until guard is essential: once a lease lapses another worker may
// already own the payment, so extending it would give two workers a live claim on
// the same settlement. An expired lease must be re-claimed, never renewed.
func (s *Store) RenewLease(ctx context.Context, paymentID, worker string, now time.Time, duration time.Duration) (time.Time, error) {
	if worker == "" || duration <= 0 || now.IsZero() {
		return time.Time{}, settlement.ErrLeaseLost
	}
	until := now.Add(duration)
	tag, err := s.Pool.Exec(ctx, `
UPDATE payment_records
SET claimed_until = $3
WHERE id = $1 AND claimed_by = $2 AND claimed_until > $4`, paymentID, worker, until, now)
	if err != nil {
		return time.Time{}, err
	}
	if tag.RowsAffected() != 1 {
		return time.Time{}, settlement.ErrLeaseLost
	}
	return until, nil
}

// ReleaseLease drops a lease the worker holds so the payment can be picked up
// again without waiting for expiry.
//
// It reports ErrLeaseLost when the worker no longer holds the lease, which is how
// a worker learns that it overran and its work may have been duplicated.
func (s *Store) ReleaseLease(ctx context.Context, paymentID, worker string) error {
	if worker == "" {
		return settlement.ErrLeaseLost
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE payment_records
SET claimed_by = NULL, claimed_until = NULL, updated_at = now()
WHERE id = $1 AND claimed_by = $2`, paymentID, worker)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return settlement.ErrLeaseLost
	}
	return nil
}
