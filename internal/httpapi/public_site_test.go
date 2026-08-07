package httpapi

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicSiteRendersFirstPartyProductSurface(t *testing.T) {
	t.Parallel()
	handler := testServer(nil, nil, 1)
	for _, path := range []string{"/", "/explore"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `href="/assets/site.css"`) || strings.Contains(body, "<script") ||
			strings.Contains(body, "fonts.googleapis") || strings.Contains(body, "//cdn") {
			t.Fatalf("GET %s uses a non-first-party product surface", path)
		}
		csp := recorder.Header().Get("Content-Security-Policy")
		for _, expected := range []string{"default-src 'none'", "style-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, expected) {
				t.Fatalf("GET %s CSP %q omitted %q", path, csp, expected)
			}
		}
	}
}

func TestSiteCSSIsServedAsAStaticFirstPartyAsset(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/site.css", nil))
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet response = %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if recorder.Body.Len() < 10_000 {
		t.Fatalf("stylesheet unexpectedly small: %d bytes", recorder.Body.Len())
	}
}

func TestPublicSiteDoesNotPresentUnavailableStatsAsZero(t *testing.T) {
	t.Parallel()
	for name, page := range map[string]*template.Template{"/": landingPage, "/explore": explorePage} {
		recorder := httptest.NewRecorder()
		renderPublicPage(recorder, page, publicSiteData{Year: 2026})
		body := recorder.Body.String()
		if !strings.Contains(body, "Stats unavailable") || !strings.Contains(body, "Merchant directory temporarily unavailable") {
			t.Fatalf("GET %s did not distinguish unavailable data: %s", name, body)
		}
		if strings.Contains(body, ">0</strong><small>Stats unavailable") {
			t.Fatalf("GET %s presented unavailable data as zero", name)
		}
	}
}

func TestPublicSiteHasMobileAndKeyboardNavigation(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	testServer(nil, nil, 1).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	for _, required := range []string{`class="skip-link"`, `class="mobile-nav"`, `aria-current="page"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("landing navigation omitted %q", required)
		}
	}
}

func TestLandingPresentsTruthfulStatusAndOpenSourceEvidence(t *testing.T) {
	t.Parallel()
	var body strings.Builder
	if err := landingPage.Execute(&body, publicSiteData{Year: 2026}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"status-unknown", "status unavailable", "OPEN BY DESIGN",
		"https://github.com/ETH402/facilitator", "Apache-2.0 licensed",
	} {
		if !strings.Contains(body.String(), required) {
			t.Fatalf("landing page omitted %q", required)
		}
	}
	if strings.Contains(body.String(), "Ethereum mainnet · operational") {
		t.Fatal("landing page claimed an operational network without available stats")
	}
}
