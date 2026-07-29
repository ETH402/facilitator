package settlement

import (
	"errors"
	"strings"
	"time"
)

// Reason codes recorded against a rejected settlement attempt. They are part of
// the durable record, so they are stable strings rather than error text.
const (
	ReasonPaymentNotFound       = "payment_not_found"
	ReasonPaymentNotVerified    = "payment_not_verified"
	ReasonRecipientNotMerchant  = "recipient_not_registered"
	ReasonAuthorizationExpiring = "authorization_expiring"
	ReasonMerchantQuotaExceeded = "merchant_quota_exceeded"
	ReasonSimulationReverted    = "simulation_reverted"
)

var (
	// ErrPaymentNotFound means no verified payment exists for the identity.
	ErrPaymentNotFound = errors.New("payment record not found")

	// ErrPaymentNotVerified means the payment is not in a state from which
	// settlement may begin. Retrying will not help without operator action.
	ErrPaymentNotVerified = errors.New("payment is not awaiting settlement")

	// ErrRecipientNotMerchant means payTo does not resolve to an active
	// registered merchant. ADR-0004 decision 9: an attacker can always produce a
	// valid authorization moving USDC between wallets they own, which verification
	// cannot reject, so gas is only ever spent on behalf of a party that accepted
	// terms and can be suspended.
	ErrRecipientNotMerchant = errors.New("payment recipient is not an active registered merchant")

	// ErrAuthorizationExpiring means valid_before falls inside the configured
	// margin. EIP-3009 enforces validBefore on-chain, so broadcasting now would
	// pay gas for a transaction that predictably reverts.
	ErrAuthorizationExpiring = errors.New("authorization expires within the settlement margin")

	// ErrMerchantQuotaExceeded means the merchant has already had its allowance
	// of settlement intents inside the rolling window.
	//
	// This is the bound decision 9 rests on. The recipient gate ensures gas is
	// only ever spent for a party that accepted terms and can be suspended, but
	// it says nothing about how much: registration is not Sybil-resistant, so
	// without a quota one registration buys unbounded gas. Quota × gas limit ×
	// max fee per gas is the operator's worst-case exposure per merchant per
	// window.
	ErrMerchantQuotaExceeded = errors.New("merchant settlement quota exceeded for the current window")

	// ErrPaymentUnsettleable means simulation proved the transfer cannot succeed,
	// so the intent was retired without broadcasting. The usual cause is the
	// authorization nonce having been consumed elsewhere between /verify and
	// /settle; broadcasting would buy a certain revert with operator gas.
	ErrPaymentUnsettleable = errors.New("payment cannot settle; simulation reverted")
)

// IntentRequest asks for a durable settlement intent. The margin and clock are
// inputs so the policy is testable and owned by the caller rather than buried in
// persistence.
type IntentRequest struct {
	PaymentIdentity string
	SignerAddress   string
	// PayerSignature is the EIP-3009 signature the payment identity binds. It
	// is persisted atomically with the intent so calldata can be rebuilt after
	// a crash without trusting a caller twice.
	PayerSignature string
	ExpiryMargin   time.Duration
	// Quota bounds how many settlement intents one merchant may commit inside
	// QuotaWindow. Both are inputs rather than persistence-level constants so the
	// policy stays owned by the caller and testable.
	Quota       int
	QuotaWindow time.Duration
	Now         time.Time
}

// Intent is a committed settlement intent. Its nonce is durably owned by the
// transaction identified by TransactionID, which is what allows a crash between
// commit and broadcast to be resolved by reconciliation instead of by signing
// something new (ADR-0004 decision 3).
type Intent struct {
	PaymentID       string
	PaymentIdentity string
	TransactionID   string
	SignerAddress   string
	Nonce           uint64
	// TxHash is populated when a duplicate request targets a transaction whose
	// hash is already durable, including terminal confirmed/reverted payments.
	TxHash string

	// Duplicate reports that an active transaction already existed for this
	// payment, so no new nonce was allocated and the existing intent is returned
	// unchanged. Settlement is idempotent per payment.
	Duplicate bool
}

// Validate checks the request shape before any database work.
func (r IntentRequest) Validate() error {
	var errs []error
	if r.PaymentIdentity == "" {
		errs = append(errs, errors.New("payment identity is required"))
	}
	if r.SignerAddress == "" {
		errs = append(errs, errors.New("signer address is required"))
	}
	if len(r.PayerSignature) != 132 || !strings.HasPrefix(r.PayerSignature, "0x") {
		errs = append(errs, errors.New("payer signature must be 0x-prefixed 65-byte hex"))
	}
	if r.ExpiryMargin <= 0 {
		// Zero would mean broadcasting an authorization that expires this second.
		errs = append(errs, errors.New("expiry margin must be positive"))
	}
	// Both must be positive: a zero quota would admit nothing, and a zero window
	// would silently admit everything, which is the gap this control closes.
	if r.Quota <= 0 {
		errs = append(errs, errors.New("merchant settlement quota must be positive"))
	}
	if r.QuotaWindow <= 0 {
		errs = append(errs, errors.New("merchant settlement quota window must be positive"))
	}
	if r.Now.IsZero() {
		errs = append(errs, errors.New("current time is required"))
	}
	return errors.Join(errs...)
}
