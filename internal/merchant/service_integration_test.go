//go:build integration

package merchant

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
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
