package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/merchant"
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

func TestMerchantPanelIsSelfContainedAndPrivate(t *testing.T) {
	response := httptest.NewRecorder()
	Dependencies{}.merchantPanel(response, httptest.NewRequest(http.MethodGet, "/merchant", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `src="/merchant/app.js"`) || !strings.Contains(body, `href="/assets/site.css"`) ||
		strings.Contains(body, "https://") {
		t.Fatalf("panel is not self-contained: %s", body)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, required := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, required) {
			t.Fatalf("CSP %q omitted %q", csp, required)
		}
	}
}

func TestMerchantAdminCookieIsHttpOnlyStrictAndSecure(t *testing.T) {
	response := httptest.NewRecorder()
	setMerchantAdminCookie(response, merchant.AdminSession{
		Token: "eth402_admin_" + strings.Repeat("a", 32), ExpiresAt: time.Now().Add(time.Hour),
	})
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure ||
		cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("cookie is not hardened: %+v", cookies)
	}
}
