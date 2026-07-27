package metrics

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Registry struct {
	httpRequests   atomic.Uint64
	httpDuration   atomic.Int64
	panicCount     atomic.Uint64
	registrations  atomic.Uint64
	emailVerified  atomic.Uint64
	walletVerified atomic.Uint64
	walletFailures atomic.Uint64
	verifications  atomic.Uint64
	verifyFailures atomic.Uint64
	mu             sync.Mutex
	status         map[string]uint64
}

func New() *Registry { return &Registry{status: make(map[string]uint64)} }

func (r *Registry) ObserveHTTP(method, route string, status int, duration time.Duration) {
	r.httpRequests.Add(1)
	if duration > 0 {
		r.httpDuration.Add(duration.Microseconds())
	}
	key := method + "|" + route + "|" + strconv.Itoa(status)
	r.mu.Lock()
	r.status[key]++
	r.mu.Unlock()
}

func (r *Registry) IncPanic()                     { r.panicCount.Add(1) }
func (r *Registry) IncRegistration()              { r.registrations.Add(1) }
func (r *Registry) IncEmailVerification()         { r.emailVerified.Add(1) }
func (r *Registry) IncWalletVerification()        { r.walletVerified.Add(1) }
func (r *Registry) IncWalletVerificationFailure() { r.walletFailures.Add(1) }
func (r *Registry) IncVerification()              { r.verifications.Add(1) }
func (r *Registry) IncVerificationFailure()       { r.verifyFailures.Add(1) }

func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, "# HELP eth402_http_requests_total Total HTTP requests.\n# TYPE eth402_http_requests_total counter\n")
	r.mu.Lock()
	for key, count := range r.status {
		parts := strings.Split(key, "|")
		_, _ = fmt.Fprintf(w, "eth402_http_requests_total{method=%q,route=%q,status=%q} %d\n", parts[0], parts[1], parts[2], count)
	}
	r.mu.Unlock()
	_, _ = fmt.Fprintf(w, "# HELP eth402_http_request_duration_microseconds_total Aggregate HTTP request duration.\n# TYPE eth402_http_request_duration_microseconds_total counter\neth402_http_request_duration_microseconds_total %d\n", r.httpDuration.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_panics_total Recovered HTTP panics.\n# TYPE eth402_panics_total counter\neth402_panics_total %d\n", r.panicCount.Load())
	for _, name := range []string{
		"settlement_requests_total",
		"settlements_confirmed_total", "settlements_failed_total", "rpc_errors_total",
		"database_errors_total",
	} {
		_, _ = fmt.Fprintf(w, "# TYPE eth402_%s counter\neth402_%s 0\n", name, name)
	}
	for name, value := range map[string]uint64{
		"registrations_total":                r.registrations.Load(),
		"email_verifications_total":          r.emailVerified.Load(),
		"wallet_verifications_total":         r.walletVerified.Load(),
		"wallet_verification_failures_total": r.walletFailures.Load(),
		"verification_total":                 r.verifications.Load(),
		"verification_failures_total":        r.verifyFailures.Load(),
	} {
		_, _ = fmt.Fprintf(w, "# TYPE eth402_%s counter\neth402_%s %d\n", name, name, value)
	}
	_, _ = io.WriteString(w, "# TYPE eth402_confirmation_lag_blocks gauge\neth402_confirmation_lag_blocks 0\n# TYPE eth402_worker_healthy gauge\neth402_worker_healthy 0\n")
	_, _ = io.WriteString(w, "# TYPE eth402_settlement_latency_seconds histogram\neth402_settlement_latency_seconds_bucket{le=\"+Inf\"} 0\neth402_settlement_latency_seconds_sum 0\neth402_settlement_latency_seconds_count 0\n")
	_, _ = io.WriteString(w, "# TYPE eth402_rpc_requests_total counter\neth402_rpc_requests_total 0\n")
}
