package httpapi

import (
	"context"
	"strconv"
	"time"

	"net/http"

	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/store"
)

// FairUseAccountant records one authenticated request against a merchant's
// allowance. *store.Store satisfies it via CountMerchantRequest.
type FairUseAccountant interface {
	CountMerchantRequest(ctx context.Context, merchantID string, limit int64, window time.Duration, now time.Time) (store.FairUse, error)
}

// fairUse enforces a per-merchant allowance on authenticated endpoints.
//
// It keys on the **merchant**, not the API key. Keying on the key would let a
// merchant multiply its allowance by minting more keys, which is a self-service
// operation — the limit would not be a limit.
//
// It is applied only where identity is *proven* by an API key. It is deliberately
// not applied to /verify or /settle: those are unauthenticated, and the merchant
// there is derived from the caller-supplied `payTo`. A per-merchant limit on an
// attacker-chosen identity is not a control, it is a weapon — anyone could exhaust
// an innocent merchant's allowance and deny them service. Those endpoints are
// bounded by the per-IP limiter and, for the resource the facilitator actually
// pays for, by the settlement quotas.
func (d Dependencies) fairUse(next merchantHandler) merchantHandler {
	return func(w http.ResponseWriter, r *http.Request, m merchant.Merchant) {
		if d.FairUse == nil || d.MerchantRequestsPerWindow <= 0 || d.FairUseWindow <= 0 {
			next(w, r, m)
			return
		}
		usage, err := d.FairUse.CountMerchantRequest(r.Context(), m.ID,
			d.MerchantRequestsPerWindow, d.FairUseWindow, time.Now())
		if err != nil {
			// Accounting failed, so the allowance is unknown. Serving the request is
			// the right call: this is a fair-use control, not an authorization
			// decision, and failing closed would turn a bookkeeping outage into an
			// outage for every paying merchant.
			d.Logger.ErrorContext(r.Context(), "fair-use accounting failed; allowing the request",
				"merchant_id", m.ID, "error", err)
			next(w, r, m)
			return
		}
		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(usage.Limit, 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(usage.Remaining, 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(usage.ResetsAt.Unix(), 10))
		if usage.Exceeded {
			retryAfter := max(1, int(time.Until(usage.ResetsAt).Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			if d.Metrics != nil {
				d.Metrics.IncFairUseRefusal()
			}
			d.Logger.InfoContext(r.Context(), "merchant fair-use allowance exceeded",
				"merchant_id", m.ID, "used", usage.Used, "limit", usage.Limit)
			writeError(w, http.StatusTooManyRequests, "fair_use_exceeded",
				"merchant request allowance exceeded for the current window", requestIDFrom(r.Context()))
			return
		}
		next(w, r, m)
	}
}
