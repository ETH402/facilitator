package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/ETH402/facilitator/internal/merchant"
)

const merchantAdminCookie = "eth402_admin"

func (d Dependencies) merchantAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /merchant", d.merchantPanel)
	mux.HandleFunc("GET /merchant/app.js", d.merchantPanelJS)
	mux.HandleFunc("GET /merchant/api/session", d.withAdmin(false, d.adminSession))
	mux.HandleFunc("POST /merchant/api/logout", d.adminLogout)
	mux.HandleFunc("POST /merchant/api/wallet-challenge", d.withAdmin(false, d.adminWalletChallenge))
	mux.HandleFunc("POST /merchant/api/verify-wallet", d.withAdmin(false, d.adminVerifyWallet))
	mux.HandleFunc("POST /merchant/api/recipient-challenge", d.withAdmin(true, d.adminRecipientChallenge))
	mux.HandleFunc("POST /merchant/api/verify-recipient-change", d.withAdmin(true, d.adminVerifyRecipientChange))
	mux.HandleFunc("GET /merchant/api/stats", d.withAdmin(true, d.adminStats))
	mux.HandleFunc("PUT /merchant/api/stats-consent", d.withAdmin(true, d.adminStatsConsent))
	mux.HandleFunc("PUT /merchant/api/public-profile", d.withAdmin(true, d.adminPublicProfileConsent))
	mux.HandleFunc("GET /merchant/api/api-keys", d.withAdmin(true, d.adminListKeys))
	mux.HandleFunc("POST /merchant/api/api-keys", d.withAdmin(true, d.adminCreateKey))
	mux.HandleFunc("DELETE /merchant/api/api-keys/{id}", d.withAdmin(true, d.adminRevokeKey))
}

func setMerchantAdminCookie(w http.ResponseWriter, session merchant.AdminSession) {
	http.SetCookie(w, &http.Cookie{
		Name: merchantAdminCookie, Value: session.Token, Path: "/",
		Expires: session.ExpiresAt, MaxAge: max(1, int(time.Until(session.ExpiresAt).Seconds())),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearMerchantAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: merchantAdminCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

type adminHandler func(http.ResponseWriter, *http.Request, merchant.AdminPrincipal, string)

func (d Dependencies) withAdmin(requireWallet bool, next adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		cookie, err := r.Cookie(merchantAdminCookie)
		if err != nil {
			writeMerchantError(w, r, merchant.ErrUnauthorized)
			return
		}
		principal, err := d.Merchant.AuthenticateAdmin(r.Context(), cookie.Value)
		if err != nil {
			clearMerchantAdminCookie(w)
			writeMerchantError(w, r, err)
			return
		}
		if requireWallet && (!principal.WalletAuthenticated || principal.Merchant.Status != "active") {
			writeMerchantError(w, r, merchant.ErrForbidden)
			return
		}
		d.fairUse(func(w http.ResponseWriter, r *http.Request, _ merchant.Merchant, _ string) {
			next(w, r, principal, cookie.Value)
		})(w, r, principal.Merchant, "")
	}
}

func (d Dependencies) adminSession(w http.ResponseWriter, _ *http.Request, principal merchant.AdminPrincipal, _ string) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"merchant": principal.Merchant, "wallet_authenticated": principal.WalletAuthenticated,
	})
}

func (d Dependencies) adminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(merchantAdminCookie); err == nil {
		_ = d.Merchant.RevokeAdminSession(r.Context(), cookie.Value)
	}
	clearMerchantAdminCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (d Dependencies) adminWalletChallenge(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, _ string) {
	m := principal.Merchant
	var in struct {
		Address string `json:"address"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	action := "authenticate-admin"
	var challenge merchant.Challenge
	var err error
	if m.Status == "pending" && m.EmailVerifiedAt != nil && m.WalletVerifiedAt == nil {
		if in.Address == "" {
			challenge, err = d.Merchant.WalletChallenge(r.Context(), m.ID, "", "verify-recipient", requestIDFrom(r.Context()))
		} else {
			challenge, err = d.Merchant.PendingRecipientChallenge(r.Context(), m.ID, in.Address, requestIDFrom(r.Context()))
		}
	} else if m.Status != "active" || m.WalletVerifiedAt == nil {
		writeMerchantError(w, r, merchant.ErrConflict)
		return
	} else {
		if in.Address != "" {
			writeMerchantError(w, r, merchant.ErrInvalid)
			return
		}
		challenge, err = d.Merchant.WalletChallenge(r.Context(), m.ID, "", action, requestIDFrom(r.Context()))
	}
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, challenge)
}

func (d Dependencies) adminVerifyWallet(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Message     string `json:"message"`
		Signature   string `json:"signature"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	var key string
	var err error
	switch m.Status {
	case "pending":
		key, err = d.Merchant.VerifyWallet(r.Context(), m.ID, in.ChallengeID, in.Message,
			in.Signature, "verify-recipient", requestIDFrom(r.Context()))
		if err == nil {
			if markErr := d.Merchant.MarkAdminSessionWalletVerified(r.Context(), m.ID, sessionToken); markErr != nil {
				// Activation and one-time API-key issuance already committed. Never
				// discard that key because session elevation raced expiry; return it
				// once and require a fresh email/wallet login for later administration.
				d.Logger.ErrorContext(r.Context(), "merchant activated but admin session elevation failed",
					"merchant_id", m.ID, "error", markErr)
			}
		}
	case "active":
		err = d.Merchant.VerifyAdminWallet(r.Context(), m.ID, sessionToken, in.ChallengeID,
			in.Message, in.Signature, requestIDFrom(r.Context()))
	default:
		err = merchant.ErrForbidden
	}
	if err != nil {
		d.Metrics.IncWalletVerificationFailure()
		writeMerchantError(w, r, err)
		return
	}
	d.Metrics.IncWalletVerification()
	result := map[string]string{"status": "authenticated"}
	if key != "" {
		result["status"], result["api_key"] = "active", key
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func (d Dependencies) adminRecipientChallenge(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, _ string) {
	var in struct {
		Address string `json:"new_address"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	challenge, err := d.Merchant.WalletChallenge(r.Context(), principal.Merchant.ID, in.Address,
		"change-recipient", requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, challenge)
}

func (d Dependencies) adminVerifyRecipientChange(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Message     string `json:"message"`
		Signature   string `json:"signature"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	err := d.Merchant.VerifyAdminRecipientChange(r.Context(), principal.Merchant.ID, sessionToken,
		in.ChallengeID, in.Message, in.Signature, requestIDFrom(r.Context()))
	if err != nil {
		d.Metrics.IncWalletVerificationFailure()
		writeMerchantError(w, r, err)
		return
	}
	d.Metrics.IncWalletVerification()
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "recipient_changed"})
}

func (d Dependencies) adminStats(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	result, err := d.Merchant.AdminStats(r.Context(), m.ID, sessionToken)
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func (d Dependencies) adminStatsConsent(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if DecodeStrict(w, r, &in) != nil || in.Enabled == nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	optedInAt, err := d.Merchant.SetAdminStatsConsent(r.Context(), m.ID, sessionToken, *in.Enabled, requestIDFrom(r.Context()))
	if err != nil {
		if !errors.Is(err, merchant.ErrInvalid) && !errors.Is(err, merchant.ErrForbidden) &&
			!errors.Is(err, merchant.ErrNotFound) {
			d.Logger.ErrorContext(r.Context(), "merchant stats consent update failed",
				"merchant_id", m.ID, "error", err)
		}
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *in.Enabled, "opted_in_at": optedInAt})
}

func (d Dependencies) adminPublicProfileConsent(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if DecodeStrict(w, r, &in) != nil || in.Enabled == nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	optedInAt, err := d.Merchant.SetAdminPublicProfileConsent(r.Context(), m.ID, sessionToken, *in.Enabled, requestIDFrom(r.Context()))
	if err != nil {
		if !errors.Is(err, merchant.ErrInvalid) && !errors.Is(err, merchant.ErrForbidden) &&
			!errors.Is(err, merchant.ErrNotFound) {
			d.Logger.ErrorContext(r.Context(), "merchant public profile consent update failed",
				"merchant_id", m.ID, "error", err)
		}
		writeMerchantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": *in.Enabled, "opted_in_at": optedInAt})
}

func (d Dependencies) adminListKeys(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	keys, err := d.Merchant.ListAdminAPIKeys(r.Context(), m.ID, sessionToken)
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (d Dependencies) adminCreateKey(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	var in struct {
		Name string `json:"name"`
	}
	if DecodeStrict(w, r, &in) != nil {
		writeMerchantError(w, r, merchant.ErrInvalid)
		return
	}
	key, raw, err := d.Merchant.CreateAdminAPIKey(r.Context(), m.ID, sessionToken, in.Name, requestIDFrom(r.Context()))
	if err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "api_key": raw})
}

func (d Dependencies) adminRevokeKey(w http.ResponseWriter, r *http.Request, principal merchant.AdminPrincipal, sessionToken string) {
	m := principal.Merchant
	if err := d.Merchant.RevokeAdminAPIKey(r.Context(), m.ID, sessionToken, r.PathValue("id"), requestIDFrom(r.Context())); err != nil {
		writeMerchantError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
