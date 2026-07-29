package metrics

import (
	"fmt"
	"io"
	"math/big"
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
	settlements    atomic.Uint64
	settleFailures atomic.Uint64
	// Signer balance is stored as a string so an arbitrarily large wei value
	// survives; it is converted only at exposition, where Prometheus requires a
	// float anyway.
	signerBalanceWei atomic.Value
	signerBalanceAt  atomic.Int64
	signerBalanceErr atomic.Uint64
	mu               sync.Mutex
	status           map[string]uint64
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
func (r *Registry) IncSettlement()                { r.settlements.Add(1) }
func (r *Registry) IncSettlementFailure()         { r.settleFailures.Add(1) }

// SetSignerBalance records the settlement signer's ether balance and when it was
// read. The timestamp matters as much as the value: if a balance read starts
// failing, the last figure would otherwise sit there looking healthy, so alerts
// need to detect staleness as well as depletion.
func (r *Registry) SetSignerBalance(wei *big.Int, at time.Time) {
	if wei == nil {
		return
	}
	r.signerBalanceWei.Store(wei.String())
	r.signerBalanceAt.Store(at.Unix())
}

// IncSignerBalanceError counts failed balance reads, so a persistently
// unreadable balance is visible rather than merely stale.
func (r *Registry) IncSignerBalanceError() { r.signerBalanceErr.Add(1) }

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
		"settlement_requests_total":          r.settlements.Load(),
		"settlement_failures_total":          r.settleFailures.Load(),
	} {
		_, _ = fmt.Fprintf(w, "# TYPE eth402_%s counter\neth402_%s %d\n", name, name, value)
	}
	_, _ = io.WriteString(w, "# TYPE eth402_confirmation_lag_blocks gauge\neth402_confirmation_lag_blocks 0\n# TYPE eth402_worker_healthy gauge\neth402_worker_healthy 0\n")
	_, _ = io.WriteString(w, "# TYPE eth402_settlement_latency_seconds histogram\neth402_settlement_latency_seconds_bucket{le=\"+Inf\"} 0\neth402_settlement_latency_seconds_sum 0\neth402_settlement_latency_seconds_count 0\n")
	_, _ = io.WriteString(w, "# TYPE eth402_rpc_requests_total counter\neth402_rpc_requests_total 0\n")
	r.writeSignerBalance(w)
}

// writeSignerBalance exposes the bound on how much a compromised process can
// spend. ADR-0004 decision 8 makes the hot balance the operative
// signer-compromise control, because Cloud KMS cannot inspect calldata and the
// allowlist therefore lives inside this process — so the balance is only a
// control if somebody is watching it.
//
// Burn rate is deliberately not computed here. Prometheus derives it from this
// gauge with deriv() or rate(), correctly across restarts, which an in-process
// counter could not.
func (r *Registry) writeSignerBalance(w io.Writer) {
	stored, _ := r.signerBalanceWei.Load().(string)
	if stored == "" {
		// No signer configured, or no successful read yet. Emitting nothing is
		// better than emitting zero, which is indistinguishable from a drained
		// account.
		return
	}
	wei, ok := new(big.Float).SetString(stored)
	if !ok {
		return
	}
	balance, _ := wei.Float64()
	_, _ = fmt.Fprintf(w, "# HELP eth402_signer_balance_wei Settlement signer balance in wei; float precision, adequate for thresholds.\n"+
		"# TYPE eth402_signer_balance_wei gauge\neth402_signer_balance_wei %g\n", balance)
	_, _ = fmt.Fprintf(w, "# HELP eth402_signer_balance_updated_timestamp_seconds When the balance was last read successfully.\n"+
		"# TYPE eth402_signer_balance_updated_timestamp_seconds gauge\neth402_signer_balance_updated_timestamp_seconds %d\n", r.signerBalanceAt.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_signer_balance_read_errors_total Failed signer balance reads.\n"+
		"# TYPE eth402_signer_balance_read_errors_total counter\neth402_signer_balance_read_errors_total %d\n", r.signerBalanceErr.Load())
}
