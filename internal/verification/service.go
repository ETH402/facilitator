package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	eth402x402 "github.com/ETH402/facilitator/internal/x402"
	"github.com/ethereum/go-ethereum/common"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	exactfacilitator "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/facilitator"
	"github.com/x402-foundation/x402/go/v2/types"
)

const (
	USDCName               = "USD Coin"
	USDCVersion            = "2"
	ReasonNonceAlreadyUsed = exactfacilitator.ErrNonceAlreadyUsed
)

var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

var ErrAuthorizationConflict = errors.New("authorization nonce is already associated with another payment")

type Scheme interface {
	Verify(context.Context, types.PaymentPayload, types.PaymentRequirements, *x402.FacilitatorContext) (*x402.VerifyResponse, error)
}

type CodeReader interface {
	GetCode(context.Context, string) ([]byte, error)
	ReadContract(context.Context, string, []byte, string, ...interface{}) (interface{}, error)
}

type Recorder interface {
	RecordVerification(context.Context, Attempt) error
}

type Attempt struct {
	PaymentIdentity string
	Result          string
	ReasonCode      string
	Payment         *Payment
}

type Payment struct {
	Identity    string
	Asset       string
	Payer       string
	Recipient   string
	Amount      string
	Nonce       string
	ValidAfter  time.Time
	ValidBefore time.Time
	PayloadHash string
}

type Request struct {
	X402Version         int                       `json:"x402Version"`
	PaymentPayload      types.PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements types.PaymentRequirements `json:"paymentRequirements"`
}

type Service struct {
	scheme   Scheme
	code     CodeReader
	recorder Recorder
	timeout  time.Duration
}

func New(scheme Scheme, code CodeReader, recorder Recorder, timeout time.Duration) *Service {
	return &Service{scheme: scheme, code: code, recorder: recorder, timeout: timeout}
}

func Supported() types.SupportedResponse {
	return types.SupportedResponse{
		Kinds: []types.SupportedKind{{
			X402Version: 2,
			Scheme:      "exact",
			Network:     config.MainnetNetwork,
			Extra: map[string]interface{}{
				"asset":               config.MainnetUSDC,
				"assetTransferMethod": string(x402evm.AssetTransferMethodEIP3009),
			},
		}},
		Extensions: []string{},
		Signers:    map[string][]string{},
	}
}

func (s *Service) Verify(ctx context.Context, request Request) (*x402.VerifyResponse, error) {
	payment, reason := validateRequest(request)
	if reason != "" {
		if err := s.record(ctx, Attempt{PaymentIdentity: identityOf(payment), Result: "failed", ReasonCode: reason}); err != nil {
			return nil, err
		}
		return &x402.VerifyResponse{IsValid: false, InvalidReason: reason, Payer: payerOf(payment)}, nil
	}

	verifyCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	code, err := s.code.GetCode(verifyCtx, payment.Payer)
	if err != nil {
		return nil, s.unavailable(ctx, payment, fmt.Errorf("check payer code: %w", err))
	}
	if len(code) != 0 {
		const reason = "unsupported_payer_type"
		if err := s.record(ctx, Attempt{PaymentIdentity: payment.Identity, Result: "failed", ReasonCode: reason}); err != nil {
			return nil, err
		}
		return &x402.VerifyResponse{IsValid: false, InvalidReason: reason, Payer: payment.Payer}, nil
	}
	var nonce [32]byte
	nonceBytes, _ := hex.DecodeString(strings.TrimPrefix(payment.Nonce, "0x"))
	copy(nonce[:], nonceBytes)
	used, err := s.code.ReadContract(
		verifyCtx,
		payment.Asset,
		x402evm.AuthorizationStateABI,
		x402evm.FunctionAuthorizationState,
		common.HexToAddress(payment.Payer),
		nonce,
	)
	if err != nil {
		return nil, s.unavailable(ctx, payment, fmt.Errorf("check authorization state: %w", err))
	}
	nonceUsed, ok := used.(bool)
	if !ok {
		return nil, s.unavailable(ctx, payment, fmt.Errorf("unexpected authorizationState result %T", used))
	}
	if nonceUsed {
		if err := s.record(ctx, Attempt{
			PaymentIdentity: payment.Identity,
			Result:          "failed",
			ReasonCode:      ReasonNonceAlreadyUsed,
		}); err != nil {
			return nil, err
		}
		return &x402.VerifyResponse{
			IsValid: false, InvalidReason: ReasonNonceAlreadyUsed, Payer: payment.Payer,
		}, nil
	}

	response, err := s.scheme.Verify(verifyCtx, request.PaymentPayload, request.PaymentRequirements, nil)
	if err != nil {
		var verificationError *x402.VerifyError
		if !errors.As(err, &verificationError) {
			return nil, s.unavailable(ctx, payment, fmt.Errorf("x402 verification: %w", err))
		}
		if recordErr := s.record(ctx, Attempt{
			PaymentIdentity: payment.Identity,
			Result:          "failed",
			ReasonCode:      verificationError.InvalidReason,
		}); recordErr != nil {
			return nil, recordErr
		}
		return &x402.VerifyResponse{
			IsValid:       false,
			InvalidReason: verificationError.InvalidReason,
			Payer:         verificationError.Payer,
		}, nil
	}
	if response == nil || !response.IsValid {
		return nil, s.unavailable(ctx, payment, errors.New("x402 verifier returned an invalid success response"))
	}
	if err := s.record(ctx, Attempt{
		PaymentIdentity: payment.Identity,
		Result:          "verified",
		Payment:         payment,
	}); err != nil {
		if errors.Is(err, ErrAuthorizationConflict) {
			return &x402.VerifyResponse{
				IsValid: false, InvalidReason: ReasonNonceAlreadyUsed, Payer: payment.Payer,
			}, nil
		}
		return nil, err
	}
	return response, nil
}

func (s *Service) RecordInvalidRequest(ctx context.Context) error {
	return s.record(ctx, Attempt{Result: "failed", ReasonCode: "invalid_request"})
}

func (s *Service) record(ctx context.Context, attempt Attempt) error {
	if s.recorder == nil {
		return nil
	}
	if err := s.recorder.RecordVerification(ctx, attempt); err != nil {
		return fmt.Errorf("record verification: %w", err)
	}
	return nil
}

func (s *Service) unavailable(ctx context.Context, payment *Payment, cause error) error {
	recordErr := s.record(ctx, Attempt{
		PaymentIdentity: identityOf(payment),
		Result:          "failed",
		ReasonCode:      "verification_unavailable",
	})
	if recordErr != nil {
		return errors.Join(cause, recordErr)
	}
	return cause
}

func validateRequest(request Request) (*Payment, string) {
	if request.X402Version != 2 || request.PaymentPayload.X402Version != 2 {
		return nil, x402.ErrInvalidVersion
	}
	payload := request.PaymentPayload
	requirements := request.PaymentRequirements
	if !x402.DeepEqual(payload.Accepted, requirements) {
		return nil, "invalid_payment_requirements_mismatch"
	}
	if requirements.Scheme != "exact" || payload.Accepted.Scheme != "exact" {
		return nil, exactfacilitator.ErrInvalidScheme
	}
	if requirements.Network != config.MainnetNetwork {
		return nil, "unsupported_network"
	}
	if !strings.EqualFold(requirements.Asset, config.MainnetUSDC) {
		return nil, "unsupported_asset"
	}
	if len(payload.Extensions) != 0 {
		return nil, "unsupported_extension"
	}
	if method, ok := requirements.Extra["assetTransferMethod"]; ok && method != string(x402evm.AssetTransferMethodEIP3009) {
		return nil, "unsupported_asset_transfer_method"
	}
	if requirements.Extra["name"] != USDCName || requirements.Extra["version"] != USDCVersion {
		return nil, exactfacilitator.ErrMissingEip712Domain
	}
	if !common.IsHexAddress(requirements.PayTo) {
		return nil, exactfacilitator.ErrRecipientMismatch
	}
	requiredAmount, ok := unsignedDecimal(requirements.Amount)
	if !ok || requiredAmount.Sign() <= 0 || requiredAmount.Cmp(maxUint256) > 0 {
		return nil, exactfacilitator.ErrInvalidRequiredAmount
	}
	if x402evm.IsPermit2Payload(payload.Payload) || !x402evm.IsEIP3009Payload(payload.Payload) {
		return nil, exactfacilitator.ErrUnsupportedPayloadType
	}
	evmPayload, err := x402evm.PayloadFromMap(payload.Payload)
	if err != nil {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	authorizationMap, ok := payload.Payload["authorization"].(map[string]interface{})
	if !ok || len(payload.Payload) != 2 || len(authorizationMap) != 6 {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	auth := evmPayload.Authorization
	if !common.IsHexAddress(auth.From) || !common.IsHexAddress(auth.To) ||
		!strings.EqualFold(auth.To, requirements.PayTo) {
		return nil, exactfacilitator.ErrRecipientMismatch
	}
	value, valueOK := unsignedDecimal(auth.Value)
	after, afterOK := unsignedDecimal(auth.ValidAfter)
	before, beforeOK := unsignedDecimal(auth.ValidBefore)
	if !valueOK || !afterOK || !beforeOK ||
		!validUnix(after) || !validUnix(before) || before.Cmp(after) <= 0 {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	if value.Cmp(requiredAmount) != 0 {
		return nil, exactfacilitator.ErrAuthorizationValueMismatch
	}
	nonce, err := hex.DecodeString(strings.TrimPrefix(auth.Nonce, "0x"))
	if err != nil || len(nonce) != 32 || !strings.HasPrefix(auth.Nonce, "0x") {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(evmPayload.Signature, "0x"))
	if err != nil || len(signature) != 65 || !strings.HasPrefix(evmPayload.Signature, "0x") {
		return nil, exactfacilitator.ErrInvalidSignatureFormat
	}
	identity, err := eth402x402.PaymentID(eth402x402.IdentityFields{
		Version: 2, Scheme: requirements.Scheme, Network: requirements.Network,
		Asset: strings.ToLower(requirements.Asset), From: strings.ToLower(auth.From),
		To: strings.ToLower(auth.To), Value: value.String(),
		ValidAfter: after.String(), ValidBefore: before.String(),
		Nonce: strings.ToLower(auth.Nonce), Signature: strings.ToLower(evmPayload.Signature),
	})
	if err != nil {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, exactfacilitator.ErrInvalidPayload
	}
	hash := sha256.Sum256(encodedPayload)
	return &Payment{
		Identity: identity, Asset: strings.ToLower(requirements.Asset),
		Payer: strings.ToLower(auth.From), Recipient: strings.ToLower(auth.To),
		Amount: value.String(), Nonce: strings.ToLower(auth.Nonce),
		ValidAfter:  time.Unix(after.Int64(), 0).UTC(),
		ValidBefore: time.Unix(before.Int64(), 0).UTC(),
		PayloadHash: hex.EncodeToString(hash[:]),
	}, ""
}

func unsignedDecimal(value string) (*big.Int, bool) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return nil, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	number, ok := new(big.Int).SetString(value, 10)
	return number, ok
}

func validUnix(value *big.Int) bool {
	return value.IsInt64() && value.Sign() >= 0 && value.Int64() <= 253402300799
}

func identityOf(payment *Payment) string {
	if payment == nil {
		return ""
	}
	return payment.Identity
}

func payerOf(payment *Payment) string {
	if payment == nil {
		return ""
	}
	return payment.Payer
}
