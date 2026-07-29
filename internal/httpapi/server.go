package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/verification"
)

const maxRequestBody = 1 << 20

type DatabaseHealth interface{ Ping(context.Context) error }

type Dependencies struct {
	Logger              *slog.Logger
	Database            DatabaseHealth
	Ethereum            ethereum.Reader
	Stats               *stats.Service
	Metrics             *metrics.Registry
	ExpectedChainID     uint64
	PublicRatePerMinute int
	RegistrationRate    int
	Merchant            *merchant.Service
	AllowedOrigin       string
	OperatorToken       string
	Verification        *verification.Service
	Settlement          *settlement.Service
	MetricsEnabled      bool
	// TrustedProxies lists reverse proxies permitted to assert a client
	// address through X-Forwarded-For. Empty means the direct peer is used.
	TrustedProxies []netip.Prefix
}

type Server struct {
	handler http.Handler
}

func New(dep Dependencies) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
	})
	mux.HandleFunc("GET /health/ready", dep.ready)
	mux.HandleFunc("GET /stats", dep.stats)
	mux.HandleFunc("GET /status", dep.status)
	if dep.MetricsEnabled {
		mux.Handle("GET /metrics", dep.Metrics)
	}
	mux.HandleFunc("GET /supported", dep.supported)
	mux.HandleFunc("POST /verify", dep.verify)
	mux.HandleFunc("POST /settle", dep.settle)
	dep.merchantRoutes(mux)
	handler := secureHeaders(dep.AllowedOrigin, mux)
	handler = requestLimit(handler)
	handler = newRateLimiter(dep.PublicRatePerMinute, dep.TrustedProxies).middleware(handler)
	handler = observe(dep.Metrics, handler)
	handler = recovery(dep.Logger, dep.Metrics, handler)
	handler = requestID(handler)
	return &Server{handler: handler}
}

func (d Dependencies) supported(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, verification.Supported())
}

func (d Dependencies) verify(w http.ResponseWriter, r *http.Request) {
	if d.Verification == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"isValid": false, "invalidReason": "service_unavailable",
		})
		return
	}
	var request verification.Request
	if err := DecodeStrict(w, r, &request); err != nil {
		d.Metrics.IncVerification()
		d.Metrics.IncVerificationFailure()
		if recordErr := d.Verification.RecordInvalidRequest(r.Context()); recordErr != nil {
			d.Logger.ErrorContext(r.Context(), "invalid verification request recording failed", "error", recordErr)
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"isValid": false, "invalidReason": "invalid_request",
		})
		return
	}
	d.Metrics.IncVerification()
	response, err := d.Verification.Verify(r.Context(), request)
	if err != nil {
		d.Metrics.IncVerificationFailure()
		d.Logger.ErrorContext(r.Context(), "payment verification failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"isValid": false, "invalidReason": "service_unavailable",
		})
		return
	}
	if !response.IsValid {
		d.Metrics.IncVerificationFailure()
	}
	writeJSON(w, http.StatusOK, response)
}

func (d Dependencies) settle(w http.ResponseWriter, r *http.Request) {
	if d.Settlement == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false, "errorReason": settlement.WireReasonSettlementUnavailable,
		})
		return
	}
	var request settlement.SettleRequest
	if err := DecodeStrict(w, r, &request); err != nil {
		d.Metrics.IncSettlement()
		d.Metrics.IncSettlementFailure()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "errorReason": "invalid_request",
		})
		return
	}
	d.Metrics.IncSettlement()
	response, err := d.Settlement.Settle(r.Context(), request)
	if err != nil {
		d.Metrics.IncSettlementFailure()
		d.Logger.ErrorContext(r.Context(), "payment settlement failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false, "errorReason": settlement.WireReasonSettlementUnavailable,
		})
		return
	}
	if !response.Success {
		d.Metrics.IncSettlementFailure()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) Handler() http.Handler { return s.handler }

func (d Dependencies) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]string{"database": "ok", "ethereum_rpc": "ok"}
	ready := true
	if d.Database == nil || d.Database.Ping(ctx) != nil {
		checks["database"], ready = "unavailable", false
	}
	if d.Ethereum == nil {
		checks["ethereum_rpc"], ready = "unavailable", false
	} else if chainID, err := d.Ethereum.ChainID(ctx); err != nil || chainID != d.ExpectedChainID {
		checks["ethereum_rpc"], ready = "unavailable", false
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status, state = http.StatusServiceUnavailable, "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": checks})
}

func (d Dependencies) stats(w http.ResponseWriter, r *http.Request) {
	if d.Stats == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "statistics are temporarily unavailable", requestIDFrom(r.Context()))
		return
	}
	response, err := d.Stats.Get(r.Context())
	if err != nil {
		d.Logger.ErrorContext(r.Context(), "stats aggregation failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "statistics are temporarily unavailable", requestIDFrom(r.Context()))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=10")
	writeJSON(w, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message, requestID string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": requestID}})
}

type requestIDKey struct{}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if len(id) < 8 || len(id) > 128 {
			var raw [16]byte
			if _, err := rand.Read(raw[:]); err != nil {
				id = strconv.FormatInt(time.Now().UnixNano(), 10)
			} else {
				id = hex.EncodeToString(raw[:])
			}
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func secureHeaders(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if origin := r.Header.Get("Origin"); origin != "" && origin != allowedOrigin {
			writeError(w, http.StatusForbidden, "cors_denied", "cross-origin requests are not allowed", requestIDFrom(r.Context()))
			return
		} else if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}

func requestLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

func recovery(logger *slog.Logger, registry *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				registry.IncPanic()
				logger.ErrorContext(r.Context(), "HTTP panic recovered", "panic", recovered, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error", requestIDFrom(r.Context()))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func observe(registry *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		registry.ObserveHTTP(r.Method, knownRoute(r.URL.Path), sw.status, time.Since(start))
	})
}

func knownRoute(path string) string {
	switch path {
	case "/health/live", "/health/ready", "/metrics", "/stats",
		"/supported", "/verify", "/settle",
		"/v1/merchants/register", "/v1/merchants/verify-email",
		"/v1/merchants/wallet-challenge", "/v1/merchants/verify-wallet",
		"/v1/me", "/v1/api-keys", "/v1/me/recipient-change",
		"/v1/me/recipient-change/verify":
		return path
	}
	// Routes carrying an identifier are reported as their registered pattern so
	// that per-route metrics stay bounded and do not leak merchant or key IDs.
	for _, pattern := range []string{
		"/v1/api-keys/{id}",
		"/v1/admin/merchants/{id}/suspend",
		"/v1/admin/merchants/{id}/reinstate",
	} {
		if matchesPattern(path, pattern) {
			return pattern
		}
	}
	return "unknown"
}

// matchesPattern reports whether path has the same shape as a ServeMux pattern
// whose only wildcard is a single non-empty path segment.
func matchesPattern(path, pattern string) bool {
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")
	patternSegments := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(pathSegments) != len(patternSegments) {
		return false
	}
	for i, segment := range patternSegments {
		if strings.HasPrefix(segment, "{") {
			if pathSegments[i] == "" {
				return false
			}
			continue
		}
		if pathSegments[i] != segment {
			return false
		}
	}
	return true
}

type rateLimiter struct {
	limit     int
	trusted   []netip.Prefix
	mu        sync.Mutex
	clients   map[string]*rateWindow
	lastSweep time.Time
}

type rateWindow struct {
	started time.Time
	count   int
}

// maxTrackedClients bounds the bucket map. It has to be bounded: the map is keyed
// by client address, and IPv6 makes distinct addresses free — a single /32
// allocation yields 2^32 distinct /64 buckets.
const maxTrackedClients = 100_000

// evictionSample is how many entries eviction looks at before choosing. Sampling
// keeps eviction O(1) while still usually removing an old entry; Go randomizes map
// iteration order, so the sample is drawn fairly across the map.
const evictionSample = 8

// evictLocked frees one slot so a newly seen client can have its own bucket.
//
// The obvious alternative — put every client past the cap into one shared bucket —
// bounds memory just as well but hands an attacker a far stronger primitive than
// the one the cap prevents: fill the map, and every legitimate client arriving
// afterwards shares a single per-minute allowance. That was measured, not
// theorised: a client sending 20 requests was denied all 20. It converts a
// memory-growth nuisance into a service-wide denial.
//
// Eviction fails the other way, and does so at a cost that makes it uninteresting.
// An attacker can churn the map to evict a heavy client's bucket and reset its
// counter — but doing that costs roughly maxTrackedClients requests to reset one
// bucket worth a few dozen, so an attacker who can afford the churn could simply
// have sent those requests from the churned addresses instead. Losing a little
// accuracy under flood is worth not denying everyone else.
//
// Callers must hold l.mu.
func (l *rateLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldestStart time.Time
	sampled := 0
	for key, window := range l.clients {
		if now.Sub(window.started) >= time.Minute {
			// Already expired: removing it costs nothing at all.
			delete(l.clients, key)
			return
		}
		if oldestKey == "" || window.started.Before(oldestStart) {
			oldestKey, oldestStart = key, window.started
		}
		if sampled++; sampled >= evictionSample {
			break
		}
	}
	if oldestKey != "" {
		delete(l.clients, oldestKey)
	}
}

func newRateLimiter(limit int, trusted []netip.Prefix) *rateLimiter {
	return &rateLimiter{limit: limit, trusted: trusted, clients: make(map[string]*rateWindow)}
}

// clientAddress resolves the bucket key for a request. When the direct peer is
// a trusted proxy, the rightmost X-Forwarded-For entry that is not itself a
// trusted proxy wins: each proxy appends the peer it observed, so a forged
// header can only prepend entries to the left of the real client address and
// can never select another client's bucket. When the peer is untrusted the
// header is ignored entirely.
func (l *rateLimiter) clientAddress(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	peerAddress, err := netip.ParseAddr(strings.TrimSpace(peer))
	if err != nil {
		return peer
	}
	if !l.isTrusted(peerAddress) {
		return bucketKey(peerAddress)
	}
	for _, entry := range forwardedFor(r) {
		address, err := netip.ParseAddr(entry)
		if err != nil {
			// A malformed entry can only have been supplied by the client,
			// which means every trustworthy hop has already been examined.
			break
		}
		if !l.isTrusted(address) {
			return bucketKey(address)
		}
	}
	return bucketKey(peerAddress)
}

func (l *rateLimiter) isTrusted(address netip.Addr) bool {
	candidate := address.Unmap()
	for _, prefix := range l.trusted {
		if prefix.Contains(candidate) {
			return true
		}
	}
	return false
}

// forwardedFor returns every X-Forwarded-For entry in right-to-left order.
func forwardedFor(r *http.Request) []string {
	var entries []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, entry := range strings.Split(header, ",") {
			entries = append(entries, strings.TrimSpace(entry))
		}
	}
	slices.Reverse(entries)
	return entries
}

// bucketKey groups IPv6 clients by /64 because a single subscriber is routinely
// assigned the whole prefix and could otherwise obtain unlimited buckets.
func bucketKey(address netip.Addr) string {
	address = address.Unmap().WithZone("")
	if address.Is6() {
		if prefix, err := address.Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return address.String()
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.limit <= 0 || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		host := l.clientAddress(r)
		now := time.Now()
		l.mu.Lock()
		if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= time.Minute {
			for client, candidate := range l.clients {
				if now.Sub(candidate.started) >= time.Minute {
					delete(l.clients, client)
				}
			}
			l.lastSweep = now
		}
		window := l.clients[host]
		if window == nil && len(l.clients) >= maxTrackedClients {
			l.evictLocked(now)
		}
		if window == nil || now.Sub(window.started) >= time.Minute {
			window = &rateWindow{started: now}
			l.clients[host] = window
		}
		window.count++
		allowed := window.count <= l.limit
		l.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded", requestIDFrom(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DecodeStrict is shared by future merchant and facilitator handlers.
func DecodeStrict(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
