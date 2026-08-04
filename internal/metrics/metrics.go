package metrics

import (
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
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
	signerBalanceWei   atomic.Value
	signerBalanceAt    atomic.Int64
	signerBalanceErr   atomic.Uint64
	rpcRequests        atomic.Uint64
	rpcErrors          atomic.Uint64
	rpcDisagreements   atomic.Uint64
	fairUseRefusals    atomic.Uint64
	retentionLastTick  atomic.Int64
	retentionErrors    atomic.Uint64
	retentionRedacted  atomic.Uint64
	emailOutboxPending atomic.Int64
	emailOutboxOldest  atomic.Int64
	emailOutboxTick    atomic.Int64
	emailDeliveryFails atomic.Uint64
	// Worker heartbeats, keyed by worker name. A worker is healthy when it has
	// ticked recently; absence is as meaningful as staleness, so a worker that
	// never started is never reported healthy.
	heartbeats sync.Map
	mu         sync.Mutex
	status     map[string]uint64
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

// ObserveRPC counts an Ethereum RPC attempt and whether it failed. Every read
// funnels through one place in the client, so this is a true request count rather
// than a sampling of call sites.
func (r *Registry) ObserveRPC(failed bool) {
	r.rpcRequests.Add(1)
	if failed {
		r.rpcErrors.Add(1)
	}
}

// ObserveRPCDisagreement counts successful provider responses whose decoded
// payment-critical state differs. It is separate from rpcErrors: both attempts
// succeeded as requests, but the logical read failed closed and needs a distinct
// operator alert.
func (r *Registry) ObserveRPCDisagreement() { r.rpcDisagreements.Add(1) }

// ObserveEmailOutbox records aggregate delivery backlog and a successful worker
// observation. No merchant, recipient, token, request, or delivery identifier is
// accepted here, keeping the metrics boundary low-cardinality and secret-free.
func (r *Registry) ObserveEmailOutbox(pending int64, oldestPendingAge time.Duration, at time.Time) {
	if pending < 0 {
		pending = 0
	}
	oldest := oldestPendingAge / time.Second
	if oldest < 0 {
		oldest = 0
	}
	r.emailOutboxPending.Store(pending)
	r.emailOutboxOldest.Store(int64(oldest))
	r.emailOutboxTick.Store(at.Unix())
}

func (r *Registry) ObserveEmailDeliveryFailure() { r.emailDeliveryFails.Add(1) }

// Heartbeat records that a worker completed a tick. Health is derived from
// recency rather than a boolean the worker sets, because a worker that has
// wedged would never get around to clearing its own flag.
func (r *Registry) Heartbeat(worker string, at time.Time) {
	r.heartbeats.Store(worker, at.Unix())
}

// IncFairUseRefusal counts requests refused by the per-merchant fair-use control.
// Separate from the per-IP 429 count: the two have different causes and different
// responses, and a single counter would hide which one is firing.
func (r *Registry) IncFairUseRefusal() { r.fairUseRefusals.Add(1) }

// ObserveRetention records both liveness and outcomes for the privacy worker.
// A failed pass still advances the timestamp and increments the error counter,
// distinguishing a live-but-failing worker from one that stopped.
func (r *Registry) ObserveRetention(redacted int64, failed bool, at time.Time) {
	r.retentionLastTick.Store(at.Unix())
	if failed {
		r.retentionErrors.Add(1)
	}
	if redacted > 0 {
		r.retentionRedacted.Add(uint64(redacted))
	}
}

// WorkerHeartbeats returns the last tick time per worker, so the public status
// page can derive settlement health from the same observations Prometheus scrapes
// rather than from a second, separately-maintained notion of health.
func (r *Registry) WorkerHeartbeats() map[string]time.Time {
	out := make(map[string]time.Time)
	r.heartbeats.Range(func(key, value any) bool {
		name, nameOK := key.(string)
		at, atOK := value.(int64)
		if nameOK && atOK {
			out[name] = time.Unix(at, 0)
		}
		return true
	})
	return out
}

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

	for name, value := range map[string]uint64{
		"registrations_total":                r.registrations.Load(),
		"email_verifications_total":          r.emailVerified.Load(),
		"wallet_verifications_total":         r.walletVerified.Load(),
		"wallet_verification_failures_total": r.walletFailures.Load(),
		"verification_total":                 r.verifications.Load(),
		"verification_failures_total":        r.verifyFailures.Load(),
		"settlement_requests_total":          r.settlements.Load(),
		"settlement_failures_total":          r.settleFailures.Load(),
		"fair_use_refusals_total":            r.fairUseRefusals.Load(),
	} {
		_, _ = fmt.Fprintf(w, "# TYPE eth402_%s counter\neth402_%s %d\n", name, name, value)
	}
	// settlements_confirmed_total, settlements_failed_total, database_errors_total,
	// confirmation_lag_blocks and settlement_latency_seconds were published here as
	// literal zeros. They are removed rather than left in place: a metric that never
	// moves is worse than an absent one, because an operator alerting on
	// rpc_errors_total would never be paged however broken the RPC was, and one
	// alerting on worker_healthy == 0 would be paged constantly while everything was
	// fine. Confirmed and failed settlement counts remain available from /stats,
	// which derives them from the database.
	_, _ = fmt.Fprintf(w, "# HELP eth402_rpc_requests_total Ethereum RPC attempts.\n"+
		"# TYPE eth402_rpc_requests_total counter\neth402_rpc_requests_total %d\n", r.rpcRequests.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_rpc_errors_total Failed Ethereum RPC attempts.\n"+
		"# TYPE eth402_rpc_errors_total counter\neth402_rpc_errors_total %d\n", r.rpcErrors.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_rpc_provider_disagreements_total Payment-critical Ethereum RPC reads whose providers disagreed.\n"+
		"# TYPE eth402_rpc_provider_disagreements_total counter\neth402_rpc_provider_disagreements_total %d\n", r.rpcDisagreements.Load())
	r.writeWorkerHealth(w)
	r.writeSignerBalance(w)
	r.writeRetention(w)
	r.writeEmailDelivery(w)
}

func (r *Registry) writeEmailDelivery(w io.Writer) {
	_, _ = fmt.Fprintf(w, "# HELP eth402_email_outbox_pending Pending registration and admin-login email deliveries.\n"+
		"# TYPE eth402_email_outbox_pending gauge\neth402_email_outbox_pending %d\n", r.emailOutboxPending.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_email_outbox_oldest_pending_age_seconds Age of the oldest pending email delivery.\n"+
		"# TYPE eth402_email_outbox_oldest_pending_age_seconds gauge\neth402_email_outbox_oldest_pending_age_seconds %d\n", r.emailOutboxOldest.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_email_delivery_last_tick_timestamp_seconds Last successful email-outbox worker observation.\n"+
		"# TYPE eth402_email_delivery_last_tick_timestamp_seconds gauge\neth402_email_delivery_last_tick_timestamp_seconds %d\n", r.emailOutboxTick.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_email_delivery_failures_total Failed SMTP submissions or permanently unreadable outbox payloads.\n"+
		"# TYPE eth402_email_delivery_failures_total counter\neth402_email_delivery_failures_total %d\n", r.emailDeliveryFails.Load())
}

// writeWorkerHealth publishes each worker's last tick. The timestamp rather than a
// boolean: liveness is the operator's threshold to choose against the worker
// interval, and a wedged worker cannot be relied on to report itself unhealthy.
func (r *Registry) writeWorkerHealth(w io.Writer) {
	names := make([]string, 0, 4)
	r.heartbeats.Range(func(key, _ any) bool {
		if name, ok := key.(string); ok {
			names = append(names, name)
		}
		return true
	})
	if len(names) == 0 {
		// No workers running — settlement is disabled. Emitting nothing is honest;
		// emitting zero would look like every worker had stalled.
		return
	}
	sort.Strings(names)
	_, _ = io.WriteString(w, "# HELP eth402_worker_last_tick_timestamp_seconds When a settlement worker last completed a tick.\n"+
		"# TYPE eth402_worker_last_tick_timestamp_seconds gauge\n")
	for _, name := range names {
		if at, ok := r.heartbeats.Load(name); ok {
			if seconds, ok := at.(int64); ok {
				_, _ = fmt.Fprintf(w, "eth402_worker_last_tick_timestamp_seconds{worker=%q} %d\n", name, seconds)
			}
		}
	}
}

// writeSignerBalance exposes the bound on how much a compromised process can
// spend. ADR-0004 decision 8 makes the hot balance the operative
// final signer-compromise loss bound. The policy boundary constrains facilitator
// requests structurally, while the KMS-authorized boundary can still spend gas;
// the balance is only a control if somebody is watching it.
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

func (r *Registry) writeRetention(w io.Writer) {
	lastTick := r.retentionLastTick.Load()
	if lastTick == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP eth402_retention_last_tick_timestamp_seconds When the privacy retention worker last completed a pass.\n"+
		"# TYPE eth402_retention_last_tick_timestamp_seconds gauge\neth402_retention_last_tick_timestamp_seconds %d\n", lastTick)
	_, _ = fmt.Fprintf(w, "# HELP eth402_retention_errors_total Failed privacy retention passes.\n"+
		"# TYPE eth402_retention_errors_total counter\neth402_retention_errors_total %d\n", r.retentionErrors.Load())
	_, _ = fmt.Fprintf(w, "# HELP eth402_retention_redacted_payments_total Terminal payments tombstoned by privacy retention.\n"+
		"# TYPE eth402_retention_redacted_payments_total counter\neth402_retention_redacted_payments_total %d\n", r.retentionRedacted.Load())
}
