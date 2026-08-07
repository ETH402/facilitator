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
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self'") ||
		strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("verification CSP is not first-party only: %q", csp)
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
		strings.Contains(body, `src="https://`) || strings.Contains(body, `href="https://`) {
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

func TestMerchantPanelSendsReplacementAddressOnlyWhilePending(t *testing.T) {
	response := httptest.NewRecorder()
	Dependencies{}.merchantPanelJS(response, httptest.NewRequest(http.MethodGet, "/merchant/app.js", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	script := response.Body.String()
	for _, required := range []string{
		"pending=merchant.status==='pending'",
		"JSON.stringify(pending?{address:account}:{})",
		"Connected wallet does not match the current recipient",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("merchant wallet flow omitted %q", required)
		}
	}
}

func TestMerchantPanelClearsSecretsAndDoesNotClaimFailedLogout(t *testing.T) {
	response := httptest.NewRecorder()
	Dependencies{}.merchantPanelJS(response, httptest.NewRequest(http.MethodGet, "/merchant/app.js", nil))
	script := response.Body.String()
	for _, required := range []string{
		"function clearSecret()",
		"merchant=null;walletAuthenticated=false;clearSecret()",
		"await api('/merchant/api/logout',{method:'POST'});showSignedOut()",
		"Sign-out failed. Your session is still active",
		"response.status===401&&path.startsWith('/merchant/api/')",
		"showSecret(activated.api_key,load)",
		"showSecret(data.api_key,loadKeys)",
		"const refresh=secretRefresh;clearSecret();if(refresh)",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("merchant session hardening omitted %q", required)
		}
	}
	if strings.Contains(script, "showSecret(activated.api_key);notice") ||
		strings.Contains(script, "showSecret(data.api_key);$('key-name').value='';await loadKeys()") {
		t.Fatal("one-time API key is followed by an authenticated refresh before acknowledgement")
	}
}

func TestMerchantPanelIncludesAccessibleNavigationAndLoadingStates(t *testing.T) {
	response := httptest.NewRecorder()
	Dependencies{}.merchantPanel(response, httptest.NewRequest(http.MethodGet, "/merchant", nil))
	body := response.Body.String()
	for _, required := range []string{
		`role="tablist"`, `role="tab"`, `role="tabpanel"`, `aria-selected="true"`,
		`for="key-name"`, `id="keys-empty"`, `class="skip-link"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("merchant panel accessibility omitted %q", required)
		}
	}
}

func TestMerchantPanelPresentsSecurityModelAndNeutralSessionExpiry(t *testing.T) {
	t.Parallel()
	page := httptest.NewRecorder()
	Dependencies{}.merchantPanel(page, httptest.NewRequest(http.MethodGet, "/merchant", nil))
	for _, required := range []string{"Email verified", "Wallet authorized", "Direct settlement", "No password to remember."} {
		if !strings.Contains(page.Body.String(), required) {
			t.Fatalf("merchant onboarding omitted %q", required)
		}
	}

	script := httptest.NewRecorder()
	Dependencies{}.merchantPanelJS(script, httptest.NewRequest(http.MethodGet, "/merchant/app.js", nil))
	if !strings.Contains(script.Body.String(), "notice(message,false,false,!!message)") {
		t.Fatal("an expired session is still presented as a form error")
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
