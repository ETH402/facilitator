package verification

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	exactfacilitator "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/facilitator"
	"github.com/x402-foundation/x402/go/v2/types"
)

type memoryRecorder struct{ attempts []Attempt }

func (r *memoryRecorder) RecordVerification(_ context.Context, attempt Attempt) error {
	r.attempts = append(r.attempts, attempt)
	return nil
}

type conflictRecorder struct{}

func (conflictRecorder) RecordVerification(_ context.Context, attempt Attempt) error {
	if attempt.Result == "verified" {
		return ErrAuthorizationConflict
	}
	return nil
}

type verificationSigner struct {
	asset     string
	nonceUsed bool
}

func (s verificationSigner) GetAddresses() []string { return nil }
func (s verificationSigner) GetCode(_ context.Context, address string) ([]byte, error) {
	if strings.EqualFold(address, s.asset) {
		return []byte{1}, nil
	}
	return nil, nil
}
func (s verificationSigner) ReadContract(_ context.Context, _ string, _ []byte, function string, _ ...interface{}) (interface{}, error) {
	if function == x402evm.FunctionAuthorizationState {
		return s.nonceUsed, nil
	}
	return nil, nil
}
func (verificationSigner) VerifyTypedData(
	_ context.Context, address string, domain x402evm.TypedDataDomain,
	fields map[string][]x402evm.TypedDataField, primary string,
	message map[string]interface{}, signature []byte,
) (bool, error) {
	return x402evm.VerifyEOATypedData(address, domain, fields, primary, message, signature)
}
func (verificationSigner) WriteContract(context.Context, string, []byte, string, []byte, ...interface{}) (string, error) {
	return "", nil
}
func (verificationSigner) SendTransaction(context.Context, string, []byte) (string, error) {
	return "", nil
}
func (verificationSigner) WaitForTransactionReceipt(context.Context, string) (*x402evm.TransactionReceipt, error) {
	return nil, nil
}
func (verificationSigner) GetBalance(context.Context, string, string) (*big.Int, error) {
	return big.NewInt(10_000_000), nil
}
func (verificationSigner) GetChainID(context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}

func TestVerifyWithOfficialExactScheme(t *testing.T) {
	request, signer := signedRequest(t)
	recorder := &memoryRecorder{}
	service := New(exactfacilitator.NewExactEvmScheme(signer, nil), signer, recorder, time.Second)

	response, err := service.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !response.IsValid || response.Payer != request.PaymentPayload.Payload["authorization"].(map[string]interface{})["from"] {
		t.Fatalf("unexpected response: %#v", response)
	}
	if len(recorder.attempts) != 1 || recorder.attempts[0].Result != "verified" ||
		recorder.attempts[0].Payment == nil {
		t.Fatalf("unexpected recorded attempt: %#v", recorder.attempts)
	}
}

func TestVerifyRejectsScopedAndMalformedPayments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
		reason string
	}{
		{"version", func(r *Request) { r.X402Version = 1 }, "invalid_version"},
		{"network", func(r *Request) {
			r.PaymentRequirements.Network = "eip155:8453"
			r.PaymentPayload.Accepted.Network = "eip155:8453"
		}, "unsupported_network"},
		{"asset", func(r *Request) {
			r.PaymentRequirements.Asset = "0x0000000000000000000000000000000000000001"
			r.PaymentPayload.Accepted.Asset = r.PaymentRequirements.Asset
		}, "unsupported_asset"},
		{"requirements mismatch", func(r *Request) {
			r.PaymentRequirements.Amount = "2"
		}, "invalid_payment_requirements_mismatch"},
		{"permit2", func(r *Request) {
			r.PaymentPayload.Payload = map[string]interface{}{"signature": "0x01", "permit2Authorization": map[string]interface{}{}}
		}, exactfacilitator.ErrUnsupportedPayloadType},
		{"wrong domain", func(r *Request) {
			r.PaymentRequirements.Extra["name"] = "USDC"
			r.PaymentPayload.Accepted.Extra["name"] = "USDC"
		}, exactfacilitator.ErrMissingEip712Domain},
		{"wrong amount", func(r *Request) {
			r.PaymentRequirements.Amount = "2"
			r.PaymentPayload.Accepted.Amount = "2"
		}, exactfacilitator.ErrAuthorizationValueMismatch},
		{"wrong recipient", func(r *Request) {
			r.PaymentRequirements.PayTo = "0x4444444444444444444444444444444444444444"
			r.PaymentPayload.Accepted.PayTo = r.PaymentRequirements.PayTo
		}, exactfacilitator.ErrRecipientMismatch},
		{"expired", func(r *Request) {
			auth := r.PaymentPayload.Payload["authorization"].(map[string]interface{})
			auth["validBefore"] = "1"
		}, exactfacilitator.ErrValidBeforeExpired},
		{"future", func(r *Request) {
			auth := r.PaymentPayload.Payload["authorization"].(map[string]interface{})
			auth["validAfter"] = big.NewInt(time.Now().Add(time.Minute).Unix()).String()
		}, exactfacilitator.ErrValidAfterInFuture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, signer := signedRequest(t)
			test.mutate(&request)
			recorder := &memoryRecorder{}
			service := New(exactfacilitator.NewExactEvmScheme(signer, nil), signer, recorder, time.Second)
			response, err := service.Verify(context.Background(), request)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if response.IsValid || response.InvalidReason != test.reason {
				t.Fatalf("response = %#v, want reason %q", response, test.reason)
			}
			if len(recorder.attempts) != 1 || recorder.attempts[0].Result != "failed" {
				t.Fatalf("attempts = %#v", recorder.attempts)
			}
		})
	}
}

func TestVerifyRejectsWrongSignature(t *testing.T) {
	request, signer := signedRequest(t)
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signRequest(t, &request, other)
	service := New(exactfacilitator.NewExactEvmScheme(signer, nil), signer, &memoryRecorder{}, time.Second)
	response, err := service.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if response.IsValid || response.InvalidReason != exactfacilitator.ErrInvalidSignature {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestVerifyRejectsUsedAuthorization(t *testing.T) {
	request, signer := signedRequest(t)
	signer.nonceUsed = true
	service := New(exactfacilitator.NewExactEvmScheme(signer, nil), signer, &memoryRecorder{}, time.Second)
	response, err := service.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if response.IsValid || response.InvalidReason != exactfacilitator.ErrNonceAlreadyUsed {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestVerifyMapsConcurrentNonceConflictToReplay(t *testing.T) {
	request, signer := signedRequest(t)
	service := New(exactfacilitator.NewExactEvmScheme(signer, nil), signer, conflictRecorder{}, time.Second)
	response, err := service.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if response.IsValid || response.InvalidReason != exactfacilitator.ErrNonceAlreadyUsed {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestSupportedIsNarrow(t *testing.T) {
	response := Supported()
	if len(response.Kinds) != 1 || response.Kinds[0].Network != config.MainnetNetwork ||
		response.Kinds[0].Scheme != "exact" || len(response.Extensions) != 0 ||
		len(response.Signers) != 0 {
		t.Fatalf("unexpected supported response: %#v", response)
	}
}

func signedRequest(t *testing.T) (Request, verificationSigner) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payer := crypto.PubkeyToAddress(key.PublicKey).Hex()
	recipient := "0x1111111111111111111111111111111111111111"
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: "1000000", PayTo: recipient, MaxTimeoutSeconds: 300,
		Extra: map[string]interface{}{
			"name": USDCName, "version": USDCVersion, "assetTransferMethod": "eip3009",
		},
	}
	request := Request{
		X402Version: 2,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]interface{}{
				"signature": "",
				"authorization": map[string]interface{}{
					"from": payer, "to": recipient, "value": "1000000",
					"validAfter": "0", "validBefore": big.NewInt(time.Now().Add(5 * time.Minute).Unix()).String(),
					"nonce": "0x" + strings.Repeat("42", 32),
				},
			},
		},
		PaymentRequirements: requirements,
	}
	signRequest(t, &request, key)
	return request, verificationSigner{asset: config.MainnetUSDC}
}

func signRequest(t *testing.T, request *Request, key *ecdsa.PrivateKey) {
	t.Helper()
	authorization := request.PaymentPayload.Payload["authorization"].(map[string]interface{})
	auth := x402evm.ExactEIP3009Authorization{
		From: authorization["from"].(string), To: authorization["to"].(string),
		Value: authorization["value"].(string), ValidAfter: authorization["validAfter"].(string),
		ValidBefore: authorization["validBefore"].(string), Nonce: authorization["nonce"].(string),
	}
	hash, err := x402evm.HashEIP3009Authorization(
		auth, big.NewInt(1), common.HexToAddress(config.MainnetUSDC).Hex(), USDCName, USDCVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatal(err)
	}
	request.PaymentPayload.Payload["signature"] = "0x" + common.Bytes2Hex(signature)
}

func FuzzVerifyRequestValidation(f *testing.F) {
	request, _ := signedRequestForFuzz(f)
	seed, err := json.Marshal(request)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"x402Version":2,"paymentPayload":{"payload":{"authorization":[]}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var candidate Request
		if json.Unmarshal(data, &candidate) != nil {
			return
		}
		_, _ = ParseRequest(candidate)
	})
}

func signedRequestForFuzz(tb testing.TB) (Request, verificationSigner) {
	tb.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		tb.Fatal(err)
	}
	payer := crypto.PubkeyToAddress(key.PublicKey).Hex()
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: "1", PayTo: "0x1111111111111111111111111111111111111111",
		MaxTimeoutSeconds: 60,
		Extra:             map[string]interface{}{"name": USDCName, "version": USDCVersion},
	}
	request := Request{
		X402Version: 2, PaymentRequirements: requirements,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]interface{}{
				"signature": "0x" + strings.Repeat("11", 65),
				"authorization": map[string]interface{}{
					"from": payer, "to": requirements.PayTo, "value": "1",
					"validAfter": "0", "validBefore": "9999999999",
					"nonce": "0x" + strings.Repeat("22", 32),
				},
			},
		},
	}
	return request, verificationSigner{asset: config.MainnetUSDC}
}
