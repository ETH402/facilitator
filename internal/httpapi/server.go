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
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/stats"
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
	mux.Handle("GET /metrics", dep.Metrics)
	handler := secureHeaders(mux)
	handler = requestLimit(handler)
	handler = requestID(handler)
	handler = recovery(dep.Logger, dep.Metrics, handler)
	handler = observe(dep.Metrics, handler)
	handler = newRateLimiter(dep.PublicRatePerMinute).middleware(handler)
	return &Server{handler: handler}
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

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "cors_denied", "cross-origin requests are not allowed", requestIDFrom(r.Context()))
			return
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
	case "/health/live", "/health/ready", "/metrics", "/stats":
		return path
	default:
		return "unknown"
	}
}

type rateLimiter struct {
	limit   int
	mu      sync.Mutex
	clients map[string]*rateWindow
}

type rateWindow struct {
	started time.Time
	count   int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, clients: make(map[string]*rateWindow)}
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.limit <= 0 || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		now := time.Now()
		l.mu.Lock()
		window := l.clients[host]
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
