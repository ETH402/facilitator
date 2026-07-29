//go:build abuse

// Package-level abuse tests. Tagged because they are slow and allocate heavily:
// they exist to characterise behaviour under deliberate abuse rather than to gate
// every commit. Run with:
//
//	go test -tags=abuse -timeout 20m ./internal/httpapi
package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/verification"
)

// abuseHandler wires the payment endpoints for real, unlike testServer, which
// leaves Verification and Settlement nil so /verify and /settle answer 503. Sending
// hostile input at an endpoint that refuses everything on configuration grounds
// would prove nothing.
func abuseHandler(t *testing.T) http.Handler {
	t.Helper()
	registry := metrics.New()
	return New(Dependencies{
		Logger: slog.New(slog.DiscardHandler), Database: fakeDB{}, Ethereum: fakeRPC{chain: 1},
		Stats: stats.NewService(stats.Config{
			Source: statsSource{}, Started: time.Now(),
			Health: stats.NewAssessor(stats.AssessorConfig{
				Database: fakeDB{}, Chain: fakeRPC{chain: 1}, ExpectedChainID: 1,
				Heartbeats: registry, StaleAfter: time.Minute,
			}),
		}),
		Metrics: registry, ExpectedChainID: 1, PublicRatePerMinute: 1_000_000,
		Verification: verification.New(validScheme{}, validChain{}, discardVerification{}, time.Second),
		Settlement:   settleTestService("0x" + strings.Repeat("ab", 32)),
	}).Handler()
}

// TestRateLimiterMemoryIsBounded is the memory-exhaustion question. The limiter
// keys a map by client address, so an attacker with many source addresses is
// asking it to allocate. IPv6 makes that cheap: a single /32 allocation yields
// 2^32 distinct /64 buckets, so "use more addresses" is not a meaningful cost.
func TestRateLimiterMemoryIsBounded(t *testing.T) {
	handler := testServer(nil, nil, 1)
	const distinct = 250_000 // well past the limiter's 100k cap

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	for i := range distinct {
		request := httptest.NewRequest(http.MethodGet, "/supported", nil)
		request.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x::1]:443", i>>16, i&0xffff)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	// The cap is 100k buckets. Each is a small struct plus a map entry and a key
	// string; 64 MiB leaves generous headroom while still failing loudly if the
	// bound is ever removed and the map grows with the request count.
	const ceiling = 64 << 20
	if grew > ceiling {
		t.Errorf("heap grew %d bytes across %d distinct clients, ceiling %d — is the bucket cap still enforced?",
			grew, distinct, ceiling)
	}
	t.Logf("heap grew %.1f MiB across %d distinct client addresses", float64(grew)/(1<<20), distinct)
}

// TestFloodDoesNotDenyServiceToNewClients is the sharper question, and the reason
// the test above is not enough.
//
// The bucket cap has to do *something* when it is reached. Collapsing every
// subsequent client into one shared bucket bounds memory, but it also hands an
// attacker a much stronger primitive than the one the cap prevents: fill the map,
// and every legitimate client arriving afterwards shares a single per-minute
// allowance. That converts a memory-growth nuisance into a service-wide denial.
//
// This test floods the limiter past its cap and then checks that an ordinary
// client can still be served.
func TestFloodDoesNotDenyServiceToNewClients(t *testing.T) {
	handler := testServer(nil, nil, 1)
	for i := range 150_000 {
		request := httptest.NewRequest(http.MethodGet, "/supported", nil)
		request.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x::1]:443", i>>16, i&0xffff)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}

	// A single legitimate client, arriving after the flood, well under the limit.
	served, limited := 0, 0
	for range 20 {
		request := httptest.NewRequest(http.MethodGet, "/supported", nil)
		request.RemoteAddr = "203.0.113.7:51000"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		switch recorder.Code {
		case http.StatusOK:
			served++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if served == 0 {
		t.Errorf("a legitimate client was denied every request after a flood (%d limited); "+
			"filling the bucket map must not deny service to everyone else", limited)
	}
	t.Logf("post-flood legitimate client: %d served, %d limited", served, limited)
}

// TestRateLimitStillAppliesUnderFlood is the other side of the same trade-off: the
// overflow path must not become a way to escape the limit entirely.
func TestRateLimitStillAppliesUnderFlood(t *testing.T) {
	handler := testServer(nil, nil, 1)
	for i := range 150_000 {
		request := httptest.NewRequest(http.MethodGet, "/supported", nil)
		request.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x::1]:443", i>>16, i&0xffff)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	// testServer permits 100/minute. One client sending far more than that must be
	// limited even though the bucket map is full.
	limited := 0
	for range 400 {
		request := httptest.NewRequest(http.MethodGet, "/supported", nil)
		request.RemoteAddr = "198.51.100.9:51000"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("a client sending 400 requests past a 100/minute limit was never limited; " +
			"the overflow path must not disable the limiter")
	}
	t.Logf("post-flood heavy client: %d of 400 limited", limited)
}

// TestMalformedPayloadsNeverPanic hammers the public payment endpoints with
// structurally hostile JSON. A panic in an HTTP handler is recovered per request,
// so this is not looking for a crash — it is looking for a 5xx, which means an
// input reached code that did not expect it.
func TestMalformedPayloadsNeverPanic(t *testing.T) {
	handler := abuseHandler(t)
	seeds := []string{
		``, `{`, `[]`, `null`, `0`, `"string"`, `{"paymentPayload":null}`,
		`{"paymentPayload":{"payload":{"authorization":{}}}}`,
		`{"paymentPayload":{"x402Version":-1}}`,
		`{"paymentPayload":{"x402Version":1e400}}`,
		`{"paymentRequirements":{"amount":"` + strings.Repeat("9", 400) + `"}}`,
		`{"paymentRequirements":{"amount":"-1"}}`,
		`{"paymentRequirements":{"payTo":"0x` + strings.Repeat("f", 40) + `"}}`,
		"{\"paymentRequirements\":{\"asset\":\"\x00\uffff\"}}",
		`{"paymentPayload":{"payload":{"signature":"0x` + strings.Repeat("0", 130) + `"}}}`,
	}
	// Deterministic mutations: no Date.now, no unseeded randomness, so a failure
	// reproduces.
	source := rand.New(rand.NewPCG(0x5eed, 0x5eed))
	var bodies []string
	bodies = append(bodies, seeds...)
	for range 2000 {
		seed := seeds[source.IntN(len(seeds))]
		bodies = append(bodies, mutate(seed, source))
	}

	for _, path := range []string{"/verify", "/settle"} {
		for i, body := range bodies {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = fmt.Sprintf("10.0.%d.%d:40000", (i/250)%256, i%250)
			handler.ServeHTTP(recorder, request)
			if recorder.Code >= 500 {
				t.Fatalf("%s returned %d for body %q", path, recorder.Code, truncate(body))
			}
			// Every refusal must still be a structured envelope, or a client cannot
			// distinguish "your request was wrong" from "we broke".
			if recorder.Code == http.StatusBadRequest || recorder.Code == http.StatusUnprocessableEntity {
				var envelope map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("%s returned unparseable body for %q: %s", path, truncate(body), recorder.Body)
				}
			}
		}
	}
}

func mutate(seed string, source *rand.Rand) string {
	if seed == "" {
		return ""
	}
	raw := []byte(seed)
	switch source.IntN(5) {
	case 0: // truncate
		return string(raw[:source.IntN(len(raw))])
	case 1: // duplicate a span, to nest and unbalance
		at := source.IntN(len(raw))
		return seed[:at] + seed[at:] + seed[at:]
	case 2: // flip a byte
		at := source.IntN(len(raw))
		raw[at] ^= byte(1 << source.IntN(8))
		return string(raw)
	case 3: // deep nesting, which is where recursive decoders fail
		return strings.Repeat(`{"a":`, 200) + seed + strings.Repeat(`}`, 200)
	default: // long field
		return strings.Replace(seed, `"`, `"`+strings.Repeat("A", 5000), 1)
	}
}

func truncate(value string) string {
	if len(value) <= 120 {
		return value
	}
	return value[:120] + "…"
}

// TestConcurrentAbuseKeepsHandlersConsistent runs the public surface concurrently
// from many addresses. It is looking for races the -race detector can see and for
// any 5xx: with stub dependencies, every response should be a deliberate one.
func TestConcurrentAbuseKeepsHandlersConsistent(t *testing.T) {
	handler := abuseHandler(t)
	paths := []string{"/supported", "/stats", "/status", "/health/live", "/health/ready", "/verify", "/settle"}

	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := map[string]int{}
	for worker := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				path := paths[(worker+i)%len(paths)]
				method := http.MethodGet
				var body *strings.Reader = strings.NewReader("")
				if path == "/verify" || path == "/settle" {
					method, body = http.MethodPost, strings.NewReader(`{"paymentPayload":{}}`)
				}
				request := httptest.NewRequest(method, path, body)
				request.RemoteAddr = fmt.Sprintf("172.16.%d.%d:50000", worker, i%250)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if recorder.Code >= 500 {
					mu.Lock()
					failures[fmt.Sprintf("%s=%d", path, recorder.Code)]++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		t.Errorf("5xx responses under concurrent load: %v", failures)
	}
}
