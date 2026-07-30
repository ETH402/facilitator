package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/jackc/pgx/v5"
)

// MarkTxRecoveredBroadcast re-attaches a transaction hash to an ambiguous
// transaction — found on chain, found pending, or re-broadcast identically —
// and returns the payment to the broadcast pipeline. From here the
// confirmation worker observes it like any other broadcast; recovery never
// finalizes anything itself. broadcast_attempted_at is stamped so the stuck
// pipeline can fee-bump the transaction after the usual window — without it a
// recovered transaction was invisible to replaceStuck and could wedge pending
// forever, head-of-line blocking the signer's nonce sequence.
func (s *Store) MarkTxRecoveredBroadcast(ctx context.Context, paymentID, transactionID, txHash, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'broadcast', tx_hash = $3, broadcast_attempted_at = now(), updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'ambiguous'`, paymentID, transactionID, txHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("recover transaction %s: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateManualReview, settlement.StateBroadcast, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkAmbiguousRetry records a failed identical re-broadcast of an ambiguous
// transaction: the attempt counter rises and updated_at re-arms, which is what
// spaces the next re-sign out exponentially instead of every tick. The send
// outcome is unknown — the transaction may still have reached the network — so
// the row stays ambiguous for on-chain reconciliation. The database stamps both
// fields, keeping the backoff on the same clock that measures it.
func (s *Store) MarkAmbiguousRetry(ctx context.Context, paymentID, transactionID string) error {
	tag, err := s.Pool.Exec(ctx, `
UPDATE ethereum_transactions
SET ambiguous_attempts = ambiguous_attempts + 1, updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'ambiguous'`, paymentID, transactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record ambiguous retry for %s: %w", transactionID, ErrSettlementRace)
	}
	return nil
}

// MarkTxAmbiguousReplaced records the re-signed form of an ambiguous
// transaction as the replacement of its original. The sighash already proved
// the transaction identical — same nonce, gas, fees, and calldata — but a
// randomized-nonce signer (Cloud KMS) produces different bytes, hence a
// different hash. Recording it replacement-shaped keeps both signatures
// watched: the confirmation worker follows the fresh hash while
// observeReplacements resolves the payment if the network mines the original
// instead. One transaction: the ambiguous row leaves the active set as
// 'replaced', the re-signed row lands already broadcast with the linkage
// recorded, and the payment leaves manual review for 'replaced'.
func (s *Store) MarkTxAmbiguousReplaced(ctx context.Context, paymentID, oldTxID string, replacement settlement.Replacement, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The ambiguous row must leave the active set before the re-signed row
	// inserts: the partial unique index allows one active transaction per
	// payment. Its hash was never recorded, so it is derived from the raw
	// transaction hash — observeReplacements watches tx_hash, and the original
	// signature is exactly what must stay watched.
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'replaced', tx_hash = '0x' || raw_transaction_hash, updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'ambiguous'`, paymentID, oldTxID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("replace ambiguous transaction %s: %w", oldTxID, ErrSettlementRace)
	}
	var replacementID string
	err = tx.QueryRow(ctx, `
INSERT INTO ethereum_transactions(
  payment_id, tx_hash, signer_address, transaction_nonce, status,
  raw_transaction_hash, sighash, gas_limit, max_fee_per_gas, max_priority_fee_per_gas,
  broadcast_attempted_at)
SELECT payment_id, $3, signer_address, transaction_nonce, 'broadcast',
       $4, sighash, $5, $6, $7, now()
FROM ethereum_transactions WHERE id = $2 AND payment_id = $1
RETURNING id`,
		paymentID, oldTxID, replacement.TxHash, replacement.RawHash,
		replacement.GasLimit, replacement.MaxFee, replacement.PriorityFee).Scan(&replacementID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions SET replaced_by_id = $3 WHERE id = $2 AND payment_id = $1`,
		paymentID, oldTxID, replacementID); err != nil {
		return err
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateManualReview, settlement.StateReplaced, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxReplaced supersedes a stuck transaction with a fee-bumped replacement
// sharing its nonce. One transaction: the old row becomes 'replaced', the
// replacement lands already broadcast with the linkage recorded, and the
// payment moves to 'replaced'. The partial unique index tolerates this
// because only the replacement stays in the active set.
func (s *Store) MarkTxReplaced(ctx context.Context, paymentID, oldTxID string, replacement settlement.Replacement, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The old row must leave the active set before the replacement inserts:
	// the partial unique index allows one active transaction per payment.
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'replaced', updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'broadcast'`, paymentID, oldTxID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("replace transaction %s: %w", oldTxID, ErrSettlementRace)
	}
	var replacementID string
	err = tx.QueryRow(ctx, `
INSERT INTO ethereum_transactions(
  payment_id, tx_hash, signer_address, transaction_nonce, status,
  raw_transaction_hash, gas_limit, max_fee_per_gas, max_priority_fee_per_gas,
  broadcast_attempted_at)
SELECT payment_id, $3, signer_address, transaction_nonce, 'broadcast',
       $4, $5, $6, $7, now()
FROM ethereum_transactions WHERE id = $2 AND payment_id = $1
RETURNING id`,
		paymentID, oldTxID, replacement.TxHash, replacement.RawHash,
		replacement.GasLimit, replacement.MaxFee, replacement.PriorityFee).Scan(&replacementID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions SET replaced_by_id = $3 WHERE id = $2 AND payment_id = $1`,
		paymentID, oldTxID, replacementID); err != nil {
		return err
	}
	// A payment already in replaced (its replacement stalled and is being
	// re-bumped) stays put; only the first replacement audits a transition.
	var previous string
	err = tx.QueryRow(ctx, `
WITH old AS (SELECT state FROM payment_records WHERE id = $1)
UPDATE payment_records p
SET state = 'replaced', updated_at = now()
FROM old
WHERE p.id = $1 AND p.state IN ('broadcast', 'replaced')
RETURNING old.state`, paymentID).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("replace transaction %s: %w", oldTxID, ErrSettlementRace)
	}
	if err != nil {
		return err
	}
	if settlement.State(previous) == settlement.StateBroadcast {
		if err := recordTransition(ctx, tx, paymentID, settlement.StateBroadcast, settlement.StateReplaced, actor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// MarkReplacementLanded records that the network mined the *original*
// transaction rather than its replacement: the mined row becomes the truth
// (confirming on success, reverted on failure) and the never-minable
// replacement is dropped. The nonce is consumed either way; only the recorded
// history changes.
func (s *Store) MarkReplacementLanded(ctx context.Context, paymentID, minedTxID string, succeeded bool, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The never-minable replacement must leave the active set before the
	// original re-enters it: one active transaction per payment.
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'dropped', updated_at = now()
WHERE payment_id = $1 AND status = ANY($2)`, paymentID, activeTransactionStatuses)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("drop superseded replacement for %s: %w", minedTxID, ErrSettlementRace)
	}
	if succeeded {
		tag, err = tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'confirming', block_number = $3, block_hash = $4,
    first_seen_at = coalesce(first_seen_at, now()), updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'replaced'`, paymentID, minedTxID, blockNumber, blockHash)
	} else {
		tag, err = tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'reverted', gas_used = $3, effective_gas_price = $4, updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'replaced'`, paymentID, minedTxID, gasUsed, gasPrice)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("land replaced transaction %s: %w", minedTxID, ErrSettlementRace)
	}
	to := settlement.StateConfirming
	if !succeeded {
		to = settlement.StateReverted
	}
	// The payment is usually replaced, but ListReplacedPending deliberately
	// watches more states: a reorg can return the payment to broadcast before
	// the original lands, and a reverted original can contradict a confirming
	// sighting of the replacement (its block left the canonical chain). All
	// three states resolve to the same truth; guarding on replaced alone
	// rolled this whole update back every tick and wedged the payment.
	from := []settlement.State{settlement.StateReplaced, settlement.StateBroadcast, settlement.StateConfirming}
	if succeeded {
		// confirming → confirming is no edge: a succeeded original while the
		// payment is confirming means the confirmation worker already sighted
		// the replacement mined, which the same canonical chain contradicts —
		// leave that to MarkTxReorgedOut and retry on the next tick.
		from = from[:2]
	}
	if err := transitionPaymentIn(ctx, tx, paymentID, from, to, actor,
		`state = '`+string(to)+`'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTxReorgedOut returns a reorged transaction to broadcast: the block it
// was first seen in is no longer canonical, so the recorded block identity is
// cleared and the confirmation worker starts observing it again. first_seen
// survives — it is the history of the first sighting, wrong block or not.
func (s *Store) MarkTxReorgedOut(ctx context.Context, paymentID, transactionID, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'broadcast', block_number = NULL, block_hash = NULL, updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'confirming'`, paymentID, transactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reorg transaction %s out: %w", transactionID, ErrSettlementRace)
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateConfirming, settlement.StateBroadcast, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListReplacedPending returns the replaced transactions of payments waiting
// for confirmation, so recovery can check whether the original landed.
func (s *Store) ListReplacedPending(ctx context.Context) ([]settlement.TrackedTransaction, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT t.payment_id, t.id, t.tx_hash
FROM ethereum_transactions t
JOIN payment_records p ON p.id = t.payment_id
WHERE t.status = 'replaced' AND p.state IN ('replaced', 'confirming', 'broadcast')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracked(rows)
}

// ListDroppedBlockingGaps returns truly expired transactions whose unused nonce
// blocks a later in-flight nonce of the same signer. Application state enters
// expired at the safety margin, before valid_before; waiting for the actual
// timestamp is what guarantees re-broadcasting the authorization cannot move
// USDC. Simulation-failed payments remain terminal but may safely consume their
// facilitator nonce after the authorization itself has expired.
func (s *Store) ListDroppedBlockingGaps(ctx context.Context, signerAddress string, expiredBefore time.Time) ([]settlement.Work, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT p.id, p.payment_identity,
       p.payer_address, p.recipient_address, p.amount_atomic::text,
       p.authorization_nonce, p.valid_after, p.valid_before, p.payer_signature,
       t.id, t.transaction_nonce::text, p.state
FROM ethereum_transactions t
JOIN payment_records p ON p.id = t.payment_id
WHERE t.status = 'dropped'
  AND t.signer_address = $1
  AND t.raw_transaction_hash IS NULL
  AND p.state IN ('expired', 'failed')
  AND p.valid_before <= $3
  AND EXISTS (
    SELECT 1 FROM ethereum_transactions later
    WHERE later.signer_address = t.signer_address
      AND later.transaction_nonce > t.transaction_nonce
      AND later.status = ANY($2)
  )`, signerAddress, activeTransactionStatuses, expiredBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var works []settlement.Work
	for rows.Next() {
		var work settlement.Work
		var nonce, state string
		if err := rows.Scan(
			&work.PaymentID, &work.PaymentIdentity,
			&work.Authorization.From, &work.Authorization.To, &work.Authorization.Value,
			&work.Authorization.Nonce, &work.Authorization.ValidAfter, &work.Authorization.ValidBefore,
			&work.Authorization.Signature, &work.TransactionID, &nonce, &state); err != nil {
			return nil, err
		}
		parsed, err := parseNonce(nonce)
		if err != nil {
			return nil, err
		}
		work.Nonce = parsed
		work.State = settlement.State(state)
		work.TransactionStatus = "dropped"
		works = append(works, work)
	}
	return works, rows.Err()
}

// ListGapFillers returns prepared gap-fill broadcasts waiting on a receipt:
// their payments are expired or failed, so the confirmation worker never sees
// them. Replaced originals are included: the network may still mine either
// signature of the nonce, so both stay watched until one lands. The exact raw
// bytes allow a missing transaction to be re-broadcast identically.
func (s *Store) ListGapFillers(ctx context.Context) ([]settlement.TrackedTransaction, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT t.payment_id, t.id, t.tx_hash, t.status, t.raw_transaction
FROM ethereum_transactions t
JOIN payment_records p ON p.id = t.payment_id
WHERE t.status IN ('broadcast', 'replaced') AND p.state IN ('expired', 'failed')
  AND t.raw_transaction_hash IS NOT NULL AND t.raw_transaction IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracked []settlement.TrackedTransaction
	for rows.Next() {
		var item settlement.TrackedTransaction
		if err := rows.Scan(&item.PaymentID, &item.TransactionID, &item.TxHash, &item.Status, &item.RawTransaction); err != nil {
			return nil, err
		}
		tracked = append(tracked, item)
	}
	return tracked, rows.Err()
}

// ListStuckGapFillers returns broadcast gap fillers that have sat pending
// beyond the replacement window, so recovery can fee-bump them like any other
// stuck broadcast. The comparison runs against the database clock because
// broadcast_attempted_at is database-stamped; the application clock can be
// skewed. Returned rows carry everything a replacement re-sign needs.
func (s *Store) ListStuckGapFillers(ctx context.Context, signerAddress string, stuckFor time.Duration) ([]settlement.Work, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT p.id, p.payment_identity,
       p.payer_address, p.recipient_address, p.amount_atomic::text,
       p.authorization_nonce, p.valid_after, p.valid_before, p.payer_signature,
       t.id, t.transaction_nonce::text, coalesce(t.tx_hash, ''),
       t.signer_address, t.gas_limit::text, t.max_fee_per_gas::text,
       t.max_priority_fee_per_gas::text, p.state
FROM ethereum_transactions t
JOIN payment_records p ON p.id = t.payment_id
WHERE t.status = 'broadcast'
  AND t.signer_address = $1
  AND t.raw_transaction IS NOT NULL
  AND p.state IN ('expired', 'failed')
  AND t.broadcast_attempted_at < now() - make_interval(secs => $2)`,
		signerAddress, stuckFor.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var works []settlement.Work
	for rows.Next() {
		var work settlement.Work
		var nonce, gasLimit, state string
		if err := rows.Scan(
			&work.PaymentID, &work.PaymentIdentity,
			&work.Authorization.From, &work.Authorization.To, &work.Authorization.Value,
			&work.Authorization.Nonce, &work.Authorization.ValidAfter, &work.Authorization.ValidBefore,
			&work.Authorization.Signature, &work.TransactionID, &nonce, &work.TxHash,
			&work.SignerAddress, &gasLimit, &work.MaxFeePerGas,
			&work.MaxPriorityFeePerGas, &state); err != nil {
			return nil, err
		}
		parsed, err := parseNonce(nonce)
		if err != nil {
			return nil, err
		}
		work.Nonce = parsed
		limit, err := parseNonce(gasLimit)
		if err != nil {
			return nil, fmt.Errorf("gas limit: %w", err)
		}
		work.GasLimit = limit
		work.State = settlement.State(state)
		work.TransactionStatus = "broadcast"
		works = append(works, work)
	}
	return works, rows.Err()
}

// MarkGapFillerPrepared durably records the exact signed filler before any
// network call. A crash or ambiguous send can then be retried byte-for-byte.
func (s *Store) MarkGapFillerPrepared(ctx context.Context, transactionID, rawHash, txHash string, raw []byte, gasLimit uint64, maxFee, priorityFee string) error {
	tag, err := s.Pool.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'broadcast', raw_transaction_hash = $2, tx_hash = $3,
    raw_transaction = $4, gas_limit = $5, max_fee_per_gas = $6, max_priority_fee_per_gas = $7,
    broadcast_attempted_at = now(), updated_at = now()
WHERE id = $1 AND status = 'dropped'`, transactionID, rawHash, txHash, raw, gasLimit, maxFee, priorityFee)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("broadcast gap filler %s: %w", transactionID, ErrSettlementRace)
	}
	return nil
}

// MarkGapFillerReplaced supersedes a stuck gap filler with a fee-bumped
// replacement sharing its nonce, mirroring MarkTxReplaced for a payment that
// is already terminal: the payment itself does not transition, only the
// transaction rows move. The replacement carries its exact signed bytes like
// the original preparation did, so observeGapFillers can re-broadcast it after
// an ambiguous send. The old row keeps its raw bytes as 'replaced' — the
// network may still mine either signature of the nonce, so both stay watched.
func (s *Store) MarkGapFillerReplaced(ctx context.Context, paymentID, oldTxID string, replacement settlement.Replacement, raw []byte) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The old row must leave the active set before the replacement inserts:
	// the partial unique index allows one active transaction per payment.
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'replaced', updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status = 'broadcast' AND raw_transaction IS NOT NULL`,
		paymentID, oldTxID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("replace gap filler %s: %w", oldTxID, ErrSettlementRace)
	}
	var replacementID string
	err = tx.QueryRow(ctx, `
INSERT INTO ethereum_transactions(
  payment_id, tx_hash, signer_address, transaction_nonce, status,
  raw_transaction_hash, raw_transaction, gas_limit, max_fee_per_gas, max_priority_fee_per_gas,
  broadcast_attempted_at)
SELECT payment_id, $3, signer_address, transaction_nonce, 'broadcast',
       $4, $5, $6, $7, $8, now()
FROM ethereum_transactions WHERE id = $2 AND payment_id = $1
RETURNING id`,
		paymentID, oldTxID, replacement.TxHash, replacement.RawHash, raw,
		replacement.GasLimit, replacement.MaxFee, replacement.PriorityFee).Scan(&replacementID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions SET replaced_by_id = $3 WHERE id = $2 AND payment_id = $1`,
		paymentID, oldTxID, replacementID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkGapFillerResolved retires a gap filler from its receipt. It is expected
// to revert — the authorization expired — and the revert still consumes the
// nonce, which is the entire point of the fill. Either signature of the nonce
// may be the one that landed (the active broadcast or a replaced original), so
// both statuses resolve, and the sibling that can now never mine is dropped to
// end its watch.
func (s *Store) MarkGapFillerResolved(ctx context.Context, transactionID string, gasUsed uint64, gasPrice string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'reverted', gas_used = $2, effective_gas_price = $3, updated_at = now()
WHERE id = $1 AND status IN ('broadcast', 'replaced')`, transactionID, gasUsed, gasPrice)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("resolve gap filler %s: %w", transactionID, ErrSettlementRace)
	}
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'dropped', updated_at = now()
WHERE payment_id = (SELECT payment_id FROM ethereum_transactions WHERE id = $1)
  AND id <> $1 AND status IN ('broadcast', 'replaced')`, transactionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanTracked(rows pgx.Rows) ([]settlement.TrackedTransaction, error) {
	var tracked []settlement.TrackedTransaction
	for rows.Next() {
		var t settlement.TrackedTransaction
		if err := rows.Scan(&t.PaymentID, &t.TransactionID, &t.TxHash); err != nil {
			return nil, err
		}
		tracked = append(tracked, t)
	}
	return tracked, rows.Err()
}

// MarkGapFillerSucceeded records the anomaly of a nonce-gap filler that the
// chain accepted.
//
// A filler re-broadcasts an authorization the facilitator judged expired, so it
// is expected to revert and consume the nonce. Success means the judgement was
// wrong and USDC actually moved: the payment is recorded `expired` while the
// ledger says it settled. The receipt is persisted and the payment escalated to
// manual_review — never to confirmed, because recovery does not finalize payments
// (ADR-0004 decision 4) and only a human can reconcile the divergence.
//
// It also ends the observation loop: ListGapFillers selects on the payment being
// `expired`, so escalating it stops the worker re-reporting the same anomaly on
// every tick.
func (s *Store) MarkGapFillerSucceeded(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'confirming', block_number = $3, block_hash = $4,
    gas_used = $5, effective_gas_price = $6, first_seen_at = coalesce(first_seen_at, now()),
    updated_at = now()
WHERE id = $2 AND payment_id = $1 AND status IN ('broadcast', 'replaced')`,
		paymentID, transactionID, blockNumber, blockHash, gasUsed, gasPrice)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record succeeded gap filler %s: %w", transactionID, ErrSettlementRace)
	}
	// The nonce is consumed, so the sibling signature can never mine; drop it
	// rather than leaving it watched forever.
	if _, err := tx.Exec(ctx, `
UPDATE ethereum_transactions
SET status = 'dropped', updated_at = now()
WHERE payment_id = $1 AND id <> $2 AND status IN ('broadcast', 'replaced')`,
		paymentID, transactionID); err != nil {
		return err
	}
	if err := transitionPayment(ctx, tx, paymentID, settlement.StateExpired, settlement.StateManualReview, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
