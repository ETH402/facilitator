package httpapi

import (
	"crypto/subtle"
	"errors"
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
	mux.HandleFunc("POST /v1/merchants/verify-email", d.verifyEmail)
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
	writeJSON(w, 200, map[string]string{"status": "active", "api_key": key})
}

type merchantHandler func(http.ResponseWriter, *http.Request, merchant.Merchant)

func (d Dependencies) withMerchant(next merchantHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeMerchantError(w, r, merchant.ErrUnauthorized)
			return
		}
		m, err := d.Merchant.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			writeMerchantError(w, r, err)
			return
		}
		next(w, r, m)
	}
}

func (d Dependencies) me(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	writeJSON(w, 200, m)
}

func (d Dependencies) createKey(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	var in struct {
		Name string `json:"name"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	k, raw, err := d.Merchant.CreateAPIKey(r.Context(), m.ID, in.Name, requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"key": k, "api_key": raw})
}
func (d Dependencies) listKeys(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	keys, err := d.Merchant.ListAPIKeys(r.Context(), m.ID)
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}
func (d Dependencies) revokeKey(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	if err := d.Merchant.RevokeAPIKey(r.Context(), m.ID, r.PathValue("id"), requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (d Dependencies) recipientChallenge(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	var in struct {
		Address string `json:"new_address"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	c, err := d.Merchant.WalletChallenge(r.Context(), m.ID, in.Address, "change-recipient", requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, 201, c)
}
func (d Dependencies) recipientVerify(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Message     string `json:"message"`
		Signature   string `json:"signature"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	_, err := d.Merchant.VerifyWallet(r.Context(), m.ID, in.ChallengeID, in.Message, in.Signature, "change-recipient", requestIDFrom(r.Context()))
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
	writeError(w, status, code, message, requestIDFrom(r.Context()))
}
