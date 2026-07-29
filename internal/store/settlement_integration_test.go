//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

const intentSigner = "0x00000000000000000000000000000000000000b2"

func settlementTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ETH402_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databaseURL, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.Pool.Exec(ctx, "TRUNCATE merchants, payment_records CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `DELETE FROM signer_accounts WHERE signer_address = $1`, intentSigner); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedSignerAccount(ctx, store.Pool, intentSigner, 0); err != nil {
		t.Fatal(err)
	}
	return store
}

type paymentFixture struct {
	identity   string
	state      string
	registered bool
	validFor   time.Duration
}

// fixtureNonce hands out distinct authorization nonces. Payments are unique on
// (network, asset, payer, nonce), so fixtures sharing a nonce collide before the
// test under examination ever runs.
var fixtureNonce atomic.Uint64

func nextFixtureNonce() string {
	return fmt.Sprintf("0x%064x", fixtureNonce.Add(1))
}

// seedPayment inserts a payment row directly so each admission guard can be
// exercised without driving a full verification.
func seedPayment(t *testing.T, store *Store, fixture paymentFixture) string {
	t.Helper()
	ctx := context.Background()
	var merchantID any
	if fixture.registered {
		var id string
		if err := store.Pool.QueryRow(ctx, `INSERT INTO merchants
			(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
			VALUES ('Intent',$1,'example.com','0x1111111111111111111111111111111111111111','v1',now(),'active',now(),now())
			RETURNING id`, fixture.identity+"@example.com").Scan(&id); err != nil {
			t.Fatal(err)
		}
		merchantID = id
	}
	validFor := fixture.validFor
	if validFor == 0 {
		validFor = time.Hour
	}
	var paymentID string
	err := store.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,recipient_address,
		 amount_atomic,authorization_nonce,valid_after,valid_before,payload_hash,verification_status,state)
		VALUES ($1,$2,2,'exact','eip155:1','0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x3333333333333333333333333333333333333333','0x1111111111111111111111111111111111111111',
		        42,$3,now()-interval '1 minute',now()+$4::interval,$5,'verified',$6)
		RETURNING id`,
		fixture.identity, merchantID, nextFixtureNonce(),
		validFor.String(), repeat("f", 64), fixture.state).Scan(&paymentID)
	if err != nil {
		t.Fatal(err)
	}
	return paymentID
}

func intentRequest(identity string) settlement.IntentRequest {
	return settlement.IntentRequest{
		PaymentIdentity: identity, SignerAddress: intentSigner,
		PayerSignature: "0x" + repeat("1", 64) + repeat("2", 64) + "1b",
		ExpiryMargin:   time.Minute, Now: time.Now(),
		Quota: 100, QuotaWindow: 24 * time.Hour,
	}
}

func attemptRows(t *testing.T, store *Store, identity, result string) int {
	t.Helper()
	var count int
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM settlement_attempts WHERE payment_identity=$1 AND result=$2`,
		identity, result).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCreateSettlementIntentAcceptsVerifiedPayment(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("1", 64)
	paymentID := seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})

	intent, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	if intent.Duplicate || intent.PaymentID != paymentID || intent.Nonce != 0 {
		t.Fatalf("unexpected intent: %+v", intent)
	}

	var state string
	var requestedAt *time.Time
	if err := store.Pool.QueryRow(ctx,
		`SELECT state, settlement_requested_at FROM payment_records WHERE id=$1`, paymentID).
		Scan(&state, &requestedAt); err != nil {
		t.Fatal(err)
	}
	if state != "broadcasting" || requestedAt == nil {
		t.Fatalf("state=%q requested_at=%v", state, requestedAt)
	}
	var txStatus, txNonce string
	if err := store.Pool.QueryRow(ctx,
		`SELECT status, transaction_nonce::text FROM ethereum_transactions WHERE payment_id=$1`, paymentID).
		Scan(&txStatus, &txNonce); err != nil {
		t.Fatal(err)
	}
	// The intent must be durable before anything is signed, and it must not carry
	// a transaction hash yet.
	if txStatus != "intent" || txNonce != "0" {
		t.Fatalf("status=%q nonce=%q", txStatus, txNonce)
	}
	var hash *string
	if err := store.Pool.QueryRow(ctx, `SELECT tx_hash FROM ethereum_transactions WHERE payment_id=$1`, paymentID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != nil {
		t.Fatalf("intent already carries a transaction hash %q", *hash)
	}
	if got := attemptRows(t, store, identity, "accepted"); got != 1 {
		t.Fatalf("accepted attempts = %d, want 1", got)
	}
	var transitions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_transitions WHERE payment_id=$1 AND from_state='verified' AND to_state='broadcasting'`,
		paymentID).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("transitions = %d, want 1", transitions)
	}
}

func TestCreateSettlementIntentIsIdempotentPerPayment(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("2", 64)
	seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})

	first, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate {
		t.Fatal("second intent was not reported as a duplicate")
	}
	// Crucially the nonce must be reused, not reallocated: a second nonce would
	// mean two transactions racing to move the same USDC.
	if second.Nonce != first.Nonce || second.TransactionID != first.TransactionID {
		t.Fatalf("duplicate allocated new work: %+v vs %+v", second, first)
	}
	var transactions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM ethereum_transactions WHERE payment_id=$1`, first.PaymentID).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 {
		t.Fatalf("ethereum_transactions rows = %d, want 1", transactions)
	}
	if got := attemptRows(t, store, identity, "duplicate"); got != 1 {
		t.Fatalf("duplicate attempts = %d, want 1", got)
	}
}

func TestCreateSettlementIntentRejectsUnregisteredRecipient(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("3", 64)
	seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: false})

	// ADR-0004 decision 9: without this gate, anyone can make the operator pay gas
	// for a valid self-to-self transfer at zero cost to themselves.
	_, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if !errors.Is(err, settlement.ErrRecipientNotMerchant) {
		t.Fatalf("error = %v, want ErrRecipientNotMerchant", err)
	}
	assertNoNonceSpent(t, store, identity)
	if got := attemptRows(t, store, identity, "rejected"); got != 1 {
		t.Fatalf("rejected attempts = %d, want 1", got)
	}
}

func TestCreateSettlementIntentRejectsExpiringAuthorization(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("4", 64)
	// Valid for 30s against a 60s margin: EIP-3009 would revert this on-chain.
	seedPayment(t, store, paymentFixture{
		identity: identity, state: "verified", registered: true, validFor: 30 * time.Second,
	})

	_, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if !errors.Is(err, settlement.ErrAuthorizationExpiring) {
		t.Fatalf("error = %v, want ErrAuthorizationExpiring", err)
	}
	assertNoNonceSpent(t, store, identity)
}

func TestCreateSettlementIntentRejectsUnverifiedPayment(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	for i, state := range []string{"received", "confirmed", "reverted", "expired"} {
		// Index rather than state initial: "received" and "reverted" share one.
		identity := fmt.Sprintf("pay_%s%d", repeat("5", 63), i)
		seedPayment(t, store, paymentFixture{identity: identity, state: state, registered: true})
		_, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
		if !errors.Is(err, settlement.ErrPaymentNotVerified) {
			t.Fatalf("state %q returned %v, want ErrPaymentNotVerified", state, err)
		}
		assertNoNonceSpent(t, store, identity)
	}
}

func TestCreateSettlementIntentRejectsUnknownPayment(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("6", 64)
	_, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if !errors.Is(err, settlement.ErrPaymentNotFound) {
		t.Fatalf("error = %v, want ErrPaymentNotFound", err)
	}
	assertNoNonceSpent(t, store, identity)
	if got := attemptRows(t, store, identity, "rejected"); got != 1 {
		t.Fatalf("rejected attempts = %d, want 1", got)
	}
}

// assertNoNonceSpent proves a refused settlement left the nonce sequence intact.
// A rejection that consumed a nonce would gap the sequence and stall every later
// settlement behind the missing transaction.
func assertNoNonceSpent(t *testing.T, store *Store, identity string) {
	t.Helper()
	ctx := context.Background()
	var next string
	if err := store.Pool.QueryRow(ctx,
		`SELECT next_nonce::text FROM signer_accounts WHERE signer_address=$1`, intentSigner).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != "0" {
		t.Fatalf("rejected settlement of %s consumed a nonce; next_nonce = %s, want 0", identity, next)
	}
}

func TestConcurrentSettlementCreatesOneIntent(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("7", 64)
	paymentID := seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})

	const callers = 6
	start := make(chan struct{})
	var wait sync.WaitGroup
	intents := make([]settlement.Intent, callers)
	failures := make([]error, callers)
	for i := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			intents[i], failures[i] = store.CreateSettlementIntent(ctx, intentRequest(identity))
		}()
	}
	close(start)
	wait.Wait()

	fresh := 0
	for i, err := range failures {
		if err != nil {
			t.Fatalf("caller %d failed: %v", i, err)
		}
		if !intents[i].Duplicate {
			fresh++
		}
	}
	// Exactly one caller may allocate; the rest must observe that intent.
	if fresh != 1 {
		t.Fatalf("%d callers allocated an intent, want 1", fresh)
	}
	var transactions int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM ethereum_transactions WHERE payment_id=$1`, paymentID).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 {
		t.Fatalf("ethereum_transactions rows = %d, want 1", transactions)
	}
	var next string
	if err := store.Pool.QueryRow(ctx,
		`SELECT next_nonce::text FROM signer_accounts WHERE signer_address=$1`, intentSigner).Scan(&next); err != nil {
		t.Fatal(err)
	}
	// One nonce consumed in total, however the callers interleaved.
	if next != "1" {
		t.Fatalf("next_nonce = %s, want 1", next)
	}
}

// TestMerchantSettlementQuotaBoundsIntents is the bound ADR-0004 decision 9
// rests on. The recipient gate ensures gas is only spent for a party that
// accepted terms and can be suspended, but says nothing about how much;
// registration is not Sybil-resistant, so without this one registration buys
// unbounded gas.
func TestMerchantSettlementQuotaBoundsIntents(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	const quota = 3

	// One merchant with several payments. seedPayment creates a fresh merchant
	// per call, so the rest are re-attributed to the first: the quota is
	// per-merchant, and a merchant with one payment each would never reach it.
	identities := make([]string, 0, quota+1)
	var merchantID string
	for i := range quota + 1 {
		identity := fmt.Sprintf("pay_%s%02d", repeat("9", 62), i)
		paymentID := seedPayment(t, store, paymentFixture{
			identity: identity, state: "verified", registered: i == 0,
		})
		if i == 0 {
			if err := store.Pool.QueryRow(ctx,
				`SELECT merchant_id FROM payment_records WHERE id=$1`, paymentID).Scan(&merchantID); err != nil {
				t.Fatal(err)
			}
		} else if _, err := store.Pool.Exec(ctx,
			`UPDATE payment_records SET merchant_id=$2 WHERE id=$1`, paymentID, merchantID); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
	}

	request := func(identity string) settlement.IntentRequest {
		r := intentRequest(identity)
		r.Quota, r.QuotaWindow = quota, 24*time.Hour
		return r
	}
	for i := range quota {
		if _, err := store.CreateSettlementIntent(ctx, request(identities[i])); err != nil {
			t.Fatalf("intent %d within quota failed: %v", i, err)
		}
	}
	// The next one must be refused, and must not consume a nonce.
	before := nextNonce(t, store)
	_, err := store.CreateSettlementIntent(ctx, request(identities[quota]))
	if !errors.Is(err, settlement.ErrMerchantQuotaExceeded) {
		t.Fatalf("intent beyond quota returned %v, want ErrMerchantQuotaExceeded", err)
	}
	if after := nextNonce(t, store); after != before {
		t.Fatalf("refused settlement consumed a nonce: %s -> %s", before, after)
	}
	if got := attemptRows(t, store, identities[quota], "rejected"); got != 1 {
		t.Fatalf("rejected attempts = %d, want 1", got)
	}

	// A window that has moved past the earlier intents admits again, proving the
	// quota rolls rather than latching permanently.
	rolled := request(identities[quota])
	rolled.QuotaWindow = time.Nanosecond
	if _, err := store.CreateSettlementIntent(ctx, rolled); err != nil {
		t.Fatalf("intent outside the window was refused: %v", err)
	}
}

func nextNonce(t *testing.T, store *Store) string {
	t.Helper()
	var next string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT next_nonce::text FROM signer_accounts WHERE signer_address=$1`, intentSigner).Scan(&next); err != nil {
		t.Fatal(err)
	}
	return next
}
