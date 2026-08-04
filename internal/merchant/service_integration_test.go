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
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
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
	if _, err := service.VerifyWallet(ctx, merchantID, challenge.ID, challenge.Message,
		signMessage(t, challenge.Message, key), "verify-recipient", "request-4"); err != nil {
		t.Fatal(err)
	}
	return merchantID
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
