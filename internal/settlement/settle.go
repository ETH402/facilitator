package settlement

import (
	"github.com/ETH402/facilitator/internal/verification"
)

// SettleRequest is the x402 facilitator settle request. It has the same shape
// as a verification request and is parsed by the same strict parser, so a
// settlement can never describe a payment verification would have rejected.
type SettleRequest = verification.Request

// Stable errorReason strings returned in the x402 SettleResponse wire format.
// Distinct from the intent.go reason codes, which are the durable audit record;
// these are the public contract and must not change without an OpenAPI bump.
const (
	WireReasonPaymentNotFound       = ReasonPaymentNotFound
	WireReasonPaymentNotVerified    = ReasonPaymentNotVerified
	WireReasonRecipientNotMerchant  = ReasonRecipientNotMerchant
	WireReasonAuthorizationExpiring = ReasonAuthorizationExpiring
	WireReasonMerchantQuotaExceeded = ReasonMerchantQuotaExceeded
	WireReasonSimulationReverted    = ReasonSimulationReverted

	// WireReasonSettlementUnavailable means the facilitator could not complete
	// the request itself (database, signer, or RPC unavailable). Retrying is
	// safe: settlement is idempotent per payment.
	WireReasonSettlementUnavailable = "settlement_unavailable"

	// WireReasonBroadcastFailed means the transaction could not be handed to
	// the network. The durable intent remains and the broadcast worker keeps
	// retrying; the payment may still settle.
	WireReasonBroadcastFailed = "broadcast_failed"
)
