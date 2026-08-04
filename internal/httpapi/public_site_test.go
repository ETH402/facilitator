package httpapi

import (
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
