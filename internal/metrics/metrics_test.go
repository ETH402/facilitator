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
