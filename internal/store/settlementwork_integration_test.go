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

// seedBroadcastingPayment drives a registered payment through intent creation
// and returns the payment ID and the work view of it.
func seedBroadcastingPayment(t *testing.T, store *Store, identity string) (string, settlement.Work) {
	t.Helper()
	ctx := context.Background()
	paymentID := seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})
	if _, err := store.CreateSettlementIntent(ctx, intentRequest(identity)); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	work, err := store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatalf("load work: %v", err)
	}
	return paymentID, work
}

func TestIntentPersistsPayerSignature(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	identity := strings.Repeat("a1", 34)
	paymentID := seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})
	if _, err := store.CreateSettlementIntent(ctx, intentRequest(identity)); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	var signature string
	if err := store.Pool.QueryRow(ctx,
		`SELECT payer_signature FROM payment_records WHERE id = $1`, paymentID).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	if signature != intentRequest(identity).PayerSignature {
		t.Fatalf("payer_signature = %q", signature)
	}
}

func TestClaimPaymentExcludesLiveLease(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, _ := seedBroadcastingPayment(t, store, strings.Repeat("b2", 34))
	now := time.Now()
	lease, err := store.ClaimPayment(ctx, paymentID, "worker-a", time.Minute, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if lease.Worker != "worker-a" || lease.State != settlement.StateBroadcasting {
		t.Fatalf("lease = %+v", lease)
	}
	if _, err := store.ClaimPayment(ctx, paymentID, "worker-b", time.Minute, now); !errors.Is(err, settlement.ErrLeaseUnavailable) {
		t.Fatalf("second claim = %v, want ErrLeaseUnavailable", err)
	}
	// A lapsed lease is claimable again: that is how work survives a dead worker.
	if _, err := store.ClaimPayment(ctx, paymentID, "worker-b", time.Minute, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("claim after lapse: %v", err)
	}
}

func TestLoadSettlementWork(t *testing.T) {
	store := settlementTestStore(t)
	identity := strings.Repeat("c3", 34)
	paymentID, work := seedBroadcastingPayment(t, store, identity)
	if work.PaymentID != paymentID || work.PaymentIdentity != identity {
		t.Fatalf("work = %+v", work)
	}
	if work.TransactionStatus != "intent" || !work.BroadcastPending() || work.AmbiguousBroadcast() {
		t.Fatalf("fresh intent: %+v", work)
	}
	auth := work.Authorization
	if auth.From != "0x3333333333333333333333333333333333333333" ||
		auth.To != "0x1111111111111111111111111111111111111111" || auth.Value != "42" {
		t.Fatalf("authorization = %+v", auth)
	}
	if auth.Signature != intentRequest(identity).PayerSignature {
		t.Fatalf("signature = %q", auth.Signature)
	}
}

func TestBroadcastLifecycle(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("d4", 34))
	rawHash := strings.Repeat("e", 64)
	sighash := strings.Repeat("a", 64)
	txHash := "0x" + strings.Repeat("f", 64)

	if err := store.MarkTxSigned(ctx, work.TransactionID, rawHash, sighash, 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	signed, err := store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatalf("reload signed work: %v", err)
	}
	if signed.RawHash != rawHash || signed.Sighash != sighash {
		t.Fatalf("recovery hashes = %q / %q, want %q / %q", signed.RawHash, signed.Sighash, rawHash, sighash)
	}
	if err := store.MarkTxSigned(ctx, work.TransactionID, rawHash, sighash, 120000, "30000000000", "2000000000"); !errors.Is(err, ErrSettlementRace) {
		t.Fatalf("second mark signed = %v, want ErrSettlementRace", err)
	}
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, txHash, "worker"); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, txHash, "worker"); !errors.Is(err, ErrSettlementRace) {
		t.Fatalf("second mark broadcast = %v, want ErrSettlementRace", err)
	}
	var state, status, recordedHash string
	if err := store.Pool.QueryRow(ctx, `
SELECT p.state, t.status, t.tx_hash FROM payment_records p
JOIN ethereum_transactions t ON t.payment_id = p.id WHERE p.id = $1`, paymentID).
		Scan(&state, &status, &recordedHash); err != nil {
		t.Fatal(err)
	}
	if state != "broadcast" || status != "broadcast" || recordedHash != txHash {
		t.Fatalf("state=%s status=%s hash=%s", state, status, recordedHash)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_transitions WHERE payment_id = $1 AND to_state = 'broadcast'`,
		paymentID).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("broadcast transitions = %d, want 1", transitions)
	}
}

func TestAmbiguousMovesPaymentToManualReview(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("a5", 34))
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxAmbiguous(ctx, paymentID, work.TransactionID, "worker"); err != nil {
		t.Fatalf("mark ambiguous: %v", err)
	}
	var state, status string
	if err := store.Pool.QueryRow(ctx, `
SELECT p.state, t.status FROM payment_records p
JOIN ethereum_transactions t ON t.payment_id = p.id WHERE p.id = $1`, paymentID).
		Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if state != "manual_review" || status != "ambiguous" {
		t.Fatalf("state=%s status=%s", state, status)
	}
}

func TestConfirmationLifecycle(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("b6", 34))
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	txHash := "0x" + strings.Repeat("f", 64)
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, txHash, "worker"); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	blockHash := "0x" + strings.Repeat("c", 64)
	if err := store.MarkTxConfirming(ctx, paymentID, work.TransactionID, 100, blockHash, "worker"); err != nil {
		t.Fatalf("mark confirming: %v", err)
	}
	// Idempotent on repeat ticks: no duplicate transitions.
	if err := store.MarkTxConfirming(ctx, paymentID, work.TransactionID, 100, blockHash, "worker"); err != nil {
		t.Fatalf("mark confirming again: %v", err)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_transitions WHERE payment_id = $1 AND to_state = 'confirming'`,
		paymentID).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("confirming transitions = %d, want 1", transitions)
	}
	if err := store.MarkTxConfirmed(ctx, paymentID, work.TransactionID, 100, blockHash, 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("mark confirmed: %v", err)
	}
	var state, fromState string
	var confirmedAt *time.Time
	if err := store.Pool.QueryRow(ctx, `
SELECT p.state, p.confirmed_at, t.from_state FROM payment_records p
JOIN payment_transitions t ON t.payment_id = p.id AND t.to_state = 'confirmed'
WHERE p.id = $1`, paymentID).Scan(&state, &confirmedAt, &fromState); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" || confirmedAt == nil || fromState != "confirming" {
		t.Fatalf("state=%s confirmed_at=%v from=%s", state, confirmedAt, fromState)
	}
	// The HTTP waiter and confirmation worker may record the same receipt at
	// once. Repeating the identical terminal outcome is idempotent and must not
	// overwrite the first observation's accounting fields.
	if err := store.MarkTxConfirmed(ctx, paymentID, work.TransactionID, 100, blockHash, 1, "1", "http-wait"); err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	var gasUsed string
	if err := store.Pool.QueryRow(ctx,
		`SELECT gas_used::text FROM ethereum_transactions WHERE id = $1`, work.TransactionID).Scan(&gasUsed); err != nil {
		t.Fatal(err)
	}
	if gasUsed != "64336" {
		t.Fatalf("idempotent confirm overwrote gas_used = %s, want 64336", gasUsed)
	}
}

func TestRevertedLifecycle(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	paymentID, work := seedBroadcastingPayment(t, store, strings.Repeat("c7", 34))
	if err := store.MarkTxSigned(ctx, work.TransactionID, strings.Repeat("e", 64), strings.Repeat("a", 64), 120000, "30000000000", "2000000000"); err != nil {
		t.Fatalf("mark signed: %v", err)
	}
	if err := store.MarkTxBroadcast(ctx, paymentID, work.TransactionID, "0x"+strings.Repeat("f", 64), "worker"); err != nil {
		t.Fatalf("mark broadcast: %v", err)
	}
	if err := store.MarkTxReverted(ctx, paymentID, work.TransactionID, 64336, "1000000000", "worker"); err != nil {
		t.Fatalf("mark reverted: %v", err)
	}
	var state, status string
	var gasUsed string
	if err := store.Pool.QueryRow(ctx, `
SELECT p.state, t.status, t.gas_used::text FROM payment_records p
JOIN ethereum_transactions t ON t.payment_id = p.id WHERE p.id = $1`, paymentID).
		Scan(&state, &status, &gasUsed); err != nil {
		t.Fatal(err)
	}
	if state != "reverted" || status != "reverted" || gasUsed != "64336" {
		t.Fatalf("state=%s status=%s gas=%s", state, status, gasUsed)
	}
	if err := store.MarkTxReverted(ctx, paymentID, work.TransactionID, 1, "1", "http-wait"); err != nil {
		t.Fatalf("second revert: %v", err)
	}
	if err := store.Pool.QueryRow(ctx,
		`SELECT gas_used::text FROM ethereum_transactions WHERE id = $1`, work.TransactionID).Scan(&gasUsed); err != nil {
		t.Fatal(err)
	}
	if gasUsed != "64336" {
		t.Fatalf("idempotent revert overwrote gas_used = %s, want 64336", gasUsed)
	}
}
