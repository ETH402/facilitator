package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/jackc/pgx/v5"
)

// ErrSettlementRace means a state transition found the row in an unexpected
// state — another actor moved it first, or the durable record is corrupt. The
// guards below make losing a race loud instead of double-recording a
// broadcast, which is what keeps at-most-one in-flight transaction per payment
// enforceable above the partial unique index.
var ErrSettlementRace = errors.New("settlement state changed underneath the actor")

// ClaimPayment takes a lease on one specific payment. The inline /settle path
// uses it to run the broadcast pipeline without competing with a worker that
// already holds the lease; ErrLeaseUnavailable means exactly that.
func (s *Store) ClaimPayment(ctx context.Context, paymentID, worker string, duration time.Duration, now time.Time) (settlement.Lease, error) {
	if paymentID == "" || worker == "" || duration <= 0 || now.IsZero() {
		return settlement.Lease{}, errors.New("claim requires a payment, worker, positive duration, and current time")
	}
	var lease settlement.Lease
	var state string
	err := s.Pool.QueryRow(ctx, `
UPDATE payment_records
SET claimed_by = $2, claimed_until = $3, updated_at = now()
WHERE id = $1 AND (claimed_by IS NULL OR claimed_until <= $4)
RETURNING id, payment_identity, state, claimed_until`,
		paymentID, worker, now.Add(duration), now).
		Scan(&lease.PaymentID, &lease.PaymentIdentity, &state, &lease.Until)
	if errors.Is(err, pgx.ErrNoRows) {
		return settlement.Lease{}, settlement.ErrLeaseUnavailable
	}
	if err != nil {
		return settlement.Lease{}, err
	}
	lease.State = settlement.State(state)
	lease.Worker = worker
	return lease, nil
}

// LoadSettlementWork reads the payment calldata fields and the active
// transaction row for a leased payment. The partial unique index guarantees at
// most one active transaction, so the join returns exactly one row while
// settlement is in flight.
func (s *Store) LoadSettlementWork(ctx context.Context, paymentID string) (settlement.Work, error) {
	var work settlement.Work
	var state, signature, nonce string
	var rawHash, maxFee, priorityFee, gasLimitText *string
	var broadcastAttemptedAt *time.Time
	err := s.Pool.QueryRow(ctx, `
SELECT p.id, p.payment_identity, p.state,
       p.payer_address, p.recipient_address, p.amount_atomic::text,
       p.authorization_nonce, p.valid_after, p.valid_before, p.payer_signature,
       t.id, t.status, t.transaction_nonce::text, coalesce(t.tx_hash, ''),
       t.signer_address, t.raw_transaction_hash,
       t.gas_limit::text, t.max_fee_per_gas::text, t.max_priority_fee_per_gas::text,
       t.broadcast_attempted_at, t.updated_at
FROM payment_records p
JOIN ethereum_transactions t ON t.payment_id = p.id AND t.status = ANY($2)
WHERE p.id = $1`, paymentID, activeTransactionStatuses).Scan(
		&work.PaymentID, &work.PaymentIdentity, &state,
		&work.Authorization.From, &work.Authorization.To, &work.Authorization.Value,
		&work.Authorization.Nonce, &work.Authorization.ValidAfter, &work.Authorization.ValidBefore,
		&signature, &work.TransactionID, &work.TransactionStatus, &nonce, &work.TxHash,
		&work.SignerAddress, &rawHash,
		&gasLimitText, &maxFee, &priorityFee,
		&broadcastAttemptedAt, &work.TransactionUpdatedAt)
	if err != nil {
		return settlement.Work{}, err
	}
	work.State = settlement.State(state)
	work.Authorization.Signature = signature
	parsed, err := parseNonce(nonce)
	if err != nil {
		return settlement.Work{}, err
	}
	work.Nonce = parsed
	if rawHash != nil {
		work.RawHash = *rawHash
	}
	if maxFee != nil {
		work.MaxFeePerGas = *maxFee
	}
	if priorityFee != nil {
		work.MaxPriorityFeePerGas = *priorityFee
	}
	if broadcastAttemptedAt != nil {
		work.BroadcastAttemptedAt = *broadcastAttemptedAt
	}
	if gasLimitText != nil {
		gasLimit, err := parseNonce(*gasLimitText)
		if err != nil {
			return settlement.Work{}, fmt.Errorf("gas limit: %w", err)
		}
		work.GasLimit = gasLimit
	}
	return work, nil
}

// MarkTxSigned records that the intent's transaction was signed: the raw
// transaction hash is the recovery handle for the ambiguous-broadcast case
// (ADR-0004 decision 4), so it is persisted before any broadcast attempt. The
// gas and fee values are stored with it because recovery may only ever
// re-sign the *identical* transaction — never a fresh nonce, never different
// terms.
func (s *Store) MarkTxSigned(ctx context.Context, transactionID, rawHash string, gasLimit uint64, maxFee, priorityFee string) error {
	tag, err := s.Pool.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'broadcasting', raw_transaction_hash = $2,
    gas_limit = $3, max_fee_per_gas = $4, max_priority_fee_per_gas = $5,
    updated_at = now()
WHERE id = $1 AND status = 'intent'`, transactionID, rawHash, gasLimit, maxFee, priorityFee)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark transaction %s signed: %w", transactionID, ErrSettlementRace)
	}
	return nil
}

// MarkTxBroadcast records a successful broadcast: the transaction hash moves
// the transaction to broadcast and the payment to broadcast, atomically, with
// the transition audited. A second caller — inline settle racing a worker —
// loses on the row guards instead of recording a second hash.
func (s *Store) MarkTxBroadcast(ctx context.Context, paymentID, transactionID, txHash, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'broadcast', tx_hash = $3, broadcast_attempted_at = now(), updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'broadcasting'`, paymentID, transactionID, txHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark transaction %s broadcast: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateBroadcasting, settlement.StateBroadcast, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxAmbiguous moves a signed-but-unrecorded transaction to ambiguous and
// the payment to manual_review. The transaction may have reached the network;
// only on-chain reconciliation (recovery) may resolve it — never a re-sign,
// never a fresh nonce (ADR-0004 decision 4).
func (s *Store) MarkTxAmbiguous(ctx context.Context, paymentID, transactionID, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'ambiguous', updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'broadcasting'`, paymentID, transactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark transaction %s ambiguous: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateBroadcasting, settlement.StateManualReview, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxConfirming records the first sighting of a mined-but-not-final
// transaction. It is a no-op when the payment is already confirming, so a
// confirmation worker can call it on every tick without duplicating
// transitions; the transaction row still updates so the recorded block stays
// canonical when a reorg moves the transaction to a different block.
func (s *Store) MarkTxConfirming(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'confirming', block_number = $3, block_hash = $4,
    first_seen_at = coalesce(first_seen_at, now()), updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status IN ('broadcast', 'confirming')`,
		paymentID, transactionID, blockNumber, blockHash); err != nil {
		return err
	}
	var previous string
	err = tx.QueryRow(ctx, `
WITH old AS (SELECT state FROM payment_records WHERE id = $1)
UPDATE payment_records p
SET state = 'confirming', updated_at = now()
FROM old
WHERE p.id = $1 AND p.state IN ('broadcast', 'replaced')
RETURNING old.state`, paymentID).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already confirming (or moved elsewhere): sighting recorded, nothing to audit.
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err := recordTransition(ctx, tx, paymentID, settlement.State(previous), settlement.StateConfirming, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxConfirmed finalizes a transaction at the required confirmation depth.
// confirmed is terminal per ADR-0004 decision 5: there is deliberately no edge
// out of it, so finality is a one-way record.
func (s *Store) MarkTxConfirmed(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'confirmed', block_number = $3, block_hash = $4,
    gas_used = $5, effective_gas_price = $6, confirmed_at = now(), updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status IN ('broadcast', 'confirming')`,
		paymentID, transactionID, blockNumber, blockHash, gasUsed, gasPrice)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark transaction %s confirmed: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPaymentIn(ctx, tx, paymentID,
		[]settlement.State{settlement.StateBroadcast, settlement.StateConfirming, settlement.StateReplaced},
		settlement.StateConfirmed, actor, `state = 'confirmed', confirmed_at = now()`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxReverted records a mined transaction the chain rejected. The operator
// paid the gas; the payment cannot settle from this authorization anymore
// because its EIP-3009 nonce may now be consumed on chain.
func (s *Store) MarkTxReverted(ctx context.Context, paymentID, transactionID string, gasUsed uint64, gasPrice, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'reverted', gas_used = $3, effective_gas_price = $4, updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status IN ('broadcast', 'confirming')`,
		paymentID, transactionID, gasUsed, gasPrice)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark transaction %s reverted: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPaymentIn(ctx, tx, paymentID,
		[]settlement.State{settlement.StateBroadcast, settlement.StateConfirming, settlement.StateReplaced},
		settlement.StateReverted, actor, `state = 'reverted'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// transitionPayment moves a payment between exactly two states and audits the
// transition, validating the edge against the state machine first.
func transitionPayment(ctx context.Context, tx pgx.Tx, paymentID string, from, to settlement.State, actor string) error {
	if err := settlement.ValidateTransition(from, to); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE payment_records SET state = $3, updated_at = now()
WHERE id = $1 AND state = $2`, paymentID, string(from), string(to))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("payment %s transition %s -> %s: %w", paymentID, from, to, ErrSettlementRace)
	}
	return recordTransition(ctx, tx, paymentID, from, to, actor)
}

// transitionPaymentIn is transitionPayment for a payment in any of several
// source states. setAssignments carries the terminal columns (it must include
// the new state) so one statement moves the row and a CTE captures the
// pre-update state for the audit record — RETURNING alone would observe the
// post-update value.
func transitionPaymentIn(ctx context.Context, tx pgx.Tx, paymentID string, from []settlement.State, to settlement.State, actor, setAssignments string) error {
	states := make([]string, 0, len(from))
	for _, state := range from {
		if err := settlement.ValidateTransition(state, to); err != nil {
			return err
		}
		states = append(states, string(state))
	}
	var previous string
	err := tx.QueryRow(ctx, `
WITH old AS (SELECT state FROM payment_records WHERE id = $1)
UPDATE payment_records p
SET `+setAssignments+`, updated_at = now()
FROM old
WHERE p.id = $1 AND p.state = ANY($2)
RETURNING old.state`, paymentID, states).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("payment %s transition to %s: %w", paymentID, to, ErrSettlementRace)
	}
	if err != nil {
		return err
	}
	return recordTransition(ctx, tx, paymentID, settlement.State(previous), to, actor)
}

func recordTransition(ctx context.Context, tx pgx.Tx, paymentID string, from, to settlement.State, actor string) error {
	_, err := tx.Exec(ctx, `
INSERT INTO payment_transitions(payment_id,from_state,to_state,actor_type,metadata)
VALUES ($1,$2,$3,$4,'{}'::jsonb)`, paymentID, string(from), string(to), actor)
	return err
}

// MarkIntentExpired retires a settlement intent whose authorization expired
// before broadcast. The transaction was never sent, so it is 'dropped' rather
// than reverted: no gas was spent and the nonce it owned is never reused
// (ADR-0004 decision 1 forbids reallocating it; the gap is reconciled during
// recovery, never by handing the nonce to another payment).
// MarkIntentUnsettleable retires an intent that simulation proved cannot succeed.
//
// The payment becomes `failed` and the transaction `dropped`, mirroring expiry:
// no gas was spent and none will be. The nonce stays allocated and unused, which
// is a gap the recovery worker fills only if a later nonce is actually blocked —
// so the revert is paid for once, if ever, instead of always.
func (s *Store) MarkIntentUnsettleable(ctx context.Context, paymentID, transactionID, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'dropped', updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'intent'`, paymentID, transactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("drop unsettleable transaction %s: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateBroadcasting, settlement.StateFailed, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MarkIntentExpired(ctx context.Context, paymentID, transactionID, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'dropped', updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'intent'`, paymentID, transactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("drop expired transaction %s: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateBroadcasting, settlement.StateExpired, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
