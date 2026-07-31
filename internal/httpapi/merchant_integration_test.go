//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/migrations"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	siwe "github.com/signinwithethereum/siwe-go"
)

type httpCaptureSender struct{ message email.Message }

func (s *httpCaptureSender) Send(_ context.Context, message email.Message) error {
	s.message = message
	return nil
}

func TestMerchantHTTPOnboarding(t *testing.T) {
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
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lock, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(402001)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(402001)`)
		lock.Release()
	}()
	if _, err := pool.Exec(ctx, "TRUNCATE merchants CASCADE"); err != nil {
		t.Fatal(err)
	}

	sender := &httpCaptureSender{}
	merchantService := merchant.New(pool, sender, merchant.Config{
		BaseURL: "https://eth402.org", TermsVersion: "test-v1",
		EmailTTL: time.Hour, Resend: time.Minute, WalletTTL: 10 * time.Minute,
		Pepper: []byte("01234567890123456789012345678901"),
	})
	registry := metrics.New()
	handler := New(Dependencies{
		Logger: slog.Default(), Database: pool, Ethereum: fakeRPC{chain: 1},
		Stats: stats.NewService(stats.Config{Source: statsSource{}, Started: time.Now()}), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100, RegistrationRate: 10,
		Merchant: merchantService, AllowedOrigin: "https://eth402.org",
	}).Handler()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	response := requestJSON(t, handler, http.MethodPost, "/v1/merchants/register", "", map[string]any{
		"name": "HTTP merchant", "business_email": "merchant@example.com",
		"recipient_address": address, "accept_terms": true,
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("registration: %d %s", response.Code, response.Body.String())
	}
	link, err := url.Parse(strings.TrimPrefix(sender.message.TextBody, "Verify your email: "))
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/verify-email", "", map[string]string{
		"token": link.Query().Get("token"),
	})
	var verified struct {
		MerchantID string `json:"merchant_id"`
	}
	decodeResponse(t, response, http.StatusOK, &verified)

	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/wallet-challenge", "", map[string]string{
		"merchant_id": verified.MerchantID,
	})
	var challenge merchant.Challenge
	decodeResponse(t, response, http.StatusCreated, &challenge)
	signature := signHTTPMessage(t, challenge.Message, key)
	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/verify-wallet", "", map[string]string{
		"merchant_id": verified.MerchantID, "challenge_id": challenge.ID,
		"message": challenge.Message, "signature": signature,
	})
	var activated struct {
		APIKey string `json:"api_key"`
	}
	decodeResponse(t, response, http.StatusOK, &activated)
	if activated.APIKey == "" {
		t.Fatal("activation omitted API key")
	}

	response = requestJSON(t, handler, http.MethodGet, "/v1/me", activated.APIKey, nil)
	var got merchant.Merchant
	decodeResponse(t, response, http.StatusOK, &got)
	if got.ID != verified.MerchantID || !strings.EqualFold(got.Recipient, address) {
		t.Fatalf("unexpected merchant: %+v", got)
	}
	response = requestJSON(t, handler, http.MethodPost, "/v1/api-keys", activated.APIKey, map[string]string{"name": "automation"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", response.Code, response.Body.String())
	}
	response = requestJSON(t, handler, http.MethodPost, "/v1/api-keys", activated.APIKey, map[string]any{
		"name": "bad", "unknown": true,
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field returned %d", response.Code)
	}

	// The onboarding email links to GET /verify-email; a browser must be able to
	// consume the token there and receive a page, not a 404.
	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/register", "", map[string]any{
		"name": "Browser merchant", "business_email": "browser@example.com",
		"recipient_address": address, "accept_terms": true,
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("second registration: %d %s", response.Code, response.Body.String())
	}
	link, err = url.Parse(strings.TrimPrefix(sender.message.TextBody, "Verify your email: "))
	if err != nil {
		t.Fatal(err)
	}
	page := requestJSON(t, handler, http.MethodGet, link.Path+"?"+link.RawQuery, "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET verification link: %d %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "Email verified") {
		t.Fatalf("verification page did not confirm: %s", page.Body.String())
	}
	// Tokens are single-use, and a malformed one gets the same generic page.
	if page = requestJSON(t, handler, http.MethodGet, link.Path+"?"+link.RawQuery, "", nil); page.Code != http.StatusBadRequest {
		t.Fatalf("reused token: %d", page.Code)
	}
	if page = requestJSON(t, handler, http.MethodGet, "/verify-email?token=wrong", "", nil); page.Code != http.StatusBadRequest {
		t.Fatalf("garbage token: %d", page.Code)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path, apiKey string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if value != nil {
		if err := json.NewEncoder(&body).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.RemoteAddr = "127.0.0.1:12345"
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, status int, target any) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("HTTP response: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatal(err)
	}
}

func signHTTPMessage(t *testing.T, message string, key *ecdsa.PrivateKey) string {
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
