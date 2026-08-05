//go:build integration

package merchant

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/secret"
	"github.com/ETH402/facilitator/migrations"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	siwe "github.com/signinwithethereum/siwe-go"
)

type captureSender struct{ message email.Message }

func (s *captureSender) Send(_ context.Context, message email.Message) error {
	s.message = message
	return nil
}

type retrySender struct {
	messages []email.Message
	failures int
}

type stagedSender struct {
	mu       sync.Mutex
	calls    int
	started  chan int
	releases [2]chan struct{}
	errors   [2]error
}

type captureEmailObserver struct {
	pending, failures int64
	oldest            time.Duration
	tick              time.Time
}

func (o *captureEmailObserver) ObserveEmailOutbox(pending int64, oldest time.Duration, at time.Time) {
	o.pending, o.oldest, o.tick = pending, oldest, at
}

func (o *captureEmailObserver) ObserveEmailDeliveryFailure() { o.failures++ }

func (s *stagedSender) Send(_ context.Context, _ email.Message) error {
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.mu.Unlock()
	s.started <- call
	<-s.releases[call]
	return s.errors[call]
}

func (s *retrySender) Send(_ context.Context, message email.Message) error {
	s.messages = append(s.messages, message)
	if s.failures > 0 {
		s.failures--
		return fmt.Errorf("provider rejected %s with body %s", message.To, message.TextBody)
	}
	return nil
}

// testPool migrates a clean database and serialises access against the other
// integration packages through a shared advisory lock.
func testPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := conn.Exec(ctx, "TRUNCATE merchants CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(402001)`); err != nil {
		lock.Release()
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(402001)`)
		lock.Release()
		pool.Close()
	})
	return pool
}

// activate drives a merchant through registration, email proof, and recipient
// proof, returning its identifier and the key controlling the recipient.
func activate(t *testing.T, service *Service, sender *captureSender, address string, key *ecdsa.PrivateKey) string {
	merchantID, _ := activateWithAPIKey(t, service, sender, address, key)
	return merchantID
}

func activateWithAPIKey(t *testing.T, service *Service, sender *captureSender, address string, key *ecdsa.PrivateKey) (string, string) {
	t.Helper()
	ctx := context.Background()
	if err := service.Register(ctx, Registration{
		Name: "Cooldown merchant", Email: "cooldown@example.com",
		Recipient: address, AcceptTerms: true,
	}, "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(strings.TrimPrefix(sender.message.TextBody, "Verify your email: "))
	if err != nil {
		t.Fatal(err)
	}
	merchantID, err := service.VerifyEmail(ctx, link.Query().Get("token"), "request-2")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.WalletChallenge(ctx, merchantID, "", "verify-recipient", "request-3")
	if err != nil {
		t.Fatal(err)
	}
	apiKey, err := service.VerifyWallet(ctx, merchantID, challenge.ID, challenge.Message,
		signMessage(t, challenge.Message, key), "verify-recipient", "request-4")
	if err != nil {
		t.Fatal(err)
	}
	return merchantID, apiKey
}

func elevatedAdminSession(t *testing.T, service *Service, sender *captureSender, emailAddress, merchantID string, key *ecdsa.PrivateKey) AdminSession {
	t.Helper()
	ctx := context.Background()
	if err := service.RequestAdminLink(ctx, emailAddress, "admin-link-"+strings.ReplaceAll(emailAddress, "@", "-")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(strings.TrimPrefix(sender.message.TextBody, "Sign in to your ETH402 merchant panel: "))
	if err != nil {
		t.Fatal(err)
	}
	id, session, err := service.VerifyEmailForAdmin(ctx, link.Query().Get("token"), "admin-email-verified")
	if err != nil || id != merchantID {
		t.Fatalf("admin email verification = %q %+v %v", id, session, err)
	}
	challenge, err := service.WalletChallenge(ctx, merchantID, "", "authenticate-admin", "admin-wallet-challenge")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.VerifyAdminWallet(ctx, merchantID, session.Token, challenge.ID, challenge.Message,
		signMessage(t, challenge.Message, key), "admin-wallet-verified"); err != nil {
		t.Fatal(err)
	}
	return session
}

func waitForChallengeLock(t *testing.T, pool *pgxpool.Pool, challengeID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tx, err := pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var id string
		err = tx.QueryRow(context.Background(), `SELECT id FROM wallet_verification_challenges
			WHERE id=$1 FOR UPDATE NOWAIT`, challengeID).Scan(&id)
		_ = tx.Rollback(context.Background())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recipient change did not acquire its challenge lock")
}

func TestPendingRecipientReplacementRequiresCurrentWalletProof(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: time.Hour,
		AdminSessionTTL: time.Hour,
		Pepper:          []byte("01234567890123456789012345678901"),
		EmailOutboxKey:  bytes.Repeat([]byte{0x42}, 32),
	})
	originalKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	originalAddress := crypto.PubkeyToAddress(originalKey.PublicKey).Hex()
	if err = service.Register(ctx, Registration{Name: "Pending replacement", Email: "pending-replace@example.com",
		Recipient: originalAddress, AcceptTerms: true}, "pending-register"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(strings.TrimPrefix(sender.message.TextBody, "Verify your email: "))
	if err != nil {
		t.Fatal(err)
	}
	merchantID, err := service.VerifyEmail(ctx, link.Query().Get("token"), "pending-email")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := service.WalletChallenge(ctx, merchantID, "", "verify-recipient", "pending-old-challenge")
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	firstAddress := crypto.PubkeyToAddress(firstKey.PublicKey).Hex()
	first, err := service.PendingRecipientChallenge(ctx, merchantID, firstAddress, "pending-first-replacement")
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secondAddress := crypto.PubkeyToAddress(secondKey.PublicKey).Hex()
	second, err := service.PendingRecipientChallenge(ctx, merchantID, secondAddress, "pending-second-replacement")
	if err != nil {
		t.Fatal(err)
	}
	for name, attempt := range map[string]struct {
		challenge Challenge
		key       *ecdsa.PrivateKey
	}{"original": {stale, originalKey}, "first replacement": {first, firstKey}} {
		if _, verifyErr := service.VerifyWallet(ctx, merchantID, attempt.challenge.ID, attempt.challenge.Message,
			signMessage(t, attempt.challenge.Message, attempt.key), "verify-recipient", "pending-stale-"+name); !errors.Is(verifyErr, ErrForbidden) {
			t.Fatalf("%s challenge returned %v", name, verifyErr)
		}
	}
	apiKey, err := service.VerifyWallet(ctx, merchantID, second.ID, second.Message,
		signMessage(t, second.Message, secondKey), "verify-recipient", "pending-current")
	if err != nil || apiKey == "" {
		t.Fatalf("current replacement activation = %q %v", apiKey, err)
	}
	principal, err := service.Authenticate(ctx, apiKey)
	if err != nil || !strings.EqualFold(principal.Recipient, secondAddress) || principal.Status != "active" {
		t.Fatalf("activated replacement = %+v %v", principal, err)
	}
	var keys, history int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE merchant_id=$1`, merchantID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM recipient_address_history WHERE merchant_id=$1`, merchantID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if keys != 1 || history != 1 {
		t.Fatalf("activation artifacts keys=%d history=%d", keys, history)
	}
	if _, err = service.PendingRecipientChallenge(ctx, merchantID, originalAddress, "pending-after-active"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("active merchant replaced through pending flow: %v", err)
	}
	if _, err = service.PendingRecipientChallenge(ctx, merchantID, "0x0000000000000000000000000000000000000000", "pending-zero"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero recipient returned %v", err)
	}
}

func TestAdminRecipientChangeSerializesSessionsAndElevation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: time.Hour,
		AdminSessionTTL: time.Hour, RecipientCooldown: 0,
		Pepper:         []byte("01234567890123456789012345678901"),
		EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	originalKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	merchantID := activate(t, service, sender, crypto.PubkeyToAddress(originalKey.PublicKey).Hex(), originalKey)
	sessionA := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	sessionB := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	challengeA, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(keyA.PublicKey).Hex(), "change-recipient", "change-a")
	if err != nil {
		t.Fatal(err)
	}
	challengeB, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(keyB.PublicKey).Hex(), "change-recipient", "change-b")
	if err != nil {
		t.Fatal(err)
	}
	signatureA := signMessage(t, challengeA.Message, keyA)
	signatureB := signMessage(t, challengeB.Message, keyB)
	type result struct {
		session AdminSession
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, attempt := range []struct {
		session   AdminSession
		challenge Challenge
		signature string
		requestID string
	}{{sessionA, challengeA, signatureA, "verify-a"}, {sessionB, challengeB, signatureB, "verify-b"}} {
		go func() {
			<-start
			results <- result{attempt.session, service.VerifyAdminRecipientChange(ctx, merchantID, attempt.session.Token,
				attempt.challenge.ID, attempt.challenge.Message, attempt.signature, attempt.requestID)}
		}()
	}
	close(start)
	first, second := <-results, <-results
	var winner, loser result
	if first.err == nil && errors.Is(second.err, ErrForbidden) {
		winner, loser = first, second
	} else if second.err == nil && errors.Is(first.err, ErrForbidden) {
		winner, loser = second, first
	} else {
		t.Fatalf("concurrent recipient changes returned %v and %v", first.err, second.err)
	}
	winnerPrincipal, err := service.AuthenticateAdmin(ctx, winner.session.Token)
	if err != nil || !winnerPrincipal.WalletAuthenticated {
		t.Fatalf("winning session was not elevated: %+v %v", winnerPrincipal, err)
	}
	loserPrincipal, err := service.AuthenticateAdmin(ctx, loser.session.Token)
	if err != nil || loserPrincipal.WalletAuthenticated {
		t.Fatalf("losing session retained elevation: %+v %v", loserPrincipal, err)
	}
	var history int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM recipient_address_history WHERE merchant_id=$1`, merchantID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("concurrent recipient changes wrote %d history rows", history)
	}
}

// TestAdminSensitiveOperationsRecheckRotatedSession pins the authorization
// boundary below HTTP middleware. The first AuthenticateAdmin call models a
// request admitted with the old recipient proof; rotating the recipient before
// its service operation must still prevent every wallet-gated read and write.
func TestAdminSensitiveOperationsRecheckRotatedSession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: time.Hour,
		AdminSessionTTL: time.Hour, RecipientCooldown: 0, PaymentRetention: 24 * time.Hour,
		Pepper:         []byte("01234567890123456789012345678901"),
		EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	originalKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	merchantID := activate(t, service, sender, crypto.PubkeyToAddress(originalKey.PublicKey).Hex(), originalKey)
	staleSession := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	changingSession := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	if principal, authErr := service.AuthenticateAdmin(ctx, staleSession.Token); authErr != nil || !principal.WalletAuthenticated {
		t.Fatalf("pre-rotation middleware authentication failed: %+v %v", principal, authErr)
	}
	keys, err := service.ListAPIKeys(ctx, merchantID)
	if err != nil || len(keys) == 0 {
		t.Fatalf("initial API key missing: %+v %v", keys, err)
	}
	if _, err = service.SetAdminStatsConsent(ctx, merchantID, staleSession.Token, true, "stats-before-rotation"); err != nil {
		t.Fatal(err)
	}

	newKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(newKey.PublicKey).Hex(), "change-recipient", "rotate-challenge")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.VerifyAdminRecipientChange(ctx, merchantID, changingSession.Token, challenge.ID,
		challenge.Message, signMessage(t, challenge.Message, newKey), "rotate-recipient"); err != nil {
		t.Fatal(err)
	}

	if _, _, err = service.CreateAdminAPIKey(ctx, merchantID, staleSession.Token, "must-not-exist", "stale-create"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session created API key: %v", err)
	}
	if _, err = service.ListAdminAPIKeys(ctx, merchantID, staleSession.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session listed API keys: %v", err)
	}
	if err = service.RevokeAdminAPIKey(ctx, merchantID, staleSession.Token, keys[0].ID, "stale-revoke"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session revoked API key: %v", err)
	}
	if _, err = service.SetAdminStatsConsent(ctx, merchantID, staleSession.Token, false, "stale-stats-consent"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session changed stats consent: %v", err)
	}
	if _, err = service.SetAdminPublicProfileConsent(ctx, merchantID, staleSession.Token, true, "stale-public-consent"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session changed public consent: %v", err)
	}
	if _, err = service.AdminStats(ctx, merchantID, staleSession.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale session read private stats: %v", err)
	}

	keys, err = service.ListAPIKeys(ctx, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].RevokedAt != nil {
		t.Fatalf("stale operations changed API keys: %+v", keys)
	}
}

// TestAPIKeyOperationsRecheckSuspension models a bearer token accepted by HTTP
// middleware immediately before an operator suspends the merchant. Protected
// service work must observe the committed suspension, leave no durable writes,
// and preserve the same key for an explicit later reinstatement.
func TestAPIKeyOperationsRecheckSuspension(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: time.Hour,
		AdminSessionTTL: time.Hour, RecipientCooldown: 0,
		Pepper:         []byte("01234567890123456789012345678901"),
		EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	originalKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	originalAddress := crypto.PubkeyToAddress(originalKey.PublicKey).Hex()
	merchantID, apiKey := activateWithAPIKey(t, service, sender, originalAddress, originalKey)
	keys, err := service.ListAuthenticatedAPIKeys(ctx, merchantID, apiKey)
	if err != nil || len(keys) != 1 {
		t.Fatalf("initial authenticated keys = %+v %v", keys, err)
	}
	newRecipientKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newRecipient := crypto.PubkeyToAddress(newRecipientKey.PublicKey).Hex()
	challenge, err := service.AuthenticatedWalletChallenge(ctx, merchantID, apiKey, newRecipient,
		"change-recipient", "before-suspension-challenge")
	if err != nil {
		t.Fatal(err)
	}
	if principal, authErr := service.Authenticate(ctx, apiKey); authErr != nil || principal.ID != merchantID {
		t.Fatalf("pre-suspension middleware authentication failed: %+v %v", principal, authErr)
	}
	if err = service.Suspend(ctx, merchantID, "security-review", "operator", false, "suspend"); err != nil {
		t.Fatal(err)
	}

	if _, _, err = service.CreateAuthenticatedAPIKey(ctx, merchantID, apiKey, "must-not-exist", "suspended-create"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended merchant created API key: %v", err)
	}
	if _, err = service.ListAuthenticatedAPIKeys(ctx, merchantID, apiKey); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended merchant listed API keys: %v", err)
	}
	if err = service.RevokeAuthenticatedAPIKey(ctx, merchantID, apiKey, keys[0].ID, "suspended-revoke"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended merchant revoked API key: %v", err)
	}
	if _, err = service.AuthenticatedWalletChallenge(ctx, merchantID, apiKey, newRecipient,
		"change-recipient", "suspended-challenge"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended merchant created recipient challenge: %v", err)
	}
	if _, err = service.VerifyAuthenticatedWallet(ctx, merchantID, apiKey, challenge.ID, challenge.Message,
		signMessage(t, challenge.Message, newRecipientKey), "change-recipient", "suspended-verify"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("suspended merchant changed recipient: %v", err)
	}

	var storedRecipient string
	var consumedAt *time.Time
	if err = pool.QueryRow(ctx, `SELECT recipient_address FROM merchants WHERE id=$1`, merchantID).Scan(&storedRecipient); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT consumed_at FROM wallet_verification_challenges WHERE id=$1`, challenge.ID).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	allKeys, err := service.ListAPIKeys(ctx, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(storedRecipient, originalAddress) || consumedAt != nil || len(allKeys) != 1 || allKeys[0].RevokedAt != nil {
		t.Fatalf("suspended operations changed state: recipient=%s consumed=%v keys=%+v", storedRecipient, consumedAt, allKeys)
	}

	if err = service.Suspend(ctx, merchantID, "", "operator", true, "reinstate"); err != nil {
		t.Fatal(err)
	}
	if principal, authErr := service.Authenticate(ctx, apiKey); authErr != nil || principal.ID != merchantID {
		t.Fatalf("preserved API key did not work after reinstatement: %+v %v", principal, authErr)
	}
	if keys, err = service.ListAuthenticatedAPIKeys(ctx, merchantID, apiKey); err != nil || len(keys) != 1 {
		t.Fatalf("reinstated authenticated keys = %+v %v", keys, err)
	}
}

func TestAdminRecipientChangeUsesPostLockProofTime(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: time.Hour,
		AdminSessionTTL: 2 * time.Hour, RecipientCooldown: 0,
		Pepper:         []byte("01234567890123456789012345678901"),
		EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	originalKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	merchantID := activate(t, service, sender, crypto.PubkeyToAddress(originalKey.PublicKey).Hex(), originalKey)
	base := time.Now().UTC().Truncate(time.Microsecond)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	service.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	changingSession := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	clock.Store(base.Add(time.Second).UnixNano())
	otherSession := elevatedAdminSession(t, service, sender, "cooldown@example.com", merchantID, originalKey)
	newKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(newKey.PublicKey).Hex(), "change-recipient", "post-lock-challenge")
	if err != nil {
		t.Fatal(err)
	}
	signature := signMessage(t, challenge.Message, newKey)

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT id FROM merchants WHERE id=$1 FOR UPDATE`, merchantID); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- service.VerifyAdminRecipientChange(ctx, merchantID, changingSession.Token,
			challenge.ID, challenge.Message, signature, "post-lock-verify")
	}()
	waitForChallengeLock(t, pool, challenge.ID)

	// Simulate another browser proving the old wallet after this request began
	// but before it acquired the merchant lock. The change proof must receive a
	// strictly later timestamp so this other session becomes stale.
	otherProof := base.Add(time.Minute)
	if _, err = pool.Exec(ctx, `UPDATE merchant_admin_sessions SET wallet_verified_at=$3
		WHERE merchant_id=$1 AND token_hash=$2`, merchantID, secret.Hash(otherSession.Token), otherProof); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatal(err)
	}
	clock.Store(base.Add(2 * time.Minute).UnixNano())
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatalf("recipient change after lock wait: %v", err)
	}
	principal, err := service.AuthenticateAdmin(ctx, otherSession.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.WalletAuthenticated {
		t.Fatal("old-recipient proof obtained during lock wait remained elevated")
	}

	// Expiry is also evaluated after acquiring the merchant/session locks. A
	// request that waited past its challenge deadline must roll back without
	// consuming the challenge.
	expiredKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(expiredKey.PublicKey).Hex(), "change-recipient", "expiring-challenge")
	if err != nil {
		t.Fatal(err)
	}
	expiringSignature := signMessage(t, expiring.Message, expiredKey)
	blocker, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT id FROM merchants WHERE id=$1 FOR UPDATE`, merchantID); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatal(err)
	}
	result = make(chan error, 1)
	go func() {
		result <- service.VerifyAdminRecipientChange(ctx, merchantID, changingSession.Token,
			expiring.ID, expiring.Message, expiringSignature, "expired-after-lock")
	}()
	waitForChallengeLock(t, pool, expiring.ID)
	clock.Store(expiring.ExpiresAt.Add(time.Second).UnixNano())
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrInvalid) {
		t.Fatalf("challenge expiring during lock wait returned %v", err)
	}
	var consumedAt *time.Time
	if err = pool.QueryRow(ctx, `SELECT consumed_at FROM wallet_verification_challenges WHERE id=$1`, expiring.ID).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if consumedAt != nil {
		t.Fatal("expired challenge was consumed")
	}

	// The API-key recipient-change service path uses the same post-lock proof
	// rule, even though it does not elevate an initiating browser session.
	apiChangeKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	apiChange, err := service.WalletChallenge(ctx, merchantID, crypto.PubkeyToAddress(apiChangeKey.PublicKey).Hex(), "change-recipient", "api-post-lock-challenge")
	if err != nil {
		t.Fatal(err)
	}
	apiSignature := signMessage(t, apiChange.Message, apiChangeKey)
	blocker, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT id FROM merchants WHERE id=$1 FOR UPDATE`, merchantID); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatal(err)
	}
	apiResult := make(chan error, 1)
	go func() {
		_, verifyErr := service.VerifyWallet(ctx, merchantID, apiChange.ID, apiChange.Message,
			apiSignature, "change-recipient", "api-post-lock-verify")
		apiResult <- verifyErr
	}()
	waitForChallengeLock(t, pool, apiChange.ID)
	proofBeforeAPIChange := apiChange.ExpiresAt.Add(-30 * time.Minute)
	if _, err = pool.Exec(ctx, `UPDATE merchant_admin_sessions SET wallet_verified_at=$3
		WHERE merchant_id=$1 AND token_hash=$2`, merchantID, secret.Hash(otherSession.Token), proofBeforeAPIChange); err != nil {
		_ = blocker.Rollback(ctx)
		t.Fatal(err)
	}
	clock.Store(proofBeforeAPIChange.Add(time.Minute).UnixNano())
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-apiResult; err != nil {
		t.Fatalf("API recipient change after lock wait: %v", err)
	}
	principal, err = service.AuthenticateAdmin(ctx, otherSession.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.WalletAuthenticated {
		t.Fatal("old-recipient proof remained elevated after API recipient change")
	}
}

// TestRecipientCooldownIgnoresUnrelatedMerchantWrites pins the cooldown to the
// last recipient proof. Anchoring it to merchants.updated_at let any unrelated
// write, notably operator reinstatement, silently restart the clock.
func TestRecipientCooldownIgnoresUnrelatedMerchantWrites(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	// Suspend writes merchants.updated_at from the database clock, so this case
	// has to advance real time rather than stub the service clock.
	const cooldown = 200 * time.Millisecond
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute, WalletTTL: 3 * time.Hour,
		RecipientCooldown: cooldown,
		Pepper:            []byte("01234567890123456789012345678901"),
		EmailOutboxKey:    bytes.Repeat([]byte{0x42}, 32),
	})
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	merchantID := activate(t, service, sender, crypto.PubkeyToAddress(key.PublicKey).Hex(), key)

	// Move past the cooldown that started at the initial recipient proof.
	time.Sleep(2 * cooldown)

	// An operator round trip rewrites merchants.updated_at to "now".
	if err := service.Suspend(ctx, merchantID, "review", "test-operator", false, "request-5"); err != nil {
		t.Fatal(err)
	}
	if err := service.Suspend(ctx, merchantID, "", "test-operator", true, "request-6"); err != nil {
		t.Fatal(err)
	}

	newKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newAddress := crypto.PubkeyToAddress(newKey.PublicKey).Hex()
	change, err := service.WalletChallenge(ctx, merchantID, newAddress, "change-recipient", "request-7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyWallet(ctx, merchantID, change.ID, change.Message,
		signMessage(t, change.Message, newKey), "change-recipient", "request-8"); err != nil {
		t.Fatalf("reinstatement extended the recipient cooldown: %v", err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT recipient_address FROM merchants WHERE id=$1`, merchantID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(stored, newAddress) {
		t.Fatalf("recipient = %q, want %q", stored, newAddress)
	}
}

func TestOnboardingLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &captureSender{}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute, WalletTTL: 3 * time.Hour,
		RecipientCooldown: time.Hour,
		Pepper:            []byte("01234567890123456789012345678901"),
		EmailOutboxKey:    bytes.Repeat([]byte{0x42}, 32),
	})
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if err := service.Register(ctx, Registration{
		Name: "Test merchant", Email: "merchant@example.com", Recipient: address, AcceptTerms: true,
	}, "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link := strings.TrimPrefix(sender.message.TextBody, "Verify your email: ")
	parsedLink, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	emailToken := parsedLink.Query().Get("token")
	merchantID, err := service.VerifyEmail(ctx, emailToken, "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyEmail(ctx, emailToken, "request-2-replay"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("email token replay returned %v", err)
	}
	challenge, err := service.WalletChallenge(ctx, merchantID, "", "verify-recipient", "request-3")
	if err != nil {
		t.Fatal(err)
	}
	signature := signMessage(t, challenge.Message, key)
	apiKey, err := service.VerifyWallet(ctx, merchantID, challenge.ID, challenge.Message, signature, "verify-recipient", "request-4")
	if err != nil {
		t.Fatal(err)
	}
	if apiKey == "" {
		t.Fatal("initial API key was not returned")
	}
	if _, err := service.VerifyWallet(ctx, merchantID, challenge.ID, challenge.Message, signature, "verify-recipient", "request-5"); err == nil {
		t.Fatal("wallet challenge replay succeeded")
	}
	authenticated, err := service.Authenticate(ctx, apiKey)
	if err != nil || authenticated.ID != merchantID {
		t.Fatalf("API authentication failed: %+v %v", authenticated, err)
	}
	created, raw, err := service.CreateAPIKey(ctx, merchantID, "rotation", "request-6")
	if err != nil || raw == "" {
		t.Fatalf("key rotation failed: %v", err)
	}
	keys, err := service.ListAPIKeys(ctx, merchantID)
	if err != nil || len(keys) != 2 {
		t.Fatalf("unexpected keys: %+v %v", keys, err)
	}
	if err := service.RevokeAPIKey(ctx, merchantID, created.ID, "request-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, raw); err == nil {
		t.Fatal("revoked API key authenticated")
	}
	if err := service.Suspend(ctx, merchantID, "abuse", "test-operator", false, "request-8"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, apiKey); err == nil {
		t.Fatal("suspended merchant authenticated")
	}
	if err := service.Suspend(ctx, merchantID, "", "test-operator", true, "request-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, apiKey); err != nil {
		t.Fatalf("reinstated merchant failed authentication: %v", err)
	}
	newKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newAddress := crypto.PubkeyToAddress(newKey.PublicKey).Hex()
	change, err := service.WalletChallenge(ctx, merchantID, newAddress, "change-recipient", "request-10")
	if err != nil {
		t.Fatal(err)
	}
	changeSignature := signMessage(t, change.Message, newKey)
	if _, err := service.VerifyWallet(ctx, merchantID, change.ID, change.Message, changeSignature, "change-recipient", "request-11-throttled"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("recipient cooldown returned %v", err)
	}
	baseNow := service.now()
	service.now = func() time.Time { return baseNow.Add(2 * time.Hour) }
	if _, err := service.VerifyWallet(ctx, merchantID, change.ID, change.Message, changeSignature, "change-recipient", "request-11"); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Authenticate(ctx, apiKey)
	if err != nil || !strings.EqualFold(updated.Recipient, newAddress) {
		t.Fatalf("recipient change was not applied: %+v %v", updated, err)
	}
	var history int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recipient_address_history WHERE merchant_id=$1`, merchantID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("recipient history rows = %d, want 2", history)
	}
}

func TestEmailOutboxRetriesTransientFailureWithoutFalseCooldown(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &retrySender{failures: 1}
	base := time.Now().UTC().Truncate(time.Microsecond)
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute,
		Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	service.now = func() time.Time { return base }
	registration := Registration{
		Name: "Retry merchant", Email: "retry@example.com",
		Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
	}
	if err := service.Register(ctx, registration, "retry-request"); err != nil {
		t.Fatalf("transient SMTP failure escaped generic registration: %v", err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	var sentAt *time.Time
	var attempts int
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT token.sent_at,outbox.attempts,outbox.token_ciphertext
		FROM email_verification_tokens token
		JOIN email_delivery_outbox outbox ON outbox.token_id=token.id`).Scan(
		&sentAt, &attempts, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if sentAt != nil || attempts != 1 || len(ciphertext) == 0 {
		t.Fatalf("failed delivery state sent_at=%v attempts=%d ciphertext=%d", sentAt, attempts, len(ciphertext))
	}

	// A repeated request while the same live message is pending neither calls
	// SMTP nor creates a second token. The existing item remains retryable.
	if err := service.Register(ctx, registration, "duplicate-request"); err != nil {
		t.Fatal(err)
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_verification_tokens`).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 || len(sender.messages) != 1 {
		t.Fatalf("pending duplicate tokens=%d deliveries=%d, want 1/1", tokenCount, len(sender.messages))
	}

	service.now = func() time.Time { return base.Add(emailRetryMinimum) }
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatalf("delivery attempts=%d, want 2", len(sender.messages))
	}
	var deliveredAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT token.sent_at,outbox.delivered_at,outbox.token_ciphertext
		FROM email_verification_tokens token
		JOIN email_delivery_outbox outbox ON outbox.token_id=token.id`).Scan(
		&sentAt, &deliveredAt, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if sentAt == nil || deliveredAt == nil || ciphertext != nil {
		t.Fatalf("successful delivery sent_at=%v delivered_at=%v ciphertext=%v", sentAt, deliveredAt, ciphertext)
	}

	// Delivery, not the earlier enqueue/failure, starts the cooldown.
	if err := service.Register(ctx, registration, "cooldown-request"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_verification_tokens`).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 {
		t.Fatalf("delivered cooldown created %d tokens, want 1", tokenCount)
	}
}

func TestEmailOutboxNeverDeliversExpiredToken(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &retrySender{failures: 1}
	base := time.Now().UTC().Truncate(time.Microsecond)
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Minute, Resend: time.Minute,
		Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	service.now = func() time.Time { return base }
	if err := service.Register(ctx, Registration{
		Name: "Expiry merchant", Email: "expiry@example.com",
		Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
	}, "expiry-request"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(strings.TrimPrefix(sender.messages[0].TextBody, "Verify your email: "))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(time.Minute) }
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expired token was retried: deliveries=%d", len(sender.messages))
	}
	var abandonedAt *time.Time
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT abandoned_at,token_ciphertext FROM email_delivery_outbox`).Scan(
		&abandonedAt, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if abandonedAt == nil || ciphertext != nil {
		t.Fatalf("expired outbox abandoned_at=%v ciphertext=%v", abandonedAt, ciphertext)
	}
	if _, err := service.VerifyEmail(ctx, link.Query().Get("token"), "expired-verification"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired token verification returned %v", err)
	}
}

func TestAdminLinkFailureIsGenericRetryableAndSanitized(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	sender := &retrySender{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	base := time.Now().UTC().Truncate(time.Microsecond)
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute,
		Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32), Logger: logger,
	})
	service.now = func() time.Time { return base }
	if err := service.Register(ctx, Registration{
		Name: "Admin merchant", Email: "admin@example.com",
		Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
	}, "registration-request"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(2 * time.Minute) }
	sender.failures = 1
	deliveriesBefore := len(sender.messages)
	if err := service.RequestAdminLink(ctx, "admin@example.com", "admin-request-id"); err != nil {
		t.Fatalf("known admin-link request exposed delivery failure: %v", err)
	}
	if len(sender.messages) != deliveriesBefore {
		t.Fatal("known admin-link request performed SMTP inline")
	}
	if err := service.RequestAdminLink(ctx, "absent@example.com", "absent-request-id"); err != nil {
		t.Fatalf("unknown admin-link request differed: %v", err)
	}
	if len(sender.messages) != deliveriesBefore {
		t.Fatal("unknown and known admin-link requests have different inline delivery behavior")
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if !strings.Contains(output, "admin-request-id") {
		t.Fatalf("delivery log lacks request ID: %s", output)
	}
	if strings.Contains(output, "admin@example.com") || strings.Contains(output, "token=") {
		t.Fatalf("delivery log contains sensitive email/token material: %s", output)
	}
	var sentAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT token.sent_at
		FROM email_verification_tokens token
		JOIN email_delivery_outbox outbox ON outbox.token_id=token.id
		WHERE outbox.message_kind='admin_login'`).Scan(&sentAt); err != nil {
		t.Fatal(err)
	}
	if sentAt != nil {
		t.Fatalf("failed admin email started cooldown at %v", sentAt)
	}
	service.now = func() time.Time { return base.Add(2*time.Minute + emailRetryMinimum) }
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT token.sent_at
		FROM email_verification_tokens token
		JOIN email_delivery_outbox outbox ON outbox.token_id=token.id
		WHERE outbox.message_kind='admin_login'`).Scan(&sentAt); err != nil {
		t.Fatal(err)
	}
	if sentAt == nil {
		t.Fatal("admin email retry did not record delivery")
	}
}

func TestStaleEmailWorkerCannotMutateReclaimedDelivery(t *testing.T) {
	for _, test := range []struct {
		name      string
		sendError error
	}{
		{name: "accepted by SMTP"},
		{name: "rejected by SMTP", sendError: errors.New("transient failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			base := time.Now().UTC().Truncate(time.Microsecond)
			sender := &stagedSender{
				started: make(chan int, 2),
				releases: [2]chan struct{}{
					make(chan struct{}), make(chan struct{}),
				},
				errors: [2]error{test.sendError, nil},
			}
			service := New(pool, sender, Config{
				BaseURL: "https://eth402.org", TermsVersion: "test-v1",
				EmailTTL: time.Hour, Resend: time.Minute, EmailDeliveryLease: time.Minute,
				Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
			})
			service.now = func() time.Time { return base }
			if err := service.Register(ctx, Registration{
				Name: "Lease merchant", Email: "lease@example.com",
				Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
			}, "lease-request"); err != nil {
				t.Fatal(err)
			}

			type result struct {
				found bool
				err   error
			}
			done := make(chan result, 1)
			go func() {
				found, err := service.deliverNext(ctx, "")
				done <- result{found: found, err: err}
			}()
			select {
			case call := <-sender.started:
				if call != 0 {
					t.Fatalf("first sender call = %d", call)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delivery did not reach sender")
			}

			var oldClaim string
			if err := pool.QueryRow(ctx, `SELECT claim_token FROM email_delivery_outbox`).Scan(&oldClaim); err != nil {
				t.Fatal(err)
			}
			// Expire the first lease, then let the real claim query reclaim it and
			// install a fresh ownership token for worker two.
			if _, err := pool.Exec(ctx, `UPDATE email_delivery_outbox SET claimed_until=$1`, base.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			secondDone := make(chan result, 1)
			go func() {
				found, err := service.deliverNext(ctx, "")
				secondDone <- result{found: found, err: err}
			}()
			select {
			case call := <-sender.started:
				if call != 1 {
					t.Fatalf("second sender call = %d", call)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reclaiming worker did not reach sender")
			}
			var newClaim string
			if err := pool.QueryRow(ctx, `SELECT claim_token FROM email_delivery_outbox`).Scan(&newClaim); err != nil {
				t.Fatal(err)
			}
			if oldClaim == newClaim {
				t.Fatal("reclaim did not replace ownership token")
			}
			close(sender.releases[0])
			select {
			case result := <-done:
				if !result.found || result.err != nil {
					t.Fatalf("stale worker result found=%v err=%v", result.found, result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stale delivery worker did not finish")
			}

			var currentClaim string
			var deliveredAt, sentAt *time.Time
			if err := pool.QueryRow(ctx, `SELECT outbox.claim_token,outbox.delivered_at,token.sent_at
				FROM email_delivery_outbox outbox
				JOIN email_verification_tokens token ON token.id=outbox.token_id`).Scan(
				&currentClaim, &deliveredAt, &sentAt); err != nil {
				t.Fatal(err)
			}
			if currentClaim != newClaim || deliveredAt != nil || sentAt != nil {
				t.Fatalf("stale worker mutated reclaimed row claim=%s delivered=%v sent=%v",
					currentClaim, deliveredAt, sentAt)
			}

			close(sender.releases[1])
			select {
			case result := <-secondDone:
				if !result.found || result.err != nil {
					t.Fatalf("current worker result found=%v err=%v", result.found, result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("current delivery worker did not finish")
			}
			if err := pool.QueryRow(ctx, `SELECT outbox.delivered_at,token.sent_at
				FROM email_delivery_outbox outbox
				JOIN email_verification_tokens token ON token.id=outbox.token_id`).Scan(
				&deliveredAt, &sentAt); err != nil {
				t.Fatal(err)
			}
			if deliveredAt == nil || sentAt == nil {
				t.Fatalf("current worker did not finalize delivery delivered=%v sent=%v", deliveredAt, sentAt)
			}
		})
	}
}

func TestEmailExpiryCleanupDoesNotInvalidateActiveDeliveryLease(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	sender := &stagedSender{
		started: make(chan int, 1),
		releases: [2]chan struct{}{
			make(chan struct{}), make(chan struct{}),
		},
	}
	service := New(pool, sender, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Minute, Resend: time.Minute, EmailDeliveryLease: 2 * time.Minute,
		Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32),
	})
	service.now = func() time.Time { return base }
	if err := service.Register(ctx, Registration{
		Name: "Expiry lease merchant", Email: "expiry-lease@example.com",
		Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
	}, "expiry-lease-request"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := service.deliverNext(ctx, "")
		done <- err
	}()
	select {
	case <-sender.started:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not acquire its lease")
	}

	// The token expires while SMTP is in flight. Cleanup must leave the live
	// owner's claim and ciphertext alone; only an expired/reclaimable lease may
	// be abandoned by another worker.
	service.now = func() time.Time { return base.Add(time.Minute) }
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	var claim *string
	var abandonedAt *time.Time
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT claim_token::text,abandoned_at,token_ciphertext
		FROM email_delivery_outbox`).Scan(&claim, &abandonedAt, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if claim == nil || abandonedAt != nil || len(ciphertext) == 0 {
		t.Fatalf("active delivery was invalidated claim=%v abandoned=%v ciphertext=%d", claim, abandonedAt, len(ciphertext))
	}

	close(sender.releases[0])
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active delivery did not finish")
	}
}

func TestUnreadableEmailPayloadIsPermanentlyAbandoned(t *testing.T) {
	for _, test := range []struct {
		name      string
		wrongKey  bool
		tamperAAD bool
	}{
		{name: "wrong key", wrongKey: true},
		{name: "corrupt associated data", tamperAAD: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()
			base := time.Now().UTC().Truncate(time.Microsecond)
			key := bytes.Repeat([]byte{0x42}, 32)
			enqueue := New(pool, &retrySender{}, Config{
				BaseURL: "https://eth402.org", TermsVersion: "test-v1",
				EmailTTL: time.Hour, Resend: time.Minute,
				Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: key,
			})
			enqueue.now = func() time.Time { return base }
			if err := enqueue.Register(ctx, Registration{
				Name: "Corruption merchant", Email: "corruption@example.com",
				Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
			}, "corruption-request"); err != nil {
				t.Fatal(err)
			}
			if test.tamperAAD {
				if _, err := pool.Exec(ctx, `UPDATE email_delivery_outbox SET message_kind='admin_login'`); err != nil {
					t.Fatal(err)
				}
			}
			if test.wrongKey {
				key = bytes.Repeat([]byte{0x43}, 32)
			}
			var logs bytes.Buffer
			observer := &captureEmailObserver{}
			sender := &retrySender{}
			delivery := New(pool, sender, Config{
				BaseURL: "https://eth402.org", TermsVersion: "test-v1",
				EmailTTL: time.Hour, Resend: time.Minute,
				Pepper: []byte("01234567890123456789012345678901"), EmailOutboxKey: key,
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)), EmailObserver: observer,
			})
			delivery.now = func() time.Time { return base }
			processed, err := delivery.DeliverPendingEmail(ctx)
			if err != nil || processed != 1 {
				t.Fatalf("corrupt delivery processed=%d err=%v", processed, err)
			}
			var abandonedAt *time.Time
			var ciphertext []byte
			var claim *string
			var sentAt *time.Time
			if err := pool.QueryRow(ctx, `SELECT outbox.abandoned_at,outbox.token_ciphertext,
				outbox.claim_token::text,token.sent_at
				FROM email_delivery_outbox outbox
				JOIN email_verification_tokens token ON token.id=outbox.token_id`).Scan(
				&abandonedAt, &ciphertext, &claim, &sentAt); err != nil {
				t.Fatal(err)
			}
			if abandonedAt == nil || ciphertext != nil || claim != nil || sentAt != nil {
				t.Fatalf("corrupt payload state abandoned=%v ciphertext=%v claim=%v sent=%v",
					abandonedAt, ciphertext, claim, sentAt)
			}
			if observer.failures != 1 || len(sender.messages) != 0 {
				t.Fatalf("failure observations=%d SMTP calls=%d, want 1/0", observer.failures, len(sender.messages))
			}
			output := logs.String()
			if !strings.Contains(output, "payload_authentication_failed") ||
				!strings.Contains(output, "corruption-request") {
				t.Fatalf("sanitized corruption log lacks reason/request ID: %s", output)
			}
			if strings.Contains(output, "corruption@example.com") || strings.Contains(output, "token=") {
				t.Fatalf("corruption log leaked sensitive material: %s", output)
			}
			processed, err = delivery.DeliverPendingEmail(ctx)
			if err != nil || processed != 0 || observer.failures != 1 {
				t.Fatalf("abandoned payload retried processed=%d failures=%d err=%v",
					processed, observer.failures, err)
			}
		})
	}
}

func TestEmailOutboxObservationReportsAggregateBacklog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	observer := &captureEmailObserver{}
	service := New(pool, &retrySender{}, Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute,
		Pepper:         []byte("01234567890123456789012345678901"),
		EmailOutboxKey: bytes.Repeat([]byte{0x42}, 32), EmailObserver: observer,
	})
	service.now = func() time.Time { return base }
	if err := service.Register(ctx, Registration{
		Name: "Observed merchant", Email: "observed@example.com",
		Recipient: "0x1111111111111111111111111111111111111111", AcceptTerms: true,
	}, "observation-request"); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(30 * time.Second) }
	if err := service.observeEmailOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if observer.pending != 1 || observer.oldest != 30*time.Second || !observer.tick.Equal(base.Add(30*time.Second)) {
		t.Fatalf("pending observation count=%d oldest=%s tick=%s",
			observer.pending, observer.oldest, observer.tick)
	}
	if _, err := service.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.observeEmailOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if observer.pending != 0 || observer.oldest != 0 {
		t.Fatalf("drained observation count=%d oldest=%s", observer.pending, observer.oldest)
	}
}

func signMessage(t *testing.T, message string, key *ecdsa.PrivateKey) string {
	t.Helper()
	parsed, err := siwe.ParseMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(parsed.EIP191Hash().Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	return "0x" + hex.EncodeToString(signature)
}
