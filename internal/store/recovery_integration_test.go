//go:build integration

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
)

func paymentState(t *testing.T, store *Store, paymentID string) string {
	t.Helper()
	var state string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT state FROM payment_records WHERE id = $1`, paymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func txRow(t *testing.T, store *Store, transactionID string) (status, txHash string) {
	t.Helper()
	var hash *string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT status, tx_hash FROM ethereum_transactions WHERE id = $1`, transactionID).
		Scan(&status, &hash); err != nil {
		t.Fatal(err)
	}
	if hash != nil {
		txHash = *hash
	}
	return status, txHash
}

// seedReplaced drives a payment to broadcast and replaces its transaction,
// returning the payment ID, the replaced original's ID, and the active
// replacement's work.
func seedReplaced(t *testing.T, store *Store, identity string) (string, settlement.Work, settlement.Work) {
	t.Helper()
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, identity)
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, "0x"+strings.Repeat("f", 64), "worker"); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	replacement := settlement.Replacement{
		Nonce: work.Nonce, TxHash: "0x" + strings.Repeat("d", 64), RawHash: strings.Repeat("d", 64),
		GasLimit: 120000, MaxFee: "40000000000", PriorityFee: "3000000000", SignerAddress: intentSigner,
	}
	if err := store.MarkTxReplaced(ctx, paymentID, work.TransactionID, replacement, "worker"); err != nil {
		t.Fatalf("mark replaced: %v", err)
	}
	active, err := store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatalf("load replacement work: %v", err)
	}
	return paymentID, work, active
}

func TestRecoveredBroadcastReturnsToPipeline(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("1a", 34))
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxAmbiguous(ctx, paymentID, work.TransactionID, "worker"); err != nil {
		t.Fatalf("mark ambiguous: %v", err)
	}
	txHash := "0x" + strings.Repeat("e", 64)
	if err := store.MarkTxRecoveredBroadcast(ctx, paymentID, work.TransactionID, txHash, "worker"); err != nil {
		t.Fatalf("mark recovered: %v", err)
	}
	if state := paymentState(t, store, paymentID); state != "broadcast" {
		t.Fatalf("state = %s, want broadcast", state)
	}
	status, hash := txRow(t, store, work.TransactionID)
	if status != "broadcast" || hash != txHash {
		t.Fatalf("status=%s hash=%s", status, hash)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM payment_transitions
WHERE payment_id = $1 AND from_state = 'manual_review' AND to_state = 'broadcast'`, paymentID).
		Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("recovery transitions = %d, want 1", transitions)
	}
	if err := store.MarkTxRecoveredBroadcast(ctx, paymentID, work.TransactionID, txHash, "worker"); !errors.Is(err, ErrSettlementRace) {
		t.Fatalf("second recovery = %v, want ErrSettlementRace", err)
	}
}

// TestAmbiguousReplacedRecordsReSignedTransaction covers the Cloud KMS shape
// of ambiguous recovery: the sighash proved the re-signed transaction
// identical, but its bytes — and hash — differ from the original signature.
// The original leaves the active set carrying its derived hash so
// observeReplacements keeps watching it, the re-signed row lands broadcast
// under the same nonce and inherits the sighash, and the payment leaves
// manual review for replaced (ADR-0004 decision 4).
func TestAmbiguousReplacedRecordsReSignedTransaction(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("3c", 34))
	rawHash := strings.Repeat("e", 64)
	sighash := strings.Repeat("a", 64)
	if err := store.MarkTxSigned(ctx, work.TransactionID, rawHash, sighash, 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxAmbiguous(ctx, paymentID, work.TransactionID, "worker"); err != nil {
		t.Fatalf("mark ambiguous: %v", err)
	}
	replacement := settlement.Replacement{
		Nonce: work.Nonce, TxHash: "0x" + strings.Repeat("b", 64), RawHash: strings.Repeat("b", 64),
		GasLimit: 120000, MaxFee: "30000000000", PriorityFee: "2000000000", SignerAddress: intentSigner,
	}
	if err := store.MarkTxAmbiguousReplaced(ctx, paymentID, work.TransactionID, replacement, "worker"); err != nil {
		t.Fatalf("mark ambiguous replaced: %v", err)
	}

	if state := paymentState(t, store, paymentID); state != "replaced" {
		t.Fatalf("state = %s, want replaced", state)
	}
	status, hash := txRow(t, store, work.TransactionID)
	if status != "replaced" || hash != "0x"+rawHash {
		// The original's hash was never recorded while ambiguous; the derived
		// hash is what observeReplacements can watch for a late landing.
		t.Fatalf("original status=%s hash=%s, want replaced / %s", status, hash, "0x"+rawHash)
	}
	active, err := store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatalf("load re-signed work: %v", err)
	}
	if active.TransactionID == work.TransactionID {
		t.Fatal("re-signed row must be a new transaction, not the ambiguous one")
	}
	if active.TxHash != replacement.TxHash || active.RawHash != replacement.RawHash {
		t.Fatalf("re-signed hashes = %q / %q, want %q / %q",
			active.TxHash, active.RawHash, replacement.TxHash, replacement.RawHash)
	}
	if active.Sighash != sighash {
		t.Fatalf("re-signed sighash = %q, want inherited %q", active.Sighash, sighash)
	}
	if active.Nonce != work.Nonce {
		t.Fatalf("re-signed nonce = %d, want %d", active.Nonce, work.Nonce)
	}
	var replacedBy string
	if err := store.Pool.QueryRow(ctx,
		`SELECT replaced_by_id FROM ethereum_transactions WHERE id = $1`, work.TransactionID).
		Scan(&replacedBy); err != nil {
		t.Fatal(err)
	}
	if replacedBy != active.TransactionID {
		t.Fatalf("replaced_by_id = %s, want %s", replacedBy, active.TransactionID)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM payment_transitions
WHERE payment_id = $1 AND from_state = 'manual_review' AND to_state = 'replaced'`, paymentID).
		Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("recovery transitions = %d, want 1", transitions)
	}
	pending, err := store.ListReplacedPending(ctx)
	if err != nil {
		t.Fatalf("list replaced pending: %v", err)
	}
	if len(pending) != 1 || pending[0].TxHash != "0x"+rawHash {
		t.Fatalf("replaced pending = %+v, want the original's derived hash", pending)
	}
	if err := store.MarkTxAmbiguousReplaced(ctx, paymentID, work.TransactionID, replacement, "worker"); !errors.Is(err, ErrSettlementRace) {
		t.Fatalf("second ambiguous replacement = %v, want ErrSettlementRace", err)
	}
}

func TestMarkTxReplacedChainsSameNonce(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, original, active := seedReplaced(t, store, strings.Repeat("2b", 34))

	if active.TransactionID == original.TransactionID {
		t.Fatal("replacement reused the original row")
	}
	if active.Nonce != original.Nonce || active.SignerAddress != intentSigner {
		t.Fatalf("replacement nonce/signer = %d/%s", active.Nonce, active.SignerAddress)
	}
	if active.TxHash != "0x"+strings.Repeat("d", 64) || active.MaxFeePerGas != "40000000000" {
		t.Fatalf("replacement work = %+v", active)
	}
	if active.BroadcastAttemptedAt.IsZero() {
		t.Fatal("replacement has no broadcast timestamp")
	}
	status, _ := txRow(t, store, original.TransactionID)
	if status != "replaced" {
		t.Fatalf("original status = %s, want replaced", status)
	}
	var replacedBy string
	if err := store.Pool.QueryRow(ctx,
		`SELECT replaced_by_id FROM ethereum_transactions WHERE id = $1`, original.TransactionID).
		Scan(&replacedBy); err != nil {
		t.Fatal(err)
	}
	if replacedBy != active.TransactionID {
		t.Fatalf("replaced_by = %s, want %s", replacedBy, active.TransactionID)
	}
	if state := paymentState(t, store, paymentID); state != "replaced" {
		t.Fatalf("state = %s, want replaced", state)
	}

	// Re-bumping a stalled replacement: the payment is already in replaced, so
	// the chain grows without a second payment transition.
	second := settlement.Replacement{
		Nonce: active.Nonce, TxHash: "0x" + strings.Repeat("b", 64), RawHash: strings.Repeat("b", 64),
		GasLimit: 120000, MaxFee: "45000000000", PriorityFee: "3400000000", SignerAddress: intentSigner,
	}
	if err := store.MarkTxReplaced(ctx, paymentID, active.TransactionID, second, "worker"); err != nil {
		t.Fatalf("re-bump: %v", err)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_transitions WHERE payment_id = $1 AND to_state = 'replaced'`, paymentID).
		Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("replaced transitions = %d, want 1", transitions)
	}
	var activeRows int
	if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM ethereum_transactions
WHERE payment_id = $1 AND status = ANY($2)`, paymentID, activeTransactionStatuses).Scan(&activeRows); err != nil {
		t.Fatal(err)
	}
	if activeRows != 1 {
		t.Fatalf("active transaction rows = %d, want 1", activeRows)
	}

	// The newest replacement is what confirmation finalizes — directly from
	// the replaced payment state.
	newest, err := store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatalf("load newest work: %v", err)
	}
	blockHash := "0x" + strings.Repeat("c", 64)
	if err := store.MarkTxConfirmed(ctx, paymentID, newest.TransactionID, 100, blockHash, 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("confirm from replaced: %v", err)
	}
	if state := paymentState(t, store, paymentID); state != "confirmed" {
		t.Fatalf("state = %s, want confirmed", state)
	}
}

func TestReplacementLandedOriginalWins(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, original, active := seedReplaced(t, store, strings.Repeat("3c", 34))
	blockHash := "0x" + strings.Repeat("c", 64)
	if err := store.MarkReplacementLanded(ctx, paymentID, original.TransactionID,
		true, 100, blockHash, 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("mark landed: %v", err)
	}
	if state := paymentState(t, store, paymentID); state != "confirming" {
		t.Fatalf("state = %s, want confirming", state)
	}
	status, _ := txRow(t, store, original.TransactionID)
	if status != "confirming" {
		t.Fatalf("original status = %s, want confirming", status)
	}
	status, _ = txRow(t, store, active.TransactionID)
	if status != "dropped" {
		t.Fatalf("replacement status = %s, want dropped", status)
	}
	// From here the confirmation worker finalizes the original as usual.
	if err := store.MarkTxConfirmed(ctx, paymentID, original.TransactionID, 100, blockHash, 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("confirm landed original: %v", err)
	}
}

func TestReplacementLandedReverted(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, original, active := seedReplaced(t, store, strings.Repeat("4d", 34))
	if err := store.MarkReplacementLanded(ctx, paymentID, original.TransactionID,
		false, 0, "", 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("mark landed: %v", err)
	}
	if state := paymentState(t, store, paymentID); state != "reverted" {
		t.Fatalf("state = %s, want reverted", state)
	}
	status, _ := txRow(t, store, original.TransactionID)
	if status != "reverted" {
		t.Fatalf("original status = %s, want reverted", status)
	}
	status, _ = txRow(t, store, active.TransactionID)
	if status != "dropped" {
		t.Fatalf("replacement status = %s, want dropped", status)
	}
}

func TestListReplacedPending(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, original, _ := seedReplaced(t, store, strings.Repeat("5e", 34))
	tracked, err := store.ListReplacedPending(ctx)
	if err != nil {
		t.Fatalf("list replaced: %v", err)
	}
	if len(tracked) != 1 || tracked[0].TransactionID != original.TransactionID ||
		tracked[0].PaymentID != paymentID || tracked[0].TxHash != "0x"+strings.Repeat("f", 64) {
		t.Fatalf("tracked = %+v", tracked)
	}
}

func TestReorgedOutReturnsToBroadcast(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("6f", 34))
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, "0x"+strings.Repeat("f", 64), "worker"); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	if err := store.MarkTxConfirming(ctx, paymentID, work.TransactionID, 100, "0x"+strings.Repeat("c", 64), "worker"); err != nil {
		t.Fatalf("mark confirming: %v", err)
	}
	if err := store.MarkTxReorgedOut(ctx, paymentID, work.TransactionID, "worker"); err != nil {
		t.Fatalf("mark reorged out: %v", err)
	}
	if state := paymentState(t, store, paymentID); state != "broadcast" {
		t.Fatalf("state = %s, want broadcast", state)
	}
	var status string
	var blockNumber *string
	if err := store.Pool.QueryRow(ctx,
		`SELECT status, block_number::text FROM ethereum_transactions WHERE id = $1`, work.TransactionID).
		Scan(&status, &blockNumber); err != nil {
		t.Fatal(err)
	}
	if status != "broadcast" || blockNumber != nil {
		t.Fatalf("status=%s block=%v", status, blockNumber)
	}
	// The re-sighting path works again after a reorg.
	if err := store.MarkTxConfirming(ctx, paymentID, work.TransactionID, 105, "0x"+strings.Repeat("a", 64), "worker"); err != nil {
		t.Fatalf("mark confirming after reorg: %v", err)
	}
}

func TestDroppedBlockingGapLifecycle(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("7a", 34))
	if err := store.MarkIntentExpired(ctx, paymentID, work.TransactionID, "worker"); err != nil {
		t.Fatalf("mark expired: %v", err)
	}
	// Nothing behind the gap: filling it would burn gas for no benefit.
	gaps, err := store.ListDroppedBlockingGaps(ctx, intentSigner, time.Now())
	if err != nil {
		t.Fatalf("list gaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("gaps without a later active nonce = %+v", gaps)
	}

	// A later nonce of the same signer goes active: the gap now blocks it.
	_, later := seedBroadcastingPayment(t, store, strings.Repeat("8b", 34))
	if later.Nonce != work.Nonce+1 {
		t.Fatalf("later nonce = %d, want %d", later.Nonce, work.Nonce+1)
	}
	if err := store.MarkTxSigned(ctx, later.TransactionID, strings.Repeat("9", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark later signed: %v", err)
	}
	if err := store.MarkTxBroadcast(ctx, later.PaymentID, later.TransactionID, "0x"+strings.Repeat("9", 64), "worker"); err != nil {
		t.Fatalf("mark later broadcast: %v", err)
	}
	// Application expiry starts at the safety margin. Until valid_before has
	// actually passed, replaying this authorization could still move USDC.
	gaps, err = store.ListDroppedBlockingGaps(ctx, intentSigner, time.Now())
	if err != nil {
		t.Fatalf("list gaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("not-yet-expired authorization selected as filler: %+v", gaps)
	}
	if _, err := store.Pool.Exec(ctx,
		`UPDATE payment_records SET valid_before = now() - interval '1 second' WHERE id = $1`,
		paymentID); err != nil {
		t.Fatalf("age expired authorization: %v", err)
	}
	gaps, err = store.ListDroppedBlockingGaps(ctx, intentSigner, time.Now())
	if err != nil {
		t.Fatalf("list aged gaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].TransactionID != work.TransactionID || gaps[0].Nonce != work.Nonce {
		t.Fatalf("gaps = %+v", gaps)
	}
	if gaps[0].Authorization.Value != "42" {
		t.Fatalf("gap authorization = %+v", gaps[0].Authorization)
	}

	rawHash := strings.Repeat("8", 64)
	txHash := "0x" + rawHash
	if err := store.MarkGapFillerPrepared(ctx, work.TransactionID, rawHash, txHash, []byte{1, 2, 3}, 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark gap filler broadcast: %v", err)
	}
	fillers, err := store.ListGapFillers(ctx)
	if err != nil {
		t.Fatalf("list gap fillers: %v", err)
	}
	if len(fillers) != 1 || fillers[0].TransactionID != work.TransactionID || fillers[0].TxHash != txHash {
		t.Fatalf("fillers = %+v", fillers)
	}
	if err := store.MarkGapFillerResolved(ctx, work.TransactionID, 64336, "1000000000"); err != nil {
		t.Fatalf("resolve gap filler: %v", err)
	}
	status, _ := txRow(t, store, work.TransactionID)
	if status != "reverted" {
		t.Fatalf("gap filler status = %s, want reverted", status)
	}
	fillers, err = store.ListGapFillers(ctx)
	if err != nil {
		t.Fatalf("list gap fillers: %v", err)
	}
	if len(fillers) != 0 {
		t.Fatalf("fillers after resolve = %+v", fillers)
	}
}

func TestSimulationFailedGapWaitsUntilAuthorizationExpiry(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, failed := seedBroadcastingPayment(t, store, strings.Repeat("1c", 34))
	if err := store.MarkIntentUnsettleable(ctx, paymentID, failed.TransactionID, "worker"); err != nil {
		t.Fatal(err)
	}
	_, later := seedBroadcastingPayment(t, store, strings.Repeat("2d", 34))
	if err := store.MarkTxSigned(ctx, later.TransactionID, strings.Repeat("3", 64),
		strings.Repeat("4", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTxBroadcast(ctx, later.PaymentID, later.TransactionID,
		"0x"+strings.Repeat("3", 64), "worker"); err != nil {
		t.Fatal(err)
	}

	gaps, err := store.ListDroppedBlockingGaps(ctx, intentSigner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("still-valid failed authorization selected: %+v", gaps)
	}
	if _, err := store.Pool.Exec(ctx,
		`UPDATE payment_records SET valid_before = now() - interval '1 second' WHERE id = $1`,
		paymentID); err != nil {
		t.Fatal(err)
	}
	gaps, err = store.ListDroppedBlockingGaps(ctx, intentSigner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].TransactionID != failed.TransactionID || gaps[0].State != settlement.StateFailed {
		t.Fatalf("expired failed gap = %+v", gaps)
	}
}

// TestGapFillerSucceededEscalatesAndStopsReporting covers the anomaly path: a
// filler the chain accepted moved USDC on an authorization believed expired.
// Before this the worker logged the same anomaly on every tick forever and left
// the payment recorded `expired` while the ledger said it settled.
func TestGapFillerSucceededEscalatesAndStopsReporting(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("e", 64)
	paymentID := seedPayment(t, store, paymentFixture{
		identity: identity, state: "verified", registered: true,
	})
	intent, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	// Drive it to a broadcast gap filler on an expired payment.
	if err := store.MarkIntentExpired(ctx, paymentID, intent.TransactionID, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkGapFillerPrepared(ctx, intent.TransactionID,
		repeat("a", 64), "0x"+repeat("a", 64), []byte{1, 2, 3}, 120000, "30000000000", "1000000000"); err != nil {
		t.Fatal(err)
	}
	fillers, err := store.ListGapFillers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fillers) != 1 {
		t.Fatalf("gap fillers = %d, want 1", len(fillers))
	}

	if err := store.MarkGapFillerSucceeded(ctx, paymentID, intent.TransactionID,
		12345, "0x"+repeat("b", 64), 51000, "31000000000", "worker"); err != nil {
		t.Fatal(err)
	}
	var state, txStatus string
	var blockNumber *int64
	if err := store.Pool.QueryRow(ctx, `
SELECT p.state, t.status, t.block_number
FROM payment_records p JOIN ethereum_transactions t ON t.payment_id = p.id
WHERE p.id = $1`, paymentID).Scan(&state, &txStatus, &blockNumber); err != nil {
		t.Fatal(err)
	}
	// Escalated, never finalized: recovery does not confirm payments.
	if state != "manual_review" {
		t.Fatalf("payment state = %q, want manual_review", state)
	}
	if txStatus != "confirming" || blockNumber == nil || *blockNumber != 12345 {
		t.Fatalf("transaction status = %q block = %v", txStatus, blockNumber)
	}
	// The observation loop must end, or the worker re-reports every tick.
	after, err := store.ListGapFillers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("gap fillers still listed after escalation: %d", len(after))
	}
	// Idempotence: a second escalation must not silently re-transition.
	if err := store.MarkGapFillerSucceeded(ctx, paymentID, intent.TransactionID,
		12345, "0x"+repeat("b", 64), 51000, "31000000000", "worker"); err == nil {
		t.Fatal("second escalation succeeded; the transition is not guarded")
	}
}
