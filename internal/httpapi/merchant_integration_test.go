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
		EmailTTL: time.Hour, Resend: time.Nanosecond, WalletTTL: 10 * time.Minute,
		AdminSessionTTL: time.Hour, PaymentRetention: 30 * 24 * time.Hour,
		PublicDirectoryTTL: time.Nanosecond,
		Pepper:             []byte("01234567890123456789012345678901"),
		EmailOutboxKey:     []byte("12345678901234567890123456789012"),
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
	if _, err := merchantService.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
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

	// The onboarding email links to GET /verify-email. GET must only render an
	// explicit confirmation step: mail scanners and previewers follow links and
	// must not consume the one-time token.
	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/register", "", map[string]any{
		"name": "Browser merchant", "business_email": "browser@example.com",
		"recipient_address": address, "accept_terms": true,
	})
	if _, err := merchantService.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(page.Body.String(), "Confirm your email") {
		t.Fatalf("verification page omitted confirmation: %s", page.Body.String())
	}
	// A second GET still succeeds, proving the first did not consume the token.
	if page = requestJSON(t, handler, http.MethodGet, link.Path+"?"+link.RawQuery, "", nil); page.Code != http.StatusOK {
		t.Fatalf("second GET consumed token: %d", page.Code)
	}
	page = requestForm(t, handler, link.Path, url.Values{"token": {link.Query().Get("token")}})
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Email verified") {
		t.Fatalf("browser verification POST: %d %s", page.Code, page.Body.String())
	}
	adminCookie := page.Result().Cookies()[0]
	if adminCookie.Name != merchantAdminCookie || adminCookie.Value == "" ||
		!adminCookie.HttpOnly || adminCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("admin cookie is not hardened: %+v", adminCookie)
	}
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/session", adminCookie, nil)
	var browserSession struct {
		Merchant merchant.Merchant `json:"merchant"`
	}
	decodeResponse(t, response, http.StatusOK, &browserSession)
	if browserSession.Merchant.Email != "browser@example.com" || browserSession.Merchant.Status != "pending" {
		t.Fatalf("unexpected browser session: %+v", browserSession.Merchant)
	}
	response = requestAdminJSON(t, handler, http.MethodPost, "/merchant/api/wallet-challenge", adminCookie, map[string]any{})
	var browserChallenge merchant.Challenge
	decodeResponse(t, response, http.StatusCreated, &browserChallenge)
	response = requestAdminJSON(t, handler, http.MethodPost, "/merchant/api/verify-wallet", adminCookie, map[string]string{
		"challenge_id": browserChallenge.ID, "message": browserChallenge.Message,
		"signature": signHTTPMessage(t, browserChallenge.Message, key),
	})
	var browserActivated struct {
		APIKey string `json:"api_key"`
	}
	decodeResponse(t, response, http.StatusOK, &browserActivated)
	if browserActivated.APIKey == "" {
		t.Fatal("panel activation omitted one-time API key")
	}
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/stats", adminCookie, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("stats available without opt-in: %d %s", response.Code, response.Body.String())
	}
	insertMerchantPayment := func(identityChar, nonceChar string, amount int, createdAt time.Time) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO payment_records
			(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,
			 recipient_address,amount_atomic,authorization_nonce,valid_after,valid_before,
			 payload_hash,verification_status,state,confirmed_at,created_at,updated_at)
			VALUES ($1,$2,2,'exact','eip155:1',
			 '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
			 '0x2222222222222222222222222222222222222222',
			 lower($3),$4,$5,now()-interval '2 minutes',now()+interval '1 hour',
			 $6,'verified','confirmed',$7,$7,$7)`,
			"pay_"+strings.Repeat(identityChar, 64), browserSession.Merchant.ID, address,
			amount, "0x"+strings.Repeat(nonceChar, 64), strings.Repeat(identityChar, 64), createdAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertMerchantPayment("a", "b", 999, time.Now().Add(-time.Minute))
	response = requestAdminJSON(t, handler, http.MethodPut, "/merchant/api/stats-consent", adminCookie, map[string]bool{"enabled": true})
	if response.Code != http.StatusOK {
		t.Fatalf("stats opt-in: %d %s", response.Code, response.Body.String())
	}
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/stats", adminCookie, nil)
	var merchantStats merchant.MerchantStats
	decodeResponse(t, response, http.StatusOK, &merchantStats)
	if merchantStats.ConfirmedVolumeAtomic != "0" || merchantStats.ConfirmedVolumeUSDC != "0.000000" {
		t.Fatalf("pre-opt-in payment leaked into merchant stats: %+v", merchantStats)
	}
	insertMerchantPayment("c", "d", 123, time.Now())
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/stats", adminCookie, nil)
	decodeResponse(t, response, http.StatusOK, &merchantStats)
	if merchantStats.ConfirmedSettlements != 1 || merchantStats.ConfirmedVolumeAtomic != "123" ||
		merchantStats.ConfirmedVolumeUSDC != "0.000123" {
		t.Fatalf("post-opt-in payment missing from merchant stats: %+v", merchantStats)
	}

	// Public discovery is a separate wallet-authorized consent. Private analytics
	// neither publishes the merchant nor backfills activity into the leaderboard.
	publicMerchants, err := merchantService.PublicLeaderboard(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicMerchants) != 0 {
		t.Fatalf("private analytics silently published merchant: %+v", publicMerchants)
	}
	response = requestAdminJSON(t, handler, http.MethodPut, "/merchant/api/public-profile", adminCookie, map[string]bool{"enabled": true})
	if response.Code != http.StatusOK {
		t.Fatalf("public profile opt-in: %d %s", response.Code, response.Body.String())
	}
	publicMerchants, err = merchantService.PublicLeaderboard(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicMerchants) != 1 || publicMerchants[0].ConfirmedSettlements != 0 ||
		publicMerchants[0].Name != "Browser merchant" {
		t.Fatalf("public profile leaked pre-consent activity: %+v", publicMerchants)
	}
	insertMerchantPayment("e", "f", 456, time.Now())
	publicMerchants, err = merchantService.PublicLeaderboard(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicMerchants) != 1 || publicMerchants[0].ConfirmedSettlements != 1 {
		t.Fatalf("public profile omitted post-consent settlement: %+v", publicMerchants)
	}
	page = requestJSON(t, handler, http.MethodGet, "/explore", "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Browser merchant") {
		t.Fatalf("network page omitted opted-in merchant: %d %s", page.Code, page.Body.String())
	}
	for _, privateValue := range []string{"browser@example.com", strings.ToLower(address), "0.000456"} {
		if strings.Contains(page.Body.String(), privateValue) {
			t.Fatalf("network page disclosed private value %q", privateValue)
		}
	}

	// A later email sign-in is intentionally not enough for sensitive panel
	// operations. The registered recipient must elevate each new session.
	response = requestJSON(t, handler, http.MethodPost, "/v1/merchants/admin-link", "", map[string]string{
		"business_email": "browser@example.com",
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("admin link: %d %s", response.Code, response.Body.String())
	}
	if _, err := merchantService.DeliverPendingEmail(ctx); err != nil {
		t.Fatal(err)
	}
	link, err = url.Parse(strings.TrimPrefix(sender.message.TextBody, "Sign in to your ETH402 merchant panel: "))
	if err != nil {
		t.Fatal(err)
	}
	page = requestForm(t, handler, link.Path, url.Values{"token": {link.Query().Get("token")}})
	if page.Code != http.StatusOK {
		t.Fatalf("admin email verification: %d %s", page.Code, page.Body.String())
	}
	secondCookie := page.Result().Cookies()[0]
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/session", secondCookie, nil)
	var signedIn struct {
		WalletAuthenticated bool `json:"wallet_authenticated"`
	}
	decodeResponse(t, response, http.StatusOK, &signedIn)
	if signedIn.WalletAuthenticated {
		t.Fatal("email-only session was wallet-authenticated")
	}
	response = requestAdminJSON(t, handler, http.MethodPut, "/merchant/api/public-profile", secondCookie, map[string]bool{"enabled": false})
	if response.Code != http.StatusForbidden {
		t.Fatalf("email-only session changed public profile consent: %d", response.Code)
	}
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/api-keys", secondCookie, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("email-only session accessed keys: %d", response.Code)
	}
	response = requestAdminJSON(t, handler, http.MethodPost, "/merchant/api/wallet-challenge", secondCookie, map[string]any{})
	var adminChallenge merchant.Challenge
	decodeResponse(t, response, http.StatusCreated, &adminChallenge)
	if adminChallenge.Action != "authenticate-admin" {
		t.Fatalf("admin challenge action = %q", adminChallenge.Action)
	}
	response = requestAdminJSON(t, handler, http.MethodPost, "/merchant/api/verify-wallet", secondCookie, map[string]string{
		"challenge_id": adminChallenge.ID, "message": adminChallenge.Message,
		"signature": signHTTPMessage(t, adminChallenge.Message, key),
	})
	var authenticated map[string]string
	decodeResponse(t, response, http.StatusOK, &authenticated)
	if authenticated["status"] != "authenticated" || authenticated["api_key"] != "" {
		t.Fatalf("admin authentication response = %+v", authenticated)
	}
	response = requestAdminJSON(t, handler, http.MethodGet, "/merchant/api/api-keys", secondCookie, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("wallet-authenticated session could not access keys: %d %s", response.Code, response.Body.String())
	}
	// Tokens remain single-use, and malformed links get the same generic page.
	if page = requestForm(t, handler, link.Path, url.Values{"token": {link.Query().Get("token")}}); page.Code != http.StatusBadRequest {
		t.Fatalf("reused token: %d", page.Code)
	}
	if page = requestJSON(t, handler, http.MethodGet, "/verify-email?token=wrong", "", nil); page.Code != http.StatusBadRequest {
		t.Fatalf("garbage token: %d", page.Code)
	}
}

func requestAdminJSON(t *testing.T, handler http.Handler, method, path string, cookie *http.Cookie, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if value != nil {
		if err := json.NewEncoder(&body).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func requestForm(t *testing.T, handler http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
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
