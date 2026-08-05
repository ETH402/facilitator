package metrics

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	return recorder.Body.String()
}

// An unset balance must be absent rather than zero: zero is indistinguishable
// from a drained signer, which is precisely the condition being alerted on.
func TestSignerBalanceAbsentUntilRead(t *testing.T) {
	body := scrape(t, New())
	if strings.Contains(body, "eth402_signer_balance_wei") {
		t.Fatalf("balance exposed before any successful read:\n%s", body)
	}
}

func TestSignerBalanceExposedWithFreshness(t *testing.T) {
	r := New()
	// 0.05 ETH: beyond exact float64 integer range, so this also covers the
	// documented precision caveat without breaking threshold alerting.
	wei, _ := new(big.Int).SetString("50000000000000000", 10)
	at := time.Unix(1_800_000_000, 0)
	r.SetSignerBalance(wei, at)
	r.IncSignerBalanceError()

	body := scrape(t, r)
	for _, want := range []string{
		"eth402_signer_balance_wei 5e+16",
		"eth402_signer_balance_updated_timestamp_seconds 1800000000",
		"eth402_signer_balance_read_errors_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	// Freshness must be published so an alert can catch reads that stopped
	// succeeding; a stale value alone looks healthy.
	if !strings.Contains(body, "# TYPE eth402_signer_balance_updated_timestamp_seconds gauge") {
		t.Fatal("freshness is not typed as a gauge")
	}
}

func TestSetSignerBalanceIgnoresNil(t *testing.T) {
	r := New()
	r.SetSignerBalance(nil, time.Now())
	if strings.Contains(scrape(t, r), "eth402_signer_balance_wei") {
		t.Fatal("a nil balance was published")
	}
}

// The placeholders these replaced were published as literal zeros, which is worse
// than absence: alerting on rpc_errors_total would never fire however broken the
// RPC was, and alerting on worker_healthy == 0 would fire constantly while
// everything was fine.
func TestRPCCountersReflectAttempts(t *testing.T) {
	r := New()
	body := scrape(t, r)
	for _, want := range []string{"eth402_rpc_requests_total 0", "eth402_rpc_errors_total 0", "eth402_rpc_provider_disagreements_total 0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	r.ObserveRPC(false)
	r.ObserveRPC(true)
	r.ObserveRPC(true)
	r.ObserveRPCDisagreement()
	body = scrape(t, r)
	for _, want := range []string{"eth402_rpc_requests_total 3", "eth402_rpc_errors_total 2", "eth402_rpc_provider_disagreements_total 1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func TestEmailDeliveryMetrics(t *testing.T) {
	t.Parallel()
	r := New()
	at := time.Unix(1_700_000_000, 0)
	r.ObserveEmailOutbox(3, 95*time.Second, at)
	r.ObserveEmailDeliveryFailure()
	body := scrape(t, r)
	for _, want := range []string{
		"eth402_email_outbox_pending 3",
		"eth402_email_outbox_oldest_pending_age_seconds 95",
		"eth402_email_delivery_last_tick_timestamp_seconds 1700000000",
		"eth402_email_delivery_failures_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func TestEmailDeliveryMetricsClampInvalidInputs(t *testing.T) {
	t.Parallel()
	r := New()
	r.ObserveEmailOutbox(-1, -time.Second, time.Unix(1, 0))
	body := scrape(t, r)
	for _, want := range []string{
		"eth402_email_outbox_pending 0",
		"eth402_email_outbox_oldest_pending_age_seconds 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func TestRecipientChangeMetricsAreLowCardinality(t *testing.T) {
	t.Parallel()
	r := New()
	r.ObserveRecipientChange(true)
	r.ObserveRecipientChange(false)
	body := scrape(t, r)
	for _, want := range []string{
		"eth402_recipient_pending_changes_total 1",
		"eth402_recipient_active_changes_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "merchant_id=") || strings.Contains(body, "address=") {
		t.Fatal("recipient metrics exposed a dynamic merchant or address label")
	}
}

func TestWorkerHealthAbsentUntilAWorkerTicks(t *testing.T) {
	// Settlement disabled means no workers. Zero would look like every worker had
	// stalled, so nothing is published at all.
	if strings.Contains(scrape(t, New()), "eth402_worker_last_tick_timestamp_seconds") {
		t.Fatal("worker health published with no workers running")
	}
}

func TestWorkerHealthPublishesEachTick(t *testing.T) {
	r := New()
	r.Heartbeat("broadcast", time.Unix(1_800_000_000, 0))
	r.Heartbeat("recovery", time.Unix(1_800_000_060, 0))
	body := scrape(t, r)
	for _, want := range []string{
		`eth402_worker_last_tick_timestamp_seconds{worker="broadcast"} 1800000000`,
		`eth402_worker_last_tick_timestamp_seconds{worker="recovery"} 1800000060`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	// A later tick replaces the earlier one rather than accumulating.
	r.Heartbeat("broadcast", time.Unix(1_800_000_120, 0))
	if !strings.Contains(scrape(t, r), `{worker="broadcast"} 1800000120`) {
		t.Fatal("heartbeat did not advance")
	}
}

// Nothing may still publish a metric that never moves.
func TestNoPlaceholderMetricsRemain(t *testing.T) {
	r := New()
	r.ObserveRPC(false)
	r.Heartbeat("broadcast", time.Now())
	body := scrape(t, r)
	for _, gone := range []string{
		"eth402_confirmation_lag_blocks", "eth402_settlement_latency_seconds",
		"eth402_database_errors_total", "eth402_settlements_confirmed_total",
		"eth402_settlements_failed_total", "eth402_worker_healthy",
	} {
		if strings.Contains(body, gone) {
			t.Fatalf("placeholder metric %q is still published", gone)
		}
	}
}

func TestRetentionMetricsExposeLivenessAndOutcome(t *testing.T) {
	r := New()
	if strings.Contains(scrape(t, r), "eth402_retention_last_tick") {
		t.Fatal("retention tick exposed before the worker ran")
	}
	r.ObserveRetention(3, false, time.Unix(1_800_000_000, 0))
	r.ObserveRetention(0, true, time.Unix(1_800_000_060, 0))
	body := scrape(t, r)
	for _, want := range []string{
		"eth402_retention_last_tick_timestamp_seconds 1800000060",
		"eth402_retention_errors_total 1",
		"eth402_retention_redacted_payments_total 3",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}
