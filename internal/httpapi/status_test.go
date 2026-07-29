package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStatusPageIsSelfContained is the property that keeps the page useful during
// the outages it exists to report. An external stylesheet, script, or font would
// fail exactly when the network is broken, and would disclose to a third party who
// is reading the page.
func TestStatusPageIsSelfContained(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{
		"http://", "https://", "//cdn", "<script", "src=", "@import", "url(",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("status page contains %q; it must load nothing external", forbidden)
		}
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content type = %q, want text/html", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", got)
	}
}

// TestStatusPageDisclosesNoMoreThanTheJSON guards the public surface. The page
// renders the same snapshot /stats returns, so withheld volume must stay withheld
// here too — a template that reached past the snapshot would quietly undo it.
func TestStatusPageDisclosesNoMoreThanTheJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	body := rec.Body.String()
	if strings.Contains(body, "USDC</dd>") {
		t.Error("status page published settled volume while /stats withholds it")
	}
	// The stylesheet is excluded: it legitimately contains @media and colour hex,
	// which would otherwise match the patterns this is looking for. What matters is
	// the rendered content.
	content := body
	if start := strings.Index(content, "</style>"); start >= 0 {
		content = content[start:]
	}
	for pattern, what := range map[string]string{
		`0x`:           "an Ethereum address",
		`@`:            "an email address",
		`api_key`:      "an API key",
		`merchant_id`:  "a merchant identifier",
		`payment_iden`: "a payment identity",
	} {
		if strings.Contains(content, pattern) {
			t.Errorf("status page appears to contain %s (%q); it must carry no merchant or payer data",
				what, pattern)
		}
	}
}

// TestStatusPageReportsAnOutage is the whole point: the page must be able to say
// something is wrong. testServer's RPC reports the wrong chain here, which is an
// outage a reachable-but-misconfigured RPC would otherwise hide.
func TestStatusPageReportsAnOutage(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testServer(nil, nil, 8453).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "outage") {
		t.Errorf("a wrong-chain RPC must be reported as an outage, got:\n%s", body)
	}
	if strings.Contains(body, "All systems operational") {
		t.Error("status page reported health during an outage")
	}
}
