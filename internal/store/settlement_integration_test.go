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
		Quota: 100, GlobalQuota: 10_000, QuotaWindow: 24 * time.Hour,
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

func TestCreateSettlementIntentReturnsTerminalHash(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("c", 64)
	seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})

	first, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	rawHash := repeat("d", 64)
	txHash := "0x" + rawHash
	if err := store.MarkTxSigned(ctx, first.TransactionID, rawHash, repeat("e", 64),
		120000, "30000000000", "2000000000"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTxBroadcast(ctx, first.PaymentID, first.TransactionID, txHash, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTxConfirmed(ctx, first.PaymentID, first.TransactionID, 123,
		"0x"+repeat("f", 64), 51000, "1000000000", "worker"); err != nil {
		t.Fatal(err)
	}

	retry, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || retry.TxHash != txHash || retry.TransactionID != first.TransactionID {
		t.Fatalf("terminal retry = %+v", retry)
	}
	if retry.Reverted || !retry.Confirmed {
		t.Fatalf("confirmed terminal retry has wrong outcome: %+v", retry)
	}
}

func TestCreateSettlementIntentReturnsRevertedTerminalHash(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("4", 64)
	seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})

	first, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	rawHash := repeat("6", 64)
	txHash := "0x" + rawHash
	if err := store.MarkTxSigned(ctx, first.TransactionID, rawHash, repeat("7", 64),
		120000, "30000000000", "2000000000"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTxBroadcast(ctx, first.PaymentID, first.TransactionID, txHash, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTxReverted(ctx, first.PaymentID, first.TransactionID, 51000, "1000000000", "worker"); err != nil {
		t.Fatal(err)
	}

	// The duplicate must be told the terminal outcome was a revert, so the
	// /settle response can report the terminal failure instead of a false
	// success.
	retry, err := store.CreateSettlementIntent(ctx, intentRequest(identity))
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || !retry.Reverted || retry.Confirmed || retry.TxHash != txHash {
		t.Fatalf("reverted terminal retry = %+v", retry)
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

func TestCreateSettlementIntentRejectsSuspendedMerchant(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	identity := "pay_" + repeat("a", 64)
	paymentID := seedPayment(t, store, paymentFixture{identity: identity, state: "verified", registered: true})
	if _, err := store.Pool.Exec(ctx, `
UPDATE merchants SET status = 'suspended'
WHERE id = (SELECT merchant_id FROM payment_records WHERE id = $1)`, paymentID); err != nil {
		t.Fatal(err)
	}

	// Merchant attribution is durable, but admission must use current status:
	// suspending a merchant after /verify must revoke later /settle requests.
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

	// Once the window has rolled past the earlier intents the merchant is admitted
	// again, proving the quota rolls rather than latching permanently.
	//
	// The existing rows are aged in the database rather than by advancing this
	// process's clock. settlement_requested_at is stamped by the database, so
	// comparing it against a Go timestamp at a hairline threshold would be flaky
	// under clock skew; and advancing Now far enough to matter would push the
	// authorization past valid_before, tripping the expiry guard instead.
	if _, err := store.Pool.Exec(ctx, `
UPDATE payment_records SET settlement_requested_at = settlement_requested_at - interval '48 hours'
WHERE merchant_id = $1 AND settlement_requested_at IS NOT NULL`, merchantID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSettlementIntent(ctx, request(identities[quota])); err != nil {
		t.Fatalf("intent outside the window was refused: %v", err)
	}
}

// TestMerchantSettlementQuotaSerializesConcurrentPayments forces two requests
// for different payments to reach the merchant admission boundary together.
//
// The blocker holds the merchant row before the requests start. With the
// merchant-scoped lock both callers wait there, then run one at a time after the
// blocker commits. A broken implementation that locks only each payment ignores
// the blocker and admits both against the same pre-limit count. This
// orchestration is intentional: a start channel alone often lets one request
// finish before the other counts and therefore passes against the race.
func TestMerchantSettlementQuotaSerializesConcurrentPayments(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	const quota = 1

	identities := []string{
		"pay_" + repeat("b", 64),
		"pay_" + repeat("c", 64),
	}
	firstPaymentID := seedPayment(t, store, paymentFixture{
		identity: identities[0], state: "verified", registered: true,
	})
	var merchantID string
	if err := store.Pool.QueryRow(ctx,
		`SELECT merchant_id FROM payment_records WHERE id = $1`, firstPaymentID).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	secondPaymentID := seedPayment(t, store, paymentFixture{
		identity: identities[1], state: "verified", registered: false,
	})
	if _, err := store.Pool.Exec(ctx,
		`UPDATE payment_records SET merchant_id = $2 WHERE id = $1`, secondPaymentID, merchantID); err != nil {
		t.Fatal(err)
	}

	blocker, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `SELECT id FROM merchants WHERE id = $1 FOR UPDATE`, merchantID); err != nil {
		t.Fatal(err)
	}

	type result struct{ err error }
	results := make(chan result, len(identities))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, identity := range identities {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			request := intentRequest(identity)
			request.Quota = quota
			results <- result{err: func() error {
				_, err := store.CreateSettlementIntent(ctx, request)
				return err
			}()}
		}()
	}
	close(start)

	// Both correct callers block on the merchant row. A broken implementation
	// completes both without touching it. Poll PostgreSQL lock state rather than
	// relying on a sleep that might pass merely because one goroutine ran first.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(results) == len(identities) {
			break
		}
		var blocked int
		if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query LIKE '%SELECT id FROM merchants%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= len(identities) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("settlement callers neither completed nor reached the merchant lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(results)

	var accepted, rejected int
	for result := range results {
		switch {
		case result.err == nil:
			accepted++
		case errors.Is(result.err, settlement.ErrMerchantQuotaExceeded):
			rejected++
		default:
			t.Fatalf("concurrent settlement returned unexpected error: %v", result.err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want one of each", accepted, rejected)
	}
	var committed int
	if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM payment_records
WHERE merchant_id = $1 AND settlement_requested_at IS NOT NULL`, merchantID).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if committed != quota {
		t.Fatalf("committed intents = %d, want quota %d", committed, quota)
	}
	if next := nextNonce(t, store); next != "1" {
		t.Fatalf("next_nonce = %s, want 1; quota rejection consumed a nonce", next)
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

// seedPaymentForMerchant inserts a verified payment attributed to a merchant with
// its own recipient address, so two of them are genuinely two merchants. The
// shared seedPayment helper reuses one recipient, which the merchants table treats
// as one merchant — fine for per-merchant tests, useless for a facilitator-wide one.
func seedPaymentForMerchant(t *testing.T, store *Store, identity, recipient string) string {
	t.Helper()
	ctx := context.Background()
	var merchantID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
		VALUES ('Global',$1,'example.com',$2,'v1',now(),'active',now(),now())
		RETURNING id`, identity+"@example.com", recipient).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	var paymentID string
	err := store.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,recipient_address,
		 amount_atomic,authorization_nonce,valid_after,valid_before,payload_hash,verification_status,state)
		VALUES ($1,$2,2,'exact','eip155:1','0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x3333333333333333333333333333333333333333',$3,
		        42,$4,now()-interval '1 minute',now()+interval '1 hour',$5,'verified','verified')
		RETURNING id`,
		identity, merchantID, recipient, nextFixtureNonce(), repeat("f", 64)).Scan(&paymentID)
	if err != nil {
		t.Fatal(err)
	}
	return paymentID
}

// TestGlobalQuotaHoldsAcrossMerchantsConcurrently is the test the per-merchant
// quota cannot cover.
//
// The merchant row lock serialises one merchant's requests. It says nothing about
// two *different* merchants settling at the same moment, so without a lock spanning
// every merchant both would count the same pre-limit snapshot and both commit —
// collectively exceeding the facilitator's own ceiling. That ceiling bounds gas,
// which is the operator's money, so an approximate answer is not good enough.
//
// Both callers are parked on the global lock before it is released, which is what
// makes this a real race rather than two calls that happened to be sequential.
func TestGlobalQuotaHoldsAcrossMerchantsConcurrently(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()

	first := seedPaymentForMerchant(t, store, "global-a", "0x4444444444444444444444444444444444444444")
	second := seedPaymentForMerchant(t, store, "global-b", "0x5555555555555555555555555555555555555555")
	identities := map[string]string{"global-a": first, "global-b": second}

	// Hold the facilitator-wide lock so both callers reach it and wait.
	blocker, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, globalSettlementLockKey); err != nil {
		t.Fatal(err)
	}

	type outcome struct{ err error }
	results := make(chan outcome, len(identities))
	var wait sync.WaitGroup
	for identity := range identities {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := intentRequest(identity)
			// Both bounds are one, which isolates the global one anyway: each
			// merchant has settled nothing, so each passes its own quota, and only
			// the facilitator-wide count can refuse the second. Setting the
			// per-merchant quota higher than the global one is refused by
			// IntentRequest.Validate, because in production that combination makes
			// the per-merchant number unreachable and therefore a lie.
			request.Quota, request.GlobalQuota = 1, 1
			_, err := store.CreateSettlementIntent(ctx, request)
			results <- outcome{err: err}
		}()
	}

	// Wait until both are blocked on the advisory lock rather than sleeping, which
	// would pass whenever one goroutine simply ran first.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query LIKE '%pg_advisory_xact_lock%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= len(identities) {
			break
		}
		if time.Now().After(deadline) {
			// Deliberately not a failure. This poll forces the interleaving for the
			// current implementation; if the global lock is ever replaced by another
			// mechanism, the invariant below still needs checking and a harness
			// precondition failing here would report the wrong thing.
			// TestGlobalQuotaHoldsUnderRepeatedRaces checks the same invariant
			// without depending on any lock existing.
			t.Logf("callers did not park on a facilitator-wide lock (%d of %d); "+
				"checking the invariant anyway", waiting, len(identities))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(results)

	accepted, rejected := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			accepted++
		case errors.Is(result.err, settlement.ErrGlobalQuotaExceeded):
			rejected++
		default:
			t.Fatalf("concurrent settlement returned unexpected error: %v", result.err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want one of each; the facilitator-wide ceiling was exceeded", accepted, rejected)
	}
	var committed int
	if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM payment_records WHERE settlement_requested_at IS NOT NULL`).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if committed != 1 {
		t.Fatalf("committed intents = %d, want 1", committed)
	}
	// A rejection must not consume a nonce, or the ceiling would create nonce gaps.
	if next := nextNonce(t, store); next != "1" {
		t.Fatalf("next_nonce = %s, want 1; a global-quota rejection consumed a nonce", next)
	}
}

// TestGlobalQuotaRejectsBeforePerMerchantQuotaIsReached confirms the two bounds are
// independent: a merchant that has settled nothing, and so is inside its own
// allowance, is still refused once the facilitator has spent its total.
func TestGlobalQuotaRejectsBeforePerMerchantQuotaIsReached(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	first := seedPaymentForMerchant(t, store, "cap-a", "0x6666666666666666666666666666666666666666")
	_ = first
	second := seedPaymentForMerchant(t, store, "cap-b", "0x7777777777777777777777777777777777777777")
	_ = second

	request := intentRequest("cap-a")
	request.Quota, request.GlobalQuota = 1, 1
	if _, err := store.CreateSettlementIntent(ctx, request); err != nil {
		t.Fatalf("first settlement within both bounds failed: %v", err)
	}
	next := intentRequest("cap-b")
	next.Quota, next.GlobalQuota = 1, 1
	_, err := store.CreateSettlementIntent(ctx, next)
	if !errors.Is(err, settlement.ErrGlobalQuotaExceeded) {
		t.Fatalf("second settlement returned %v, want ErrGlobalQuotaExceeded", err)
	}
	// Reported as the facilitator's limit, not the merchant's: the merchant did
	// nothing wrong and should retry later rather than investigate itself.
	if errors.Is(err, settlement.ErrMerchantQuotaExceeded) {
		t.Error("a facilitator-wide refusal must not be reported as the merchant's quota")
	}
}

// TestGlobalQuotaHoldsUnderRepeatedRaces checks the facilitator-wide ceiling without
// referring to how it is enforced.
//
// Each round releases two merchants' settlements simultaneously from a barrier and
// raises the ceiling by exactly one, so the committed total must equal the round
// number. If concurrent callers can both pass the same snapshot, the total runs
// ahead and the round that does it fails — and unlike the lock-parking test above,
// this keeps working if the mechanism changes.
func TestGlobalQuotaHoldsUnderRepeatedRaces(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	const rounds = 25

	for round := 1; round <= rounds; round++ {
		left := fmt.Sprintf("race-%d-a", round)
		right := fmt.Sprintf("race-%d-b", round)
		seedPaymentForMerchant(t, store, left, fmt.Sprintf("0x%040x", round*2+0x100))
		seedPaymentForMerchant(t, store, right, fmt.Sprintf("0x%040x", round*2+0x101))

		start := make(chan struct{})
		var wait sync.WaitGroup
		errs := make(chan error, 2)
		for _, identity := range []string{left, right} {
			wait.Add(1)
			go func() {
				defer wait.Done()
				request := intentRequest(identity)
				request.Quota, request.GlobalQuota = round, round
				<-start
				_, err := store.CreateSettlementIntent(ctx, request)
				errs <- err
			}()
		}
		close(start)
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil && !errors.Is(err, settlement.ErrGlobalQuotaExceeded) &&
				!errors.Is(err, settlement.ErrMerchantQuotaExceeded) {
				t.Fatalf("round %d: unexpected error: %v", round, err)
			}
		}
		var committed int
		if err := store.Pool.QueryRow(ctx, `
SELECT count(*) FROM payment_records WHERE settlement_requested_at IS NOT NULL`).Scan(&committed); err != nil {
			t.Fatal(err)
		}
		if committed > round {
			t.Fatalf("round %d: %d intents committed against a ceiling of %d; "+
				"concurrent callers exceeded the facilitator-wide quota", round, committed, round)
		}
	}
}
