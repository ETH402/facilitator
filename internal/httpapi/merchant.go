package httpapi

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ETH402/facilitator/internal/merchant"
)

func (d Dependencies) merchantRoutes(mux *http.ServeMux) {
	if d.Merchant == nil {
		return
	}
	mux.Handle("POST /v1/merchants/register",
		newRateLimiter(d.RegistrationRate, d.TrustedProxies).middleware(http.HandlerFunc(d.register)))
	mux.Handle("POST /v1/merchants/admin-link",
		newRateLimiter(d.RegistrationRate, d.TrustedProxies).middleware(http.HandlerFunc(d.adminLink)))
	mux.HandleFunc("POST /v1/merchants/verify-email", d.verifyEmail)
	// GET deliberately does not consume the token: mail scanners and link
	// previewers follow links automatically. Only the explicit form POST does.
	mux.HandleFunc("GET /verify-email", d.verifyEmailPage)
	mux.HandleFunc("POST /verify-email", d.verifyEmailPageSubmit)
	mux.HandleFunc("POST /v1/merchants/wallet-challenge", d.walletChallenge)
	mux.HandleFunc("POST /v1/merchants/verify-wallet", d.verifyWallet)
	mux.HandleFunc("GET /v1/me", d.withMerchant(d.fairUse(d.me)))
	mux.HandleFunc("POST /v1/api-keys", d.withMerchant(d.fairUse(d.createKey)))
	mux.HandleFunc("GET /v1/api-keys", d.withMerchant(d.fairUse(d.listKeys)))
	// Revocation is deliberately not fair-use limited: it is the operation a
	// merchant reaches for when a key has leaked, and refusing it because the same
	// merchant has been noisy would keep a compromised credential alive.
	mux.HandleFunc("DELETE /v1/api-keys/{id}", d.withMerchant(d.revokeKey))
	mux.HandleFunc("POST /v1/me/recipient-change", d.withMerchant(d.fairUse(d.recipientChallenge)))
	mux.HandleFunc("POST /v1/me/recipient-change/verify", d.withMerchant(d.fairUse(d.recipientVerify)))
	mux.HandleFunc("POST /v1/admin/merchants/{id}/suspend", d.operator(d.suspend))
	mux.HandleFunc("POST /v1/admin/merchants/{id}/reinstate", d.operator(d.reinstate))
	d.merchantAdminRoutes(mux)
}

func (d Dependencies) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Email       string `json:"business_email"`
		Recipient   string `json:"recipient_address"`
		Website     string `json:"website"`
		Description string `json:"description"`
		Accept      bool   `json:"accept_terms"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeError(w, 400, "invalid_request", "invalid request", requestIDFrom(r.Context()))
		return
	}
	err := d.Merchant.Register(r.Context(), merchant.Registration{Name: in.Name, Email: in.Email, Recipient: in.Recipient, Website: in.Website, Description: in.Description, AcceptTerms: in.Accept}, requestIDFrom(r.Context()))
	if errors.Is(err, merchant.ErrInvalid) {
		writeMerchantError(w, r, err)
		return
	}
	if err != nil {
		d.Logger.ErrorContext(r.Context(), "registration failed", "error", err)
		writeMerchantError(w, r, err)
		return
	}
	d.Metrics.IncRegistration()
	// Deliberately generic for validly-shaped and duplicate registrations.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"})
}

func (d Dependencies) adminLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"business_email"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	if err := d.Merchant.RequestAdminLink(r.Context(), in.Email, requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "email_sent_if_registered"})
}

func (d Dependencies) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	id, err := d.Merchant.VerifyEmail(r.Context(), in.Token, requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	d.Metrics.IncEmailVerification()
	writeJSON(w, 200, map[string]string{"merchant_id": id, "status": "email_verified"})
}

type verifyEmailResult struct {
	Confirm    bool
	Verified   bool
	Token      string
	MerchantID string
}

var verifyEmailPageTemplate = template.Must(template.New("verify-email").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ETH402 email verification</title>
<link rel="stylesheet" href="/assets/site.css"></head>
<body class="app-shell verify-page"><main class="verify-card app-card">
<a class="brand" href="/"><span class="brand-mark" aria-hidden="true"><i></i><b></b></span><span>ETH<span>402</span></span></a>
{{if .Confirm}}
<span class="overline">MERCHANT VERIFICATION</span><h1>Confirm your email</h1>
<p>Select the button below to finish verifying your merchant email address.</p>
<form method="post" action="/verify-email">
<input type="hidden" name="token" value="{{.Token}}">
<button class="button" type="submit">Verify email</button>
</form>
{{else if .Verified}}
<span class="verify-symbol ok" aria-hidden="true">✓</span><span class="overline">VERIFICATION COMPLETE</span><h1>Email verified</h1>
<p>Your merchant email is confirmed. Merchant ID: <code>{{.MerchantID}}</code></p>
<p>Next step: prove control of the recipient wallet in the merchant panel.</p>
<a class="button" href="/merchant">Continue to merchant panel <span aria-hidden="true">→</span></a>
{{else}}
<span class="verify-symbol bad" aria-hidden="true">!</span><span class="overline">LINK UNAVAILABLE</span><h1>Verification link invalid</h1>
<p>This link is invalid, was already used, or has expired. Register again with
the same email address to receive a fresh verification link.</p>
<a class="button button-secondary" href="/merchant">Return to merchant panel</a>
{{end}}
</main></body></html>
`))

// verifyEmailPage renders an explicit confirmation step without looking up or
// consuming the token. Automated mail scanners and link previewers commonly
// issue GET requests, so a GET must not perform the one-time state transition.
func (d Dependencies) verifyEmailPage(w http.ResponseWriter, r *http.Request) {
	renderVerifyEmailPage(w, r.URL.Query().Get("token"))
}

// verifyEmailPageSubmit consumes the token only after an explicit browser form
// submission. API clients continue to use POST /v1/merchants/verify-email.
func (d Dependencies) verifyEmailPageSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderVerifyEmailResult(w, http.StatusBadRequest, verifyEmailResult{})
		return
	}
	id, session, err := d.Merchant.VerifyEmailForAdmin(r.Context(), r.PostFormValue("token"), requestIDFrom(r.Context()))
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, merchant.ErrInvalid) && !errors.Is(err, merchant.ErrNotFound) {
			// Detail stays in the log; the page stays generic because it is public.
			d.Logger.ErrorContext(r.Context(), "email verification page failed", "error", err)
			status = http.StatusInternalServerError
		}
		renderVerifyEmailResult(w, status, verifyEmailResult{})
		return
	}
	setMerchantAdminCookie(w, session)
	d.Metrics.IncEmailVerification()
	renderVerifyEmailResult(w, http.StatusOK, verifyEmailResult{Verified: true, MerchantID: id})
}

func renderVerifyEmailPage(w http.ResponseWriter, token string) {
	if len(token) < 20 {
		renderVerifyEmailResult(w, http.StatusBadRequest, verifyEmailResult{})
		return
	}
	renderVerifyEmailResult(w, http.StatusOK, verifyEmailResult{Confirm: true, Token: token})
}

func renderVerifyEmailResult(w http.ResponseWriter, status int, result verifyEmailResult) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	// The only permitted action and asset are same-origin. The token page loads no
	// scripts, images, fonts, or remote styles.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = verifyEmailPageTemplate.Execute(w, result)
}

func (d Dependencies) walletChallenge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MerchantID string `json:"merchant_id"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	c, err := d.Merchant.WalletChallenge(r.Context(), in.MerchantID, "", "verify-recipient", requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 201, c)
}

func (d Dependencies) verifyWallet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MerchantID  string `json:"merchant_id"`
		ChallengeID string `json:"challenge_id"`
		Message     string `json:"message"`
		Signature   string `json:"signature"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	key, err := d.Merchant.VerifyWallet(r.Context(), in.MerchantID, in.ChallengeID, in.Message, in.Signature, "verify-recipient", requestIDFrom(r.Context()))
	if err != nil {
		d.Metrics.IncWalletVerificationFailure()
		writeMerchantError(w, r, err)
		return
	}
	d.Metrics.IncWalletVerification()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"status": "active", "api_key": key})
}

type merchantHandler func(http.ResponseWriter, *http.Request, merchant.Merchant, string)

func (d Dependencies) withMerchant(next merchantHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeMerchantError(w, r, merchant.ErrUnauthorized)
			return
		}
		token := strings.TrimPrefix(header, "Bearer ")
		m, err := d.Merchant.Authenticate(r.Context(), token)
		if err != nil {
			writeMerchantError(w, r, err)
			return
		}
		next(w, r, m, token)
	}
}

func (d Dependencies) me(w http.ResponseWriter, r *http.Request, m merchant.Merchant, _ string) {
	writeJSON(w, 200, m)
}

func (d Dependencies) createKey(w http.ResponseWriter, r *http.Request, m merchant.Merchant, token string) {
	var in struct {
		Name string `json:"name"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	k, raw, err := d.Merchant.CreateAuthenticatedAPIKey(r.Context(), m.ID, token, in.Name, requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"key": k, "api_key": raw})
}
func (d Dependencies) listKeys(w http.ResponseWriter, r *http.Request, m merchant.Merchant, token string) {
	keys, err := d.Merchant.ListAuthenticatedAPIKeys(r.Context(), m.ID, token)
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}
func (d Dependencies) revokeKey(w http.ResponseWriter, r *http.Request, m merchant.Merchant, token string) {
	if err := d.Merchant.RevokeAuthenticatedAPIKey(r.Context(), m.ID, token, r.PathValue("id"), requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (d Dependencies) recipientChallenge(w http.ResponseWriter, r *http.Request, m merchant.Merchant, token string) {
	var in struct {
		Address string `json:"new_address"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	c, err := d.Merchant.AuthenticatedWalletChallenge(r.Context(), m.ID, token, in.Address, "change-recipient", requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 201, c)
}
func (d Dependencies) recipientVerify(w http.ResponseWriter, r *http.Request, m merchant.Merchant, token string) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Message     string `json:"message"`
		Signature   string `json:"signature"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	_, err := d.Merchant.VerifyAuthenticatedWallet(r.Context(), m.ID, token, in.ChallengeID, in.Message, in.Signature, "change-recipient", requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "recipient_changed"})
}

type operatorHandler func(http.ResponseWriter, *http.Request, string)

func (d Dependencies) operator(next operatorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-Operator-Token")
		if d.OperatorToken == "" || len(provided) != len(d.OperatorToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(d.OperatorToken)) != 1 {
			writeMerchantError(w, r, merchant.ErrUnauthorized)
			return
		}
		next(w, r, "operator")
	}
}
func (d Dependencies) suspend(w http.ResponseWriter, r *http.Request, operator string) {
	var in struct {
		Reason string `json:"reason_code"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	if err := d.Merchant.Suspend(r.Context(), r.PathValue("id"), in.Reason, operator, false, requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "suspended"})
}
func (d Dependencies) reinstate(w http.ResponseWriter, r *http.Request, operator string) {
	if err := d.Merchant.Suspend(r.Context(), r.PathValue("id"), "", operator, true, requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func writeMerchantError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 500, "internal_error", "internal server error"
	switch {
	case errors.Is(err, merchant.ErrInvalid):
		status, code, message = 400, "invalid_request", "invalid request"
	case errors.Is(err, merchant.ErrUnauthorized):
		status, code, message = 401, "unauthorized", "authentication failed"
	case errors.Is(err, merchant.ErrForbidden):
		status, code, message = 403, "forbidden", "access forbidden"
	case errors.Is(err, merchant.ErrNotFound):
		status, code, message = 404, "not_found", "resource not found"
	case errors.Is(err, merchant.ErrConflict):
		status, code, message = 409, "conflict", "request conflicts with current state"
	case errors.Is(err, merchant.ErrThrottled):
		status, code, message = 429, "rate_limited", "request is temporarily restricted"
	}
	if status == http.StatusInternalServerError {
		slog.Default().ErrorContext(r.Context(), "merchant request failed",
			"method", r.Method, "route", r.Pattern,
			"request_id", requestIDFrom(r.Context()), "error", err)
	}
	writeError(w, status, code, message, requestIDFrom(r.Context()))
}
