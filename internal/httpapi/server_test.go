package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/verification"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	"github.com/x402-foundation/x402/go/v2/types"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

type fakeRPC struct {
	chain uint64
	err   error
}

func (f fakeRPC) ChainID(context.Context) (uint64, error)     { return f.chain, f.err }
func (f fakeRPC) BlockNumber(context.Context) (uint64, error) { return 0, f.err }

type statsSource struct{}

func (statsSource) AggregateStats(context.Context) (stats.Aggregate, error) {
	return stats.Aggregate{}, nil
}

func testServer(dbErr, rpcErr error, chain uint64) http.Handler {
	registry := metrics.New()
	return New(Dependencies{
		Logger: slog.Default(), Database: fakeDB{dbErr}, Ethereum: fakeRPC{chain, rpcErr},
		Stats: stats.NewService(stats.Config{
			Source: statsSource{}, Started: time.Now(),
			// Wired with the same assessor production uses, so a test asserting the
			// status page reports an outage exercises the real derivation rather than
			// a nil health source that would report "unknown" whatever happened.
			Health: stats.NewAssessor(stats.AssessorConfig{
				Database: fakeDB{dbErr}, Chain: fakeRPC{chain, rpcErr}, ExpectedChainID: 1,
				Heartbeats: registry, ExpectedWorkers: []string{"broadcast"},
				StaleAfter: time.Minute, SettlementEnabled: false,
			}),
		}), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100,
	}).Handler()
}

func TestDecodeStrictAcceptsValidJSONValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		body        string
		destination any
	}{
		{
			name: "object",
			body: `{"name":"merchant","nested":{"value":7},"items":[{"id":1},{"id":2}]}`,
			destination: &struct {
				Name   string `json:"name"`
				Nested struct {
					Value int `json:"value"`
				} `json:"nested"`
				Items []map[string]int `json:"items"`
			}{},
		},
		{name: "array", body: `[1,2,3]`, destination: &[]int{}},
		{name: "scalar", body: ` "merchant" `, destination: new(string)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			if err := DecodeStrict(recorder, request, test.destination); err != nil {
				t.Fatalf("DecodeStrict() error = %v", err)
			}
		})
	}
}

func TestDecodeStrictRejectsAmbiguousOrMalformedJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		body          string
		errorContains string
	}{
		{name: "duplicate top-level key", body: `{"name":"first","name":"second"}`, errorContains: "duplicate JSON object key"},
		{name: "duplicate nested key", body: `{"nested":{"value":1,"value":2}}`, errorContains: "duplicate JSON object key"},
		{name: "duplicate key inside array", body: `{"items":[{"id":1,"id":2}]}`, errorContains: "duplicate JSON object key"},
		{name: "escaped-equivalent key", body: `{"name":"first","na\u006de":"second"}`, errorContains: "duplicate JSON object key"},
		{name: "nested escaped-equivalent key", body: `{"nested":{"value":1,"val\u0075e":2}}`, errorContains: "duplicate JSON object key"},
		{name: "invalid UTF-8", body: string([]byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'}), errorContains: "not valid UTF-8"},
		{name: "trailing junk", body: `{"name":"merchant"} garbage`, errorContains: "invalid trailing JSON content"},
		{name: "second JSON value", body: `{"name":"merchant"} {"name":"other"}`, errorContains: "exactly one JSON value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := struct {
				Name   string           `json:"name"`
				Nested map[string]int   `json:"nested"`
				Items  []map[string]int `json:"items"`
			}{}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			err := DecodeStrict(recorder, request, &destination)
			if err == nil {
				t.Fatal("DecodeStrict() accepted ambiguous or malformed JSON")
			}
			if !strings.Contains(err.Error(), test.errorContains) {
				t.Fatalf("DecodeStrict() error = %q, want substring %q", err, test.errorContains)
			}
			if destination.Name != "" || destination.Nested != nil || destination.Items != nil {
				t.Fatalf("destination mutated before rejection: %#v", destination)
			}
		})
	}
}

func TestDecodeStrictPreservesRequestBodyLimit(t *testing.T) {
	t.Parallel()
	body := `{"name":"` + strings.Repeat("x", maxRequestBody) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	err := DecodeStrict(httptest.NewRecorder(), request, &struct {
		Name string `json:"name"`
	}{})
	var maxBytesError *http.MaxBytesError
	if !errors.As(err, &maxBytesError) {
		t.Fatalf("DecodeStrict() error = %v, want *http.MaxBytesError", err)
	}
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()
	handler := testServer(nil, nil, 1)
	for _, path := range []string{"/health/live", "/health/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
	}
}

func TestReadinessFailure(t *testing.T) {
	t.Parallel()
	handler := testServer(errors.New("down"), nil, 1)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestStatsSchema(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "service", "version", "network", "asset", "status"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing stable field %q", key)
		}
	}
	if got["schema_version"] != stats.SchemaVersion {
		t.Errorf("schema_version = %v, want %s", got["schema_version"], stats.SchemaVersion)
	}
	// Schema 2 withholds the volume figures unless the operator opts in. They are
	// absent rather than zero, because a published zero would be a false statement
	// about settled volume rather than a refusal to state it.
	for _, key := range []string{"total_payment_volume_atomic", "total_payment_volume_usdc", "volume_last_24h_atomic"} {
		if _, ok := got[key]; ok {
			t.Errorf("%q must be withheld unless ETH402_PUBLISH_STATS_VOLUME is set", key)
		}
	}
}

// TestStatsPublishesVolumeWhenOptedIn is the other half of the contract: the
// operator can still publish business figures, and asking for them must work.
func TestStatsPublishesVolumeWhenOptedIn(t *testing.T) {
	t.Parallel()
	service := stats.NewService(stats.Config{
		Source: statsSource{}, Started: time.Now(), PublishVolume: true,
	})
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalPaymentVolumeAtomic == "" || snapshot.TotalPaymentVolumeUSDC == "" {
		t.Errorf("opting in must publish volume, got %+v", snapshot)
	}
}

func TestWrongRPCChainNotReady(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 8453).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("wrong chain returned %d", rec.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	t.Parallel()
	handler := newRateLimiter(1, nil).middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for attempt, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/limited", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("attempt %d returned %d, want %d", attempt+1, recorder.Code, want)
		}
	}
}

// mustPrefixes builds a trusted-proxy list for tests.
func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func TestRateLimiterUsesForwardedClientBehindTrustedProxy(t *testing.T) {
	t.Parallel()
	limiter := newRateLimiter(1, mustPrefixes(t, "10.0.0.0/8"))
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// Two distinct clients arriving through the same proxy must not share a
	// bucket, which is what keyed the limiter on the proxy address before.
	for _, client := range []string{"192.0.2.10", "192.0.2.11"} {
		request := httptest.NewRequest(http.MethodGet, "/limited", nil)
		request.RemoteAddr = "10.1.2.3:4567"
		request.Header.Set("X-Forwarded-For", client)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("client %s returned %d, want %d", client, recorder.Code, http.StatusNoContent)
		}
	}
	// The same client must still be limited on its second request.
	request := httptest.NewRequest(http.MethodGet, "/limited", nil)
	request.RemoteAddr = "10.1.2.3:4567"
	request.Header.Set("X-Forwarded-For", "192.0.2.10")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("repeat client returned %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimiterIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	t.Parallel()
	limiter := newRateLimiter(1, mustPrefixes(t, "10.0.0.0/8"))
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// A directly-connected client must not escape its bucket by rotating the
	// header, so the second request is limited despite a fresh value.
	for attempt, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/limited", nil)
		request.RemoteAddr = "198.51.100.7:9999"
		request.Header.Set("X-Forwarded-For", "192.0.2."+strconv.Itoa(attempt+1))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("attempt %d returned %d, want %d", attempt+1, recorder.Code, want)
		}
	}
}

func TestRateLimiterUsesRightmostUntrustedForwardedEntry(t *testing.T) {
	t.Parallel()
	limiter := newRateLimiter(10, mustPrefixes(t, "10.0.0.0/8"))
	request := httptest.NewRequest(http.MethodGet, "/limited", nil)
	request.RemoteAddr = "10.1.2.3:4567"
	// Everything left of the proxy-appended address is attacker controlled.
	request.Header.Set("X-Forwarded-For", "203.0.113.9, garbage, 192.0.2.10")
	if got := limiter.clientAddress(request); got != "192.0.2.10" {
		t.Fatalf("client address = %q, want 192.0.2.10", got)
	}
}

func TestRateLimiterGroupsIPv6BySlash64(t *testing.T) {
	t.Parallel()
	limiter := newRateLimiter(1, nil)
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for attempt, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/limited", nil)
		request.RemoteAddr = "[2001:db8::" + strconv.Itoa(attempt+1) + "]:443"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("address %d returned %d, want %d", attempt+1, recorder.Code, want)
		}
	}
}

func TestMetricsEndpointGatedByConfiguration(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		handler := New(Dependencies{
			Logger: slog.Default(), Database: fakeDB{}, Ethereum: fakeRPC{chain: 1},
			Stats: stats.NewService(stats.Config{Source: statsSource{}, Started: time.Now()}), Metrics: metrics.New(),
			ExpectedChainID: 1, PublicRatePerMinute: 100, MetricsEnabled: enabled,
		}).Handler()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		want := http.StatusOK
		if !enabled {
			want = http.StatusNotFound
		}
		if recorder.Code != want {
			t.Fatalf("metrics enabled=%v returned %d, want %d", enabled, recorder.Code, want)
		}
	}
}

func TestKnownRouteCollapsesIdentifiers(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/v1/api-keys": "/v1/api-keys",
		"/v1/api-keys/7c9e6679-7425-40de-944b-e07fc1f90ae7": "/v1/api-keys/{id}",
		"/v1/admin/merchants/abc/suspend":                   "/v1/admin/merchants/{id}/suspend",
		"/v1/admin/merchants/abc/reinstate":                 "/v1/admin/merchants/{id}/reinstate",
		"/v1/admin/merchants/abc/delete":                    "unknown",
		"/v1/api-keys/abc/extra":                            "unknown",
		"/nonsense":                                         "unknown",
	}
	for path, want := range cases {
		if got := knownRoute(path); got != want {
			t.Fatalf("knownRoute(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCORSDeniedByDefault(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/stats", nil)
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request returned %d", recorder.Code)
	}
}

func TestCrossOriginEmailVerificationGETRendersWithoutCORSGrant(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/verify-email?token=not-consumed-by-get", nil)
	request.Header.Set("Origin", "https://webmail.example")
	recorder := httptest.NewRecorder()
	secureHeaders("https://api.eth402.org", next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cross-origin verification navigation returned %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin received CORS read permission %q", got)
	}
}

func TestCrossOriginEmailVerificationPOSTRemainsDenied(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("cross-origin token-consuming POST reached handler")
	})
	request := httptest.NewRequest(http.MethodPost, "/verify-email", nil)
	request.Header.Set("Origin", "https://webmail.example")
	recorder := httptest.NewRecorder()
	secureHeaders("https://api.eth402.org", next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin verification POST returned %d", recorder.Code)
	}
}

func TestUserActivatedCrossOriginEmailVerificationPOSTRendersWithoutCORSGrant(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodPost, "/verify-email", nil)
	request.Header.Set("Origin", "https://webmail.example")
	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Dest", "document")
	request.Header.Set("Sec-Fetch-User", "?1")
	recorder := httptest.NewRecorder()
	secureHeaders("https://api.eth402.org", next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("user-activated cross-origin verification POST returned %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin received CORS read permission %q", got)
	}
}

func TestSupportedEndpoint(t *testing.T) {
	t.Parallel()
	const signerAddress = "0xc6927a70468bd4ea24ca4beb7ff433122b877383"
	for _, test := range []struct {
		name    string
		signer  string
		wantLen int
	}{
		{name: "settlement enabled", signer: signerAddress, wantLen: 1},
		{name: "settlement disabled", wantLen: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server := New(Dependencies{
				Logger: slog.Default(), Metrics: metrics.New(), PublicRatePerMinute: 100,
				SettlementSignerAddress: test.signer,
			})
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/supported", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			var response types.SupportedResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Kinds) != 1 || response.Kinds[0].Network != config.MainnetNetwork {
				t.Fatalf("response = %#v", response)
			}
			if len(response.Signers) != test.wantLen {
				t.Fatalf("signers = %#v, want %d entries", response.Signers, test.wantLen)
			}
			if test.signer != "" && (len(response.Signers["eip155:*"]) != 1 || response.Signers["eip155:*"][0] != test.signer) {
				t.Fatalf("signers = %#v", response.Signers)
			}
		})
	}
}

type validScheme struct{}

func (validScheme) Verify(
	_ context.Context,
	payload types.PaymentPayload,
	_ types.PaymentRequirements,
	_ *x402.FacilitatorContext,
) (*x402.VerifyResponse, error) {
	auth := payload.Payload["authorization"].(map[string]interface{})
	return &x402.VerifyResponse{IsValid: true, Payer: auth["from"].(string)}, nil
}

type validChain struct{}

func (validChain) GetCode(context.Context, string) ([]byte, error) { return nil, nil }
func (validChain) ReadContract(
	_ context.Context, _ string, _ []byte, function string, _ ...interface{},
) (interface{}, error) {
	if function == x402evm.FunctionAuthorizationState {
		return false, nil
	}
	return nil, nil
}

type discardVerification struct{}

func (discardVerification) RecordVerification(context.Context, verification.Attempt) error {
	return nil
}

func TestVerifyEndpoint(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	service := verification.New(validScheme{}, validChain{}, discardVerification{}, time.Second)
	handler := New(Dependencies{
		Logger: slog.Default(), Database: fakeDB{}, Ethereum: fakeRPC{chain: 1},
		Stats: stats.NewService(stats.Config{
			Source: statsSource{}, Started: time.Now(),
			// Wired with the same assessor production uses, so a test asserting the
			// status page reports an outage exercises the real derivation rather than
			// a nil health source that would report "unknown" whatever happened.
			Health: stats.NewAssessor(stats.AssessorConfig{
				Database: fakeDB{}, Chain: fakeRPC{chain: 1}, ExpectedChainID: 1,
				Heartbeats: registry, ExpectedWorkers: []string{"broadcast"},
				StaleAfter: time.Minute, SettlementEnabled: false,
			}),
		}), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100, Verification: service,
	}).Handler()
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: "1", PayTo: "0x1111111111111111111111111111111111111111",
		MaxTimeoutSeconds: 60,
		Extra: map[string]interface{}{
			"name": verification.USDCName, "version": verification.USDCVersion,
			"assetTransferMethod": "eip3009",
		},
	}
	request := verification.Request{
		X402Version: 2, PaymentRequirements: requirements,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]interface{}{
				"signature": "0x" + strings.Repeat("11", 65),
				"authorization": map[string]interface{}{
					"from": "0x2222222222222222222222222222222222222222",
					"to":   requirements.PayTo, "value": "1", "validAfter": "0",
					"validBefore": big.NewInt(time.Now().Add(time.Minute).Unix()).String(),
					"nonce":       "0x" + strings.Repeat("33", 32),
				},
			},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(string(body))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response x402.VerifyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.IsValid {
		t.Fatalf("response = %#v", response)
	}
}

func TestVerifyRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	service := verification.New(validScheme{}, validChain{}, discardVerification{}, time.Second)
	handler := New(Dependencies{
		Logger: slog.Default(), Database: fakeDB{}, Ethereum: fakeRPC{chain: 1},
		Stats: stats.NewService(stats.Config{
			Source: statsSource{}, Started: time.Now(),
			// Wired with the same assessor production uses, so a test asserting the
			// status page reports an outage exercises the real derivation rather than
			// a nil health source that would report "unknown" whatever happened.
			Health: stats.NewAssessor(stats.AssessorConfig{
				Database: fakeDB{}, Chain: fakeRPC{chain: 1}, ExpectedChainID: 1,
				Heartbeats: registry, ExpectedWorkers: []string{"broadcast"},
				StaleAfter: time.Minute, SettlementEnabled: false,
			}),
		}), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100, Verification: service,
	}).Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost, "/verify", strings.NewReader(`{"x402Version":2,"unknown":true}`),
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type deadlineResponseWriter struct {
	header   http.Header
	deadline time.Time
}

func (w *deadlineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*deadlineResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (*deadlineResponseWriter) WriteHeader(int)                {}
func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestStatusWriterExposesResponseController(t *testing.T) {
	t.Parallel()
	underlying := &deadlineResponseWriter{}
	wrapper := &statusWriter{ResponseWriter: underlying, status: http.StatusOK}
	want := time.Now().Add(time.Minute)
	if err := http.NewResponseController(wrapper).SetWriteDeadline(want); err != nil {
		t.Fatalf("set write deadline through metrics wrapper: %v", err)
	}
	if !underlying.deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", underlying.deadline, want)
	}
}
