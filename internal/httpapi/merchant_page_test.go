package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyEmailPageRequiresExplicitSubmission(t *testing.T) {
	token := strings.Repeat("a", 32) + `\"><script>alert(1)</script>`
	response := httptest.NewRecorder()

	renderVerifyEmailPage(response, token)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Confirm your email") || !strings.Contains(body, `method="post"`) {
		t.Fatalf("confirmation form missing: %s", body)
	}
	if strings.Contains(body, "<script>") {
		t.Fatalf("token was not HTML-escaped: %s", body)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("X-Robots-Tag = %q, want noindex", got)
	}
}

func TestVerifyEmailPageRejectsMalformedToken(t *testing.T) {
	response := httptest.NewRecorder()

	renderVerifyEmailPage(response, "short")

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "Verification link invalid") {
		t.Fatalf("generic invalid page missing: %s", response.Body.String())
	}
}
