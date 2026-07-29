package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/policy"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ethereum/go-ethereum/core/types"
)

const testToken = "policy-signer-token-for-tests-0123456789"

// newTestBoundary uses the development signer as the backend. The KMS backend
// cannot run in a unit test, and it is not what these tests are about: the
// handler's job is to decide what gets signed, not how.
func newTestBoundary(t *testing.T) *boundary {
	t.Helper()
	backend, err := signer.NewDevelopment("0x" + strings.Repeat("00", 31) + "01")
	if err != nil {
		t.Fatalf("development signer: %v", err)
	}
	address, err := backend.Address(context.Background())
	if err != nil {
		t.Fatalf("signer address: %v", err)
	}
	return &boundary{
		signer:  backend,
		address: address,
		token:   testToken,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		limits: policy.Limits{
			MaxFeePerGasWei:         big.NewInt(80_000_000_000),
			MaxPriorityFeePerGasWei: big.NewInt(2_000_000_000),
			MaxGasLimit:             250_000,
		},
	}
}

func validRequest() policy.Request {
	return policy.Request{
		Nonce:                7,
		GasLimit:             120_000,
		MaxFeePerGas:         "40000000000",
		MaxPriorityFeePerGas: "1000000000",
		Authorization: policy.Authorization{
			From:        "0x1111111111111111111111111111111111111111",
			To:          "0x2222222222222222222222222222222222222222",
			Value:       "1500000",
			ValidAfter:  "1700000000",
			ValidBefore: "1700003600",
			Nonce:       "0x" + strings.Repeat("ab", 32),
			Signature:   "0x" + strings.Repeat("cd", 64) + "1b",
		},
	}
}

func post(t *testing.T, b *boundary, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	switch typed := body.(type) {
	case string:
		encoded = []byte(typed)
	default:
		var err error
		encoded, err = json.Marshal(typed)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/sign", bytes.NewReader(encoded))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	b.sign(recorder, request)
	return recorder
}

func TestBoundarySignsAValidRequest(t *testing.T) {
	b := newTestBoundary(t)
	recorder := post(t, b, testToken, validRequest())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var response policy.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.SignerAddress != b.address {
		t.Errorf("signer address = %s, want %s", response.SignerAddress, b.address)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(response.RawTransaction, "0x"))
	if err != nil {
		t.Fatalf("decode raw transaction: %v", err)
	}
	var signed types.Transaction
	if err := signed.UnmarshalBinary(raw); err != nil {
		t.Fatalf("decode signed transaction: %v", err)
	}
	if to := signed.To(); to == nil || !strings.EqualFold(to.Hex(), config.MainnetUSDC) {
		t.Errorf("recipient = %v, want %s", to, config.MainnetUSDC)
	}
	if signed.Value().Sign() != 0 {
		t.Errorf("value = %s, want 0", signed.Value())
	}
	if signed.Nonce() != 7 {
		t.Errorf("nonce = %d, want 7", signed.Nonce())
	}
}

// TestBoundaryRequiresTheToken guards the only authentication this service has.
// A missing check here would make it an open signing service to anything that can
// reach its port.
func TestBoundaryRequiresTheToken(t *testing.T) {
	b := newTestBoundary(t)
	for name, token := range map[string]string{
		"no token":       "",
		"wrong token":    "not-the-token-but-the-right-length-xxxx",
		"prefix only":    testToken[:20],
		"trailing extra": testToken + "x",
	} {
		t.Run(name, func(t *testing.T) {
			if got := post(t, b, token, validRequest()).Code; got != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", got)
			}
		})
	}
	// The identity endpoint holds the same line: it discloses the signing address.
	request := httptest.NewRequest(http.MethodGet, "/identity", nil)
	recorder := httptest.NewRecorder()
	b.identity(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated identity status = %d, want 401", recorder.Code)
	}
}

// TestBoundaryRejectsUnknownFields matters more than it looks. A caller sending a
// field this boundary does not understand may believe it constrains something;
// ignoring it silently would make that belief false. The concrete case is a caller
// that thinks it is pinning `to` or `value`.
func TestBoundaryRejectsUnknownFields(t *testing.T) {
	b := newTestBoundary(t)
	body := `{"nonce":7,"gasLimit":120000,"maxFeePerGas":"40000000000",
	  "maxPriorityFeePerGas":"1000000000","to":"0x4444444444444444444444444444444444444444",
	  "authorization":{"from":"0x1111111111111111111111111111111111111111",
	  "to":"0x2222222222222222222222222222222222222222","value":"1500000",
	  "validAfter":"1700000000","validBefore":"1700003600",
	  "nonce":"0x` + strings.Repeat("ab", 32) + `","signature":"0x` + strings.Repeat("cd", 64) + `1b"}}`
	if got := post(t, b, testToken, body).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unrecognized field", got)
	}
}

func TestBoundaryRejectsTrailingJSON(t *testing.T) {
	b := newTestBoundary(t)
	encoded, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	body := string(encoded) + `{"nonce":8}`
	if got := post(t, b, testToken, body).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for trailing JSON", got)
	}
}

func TestBoundaryRefusesOverItsCeilings(t *testing.T) {
	b := newTestBoundary(t)
	over := validRequest()
	over.MaxFeePerGas = "90000000000"
	recorder := post(t, b, testToken, over)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "ceiling") {
		t.Errorf("body should name the ceiling that was exceeded, got %s", recorder.Body)
	}
}

func TestBoundaryRefusesMalformedRequests(t *testing.T) {
	b := newTestBoundary(t)
	for name, body := range map[string]any{
		"not json":              "{",
		"empty body":            "",
		"missing authorization": policy.Request{Nonce: 1, GasLimit: 120_000, MaxFeePerGas: "1", MaxPriorityFeePerGas: "1"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := post(t, b, testToken, body).Code; got != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", got)
			}
		})
	}
}

func TestBoundaryDoesNotLogOrEchoAuthorization(t *testing.T) {
	var logs bytes.Buffer
	b := newTestBoundary(t)
	b.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	request := validRequest()
	request.Authorization.Signature = "sensitive-malformed-signature"

	recorder := post(t, b, testToken, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	combined := logs.String() + recorder.Body.String()
	for name, sensitive := range map[string]string{
		"payer":      request.Authorization.From,
		"recipient":  request.Authorization.To,
		"amount":     request.Authorization.Value,
		"auth nonce": request.Authorization.Nonce,
		"signature":  request.Authorization.Signature,
	} {
		if strings.Contains(combined, sensitive) {
			t.Errorf("%s authorization data leaked in log or response", name)
		}
	}
}

func TestBoundarySuccessLogOmitsAuthorization(t *testing.T) {
	var logs bytes.Buffer
	b := newTestBoundary(t)
	b.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	request := validRequest()

	recorder := post(t, b, testToken, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	for name, sensitive := range map[string]string{
		"payer":      request.Authorization.From,
		"recipient":  request.Authorization.To,
		"amount":     request.Authorization.Value,
		"auth nonce": request.Authorization.Nonce,
		"signature":  request.Authorization.Signature,
	} {
		if strings.Contains(logs.String(), sensitive) {
			t.Errorf("%s authorization data leaked in success log", name)
		}
	}
}

// TestUnconfiguredBoundaryRefusesEverything covers the deployment mistake with the
// largest blast radius: ceilings left unset must sign nothing.
func TestUnconfiguredBoundaryRefusesEverything(t *testing.T) {
	b := newTestBoundary(t)
	b.limits = policy.Limits{}
	if got := post(t, b, testToken, validRequest()).Code; got != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", got)
	}
}

func TestLimitsFromEnvRefusesToDefault(t *testing.T) {
	// Each variable absent in turn must fail, so no ceiling can be silently
	// defaulted into existence.
	full := map[string]string{
		"POLICYSIGNER_MAX_FEE_PER_GAS_WEI":          "80000000000",
		"POLICYSIGNER_MAX_PRIORITY_FEE_PER_GAS_WEI": "2000000000",
		"POLICYSIGNER_MAX_GAS_LIMIT":                "250000",
	}
	for omitted := range full {
		t.Run("without "+omitted, func(t *testing.T) {
			for key, value := range full {
				if key == omitted {
					t.Setenv(key, "")
					continue
				}
				t.Setenv(key, value)
			}
			if _, err := limitsFromEnv(); err == nil {
				t.Errorf("accepted configuration missing %s", omitted)
			}
		})
	}
	for key, value := range full {
		t.Setenv(key, value)
	}
	limits, err := limitsFromEnv()
	if err != nil {
		t.Fatalf("complete configuration rejected: %v", err)
	}
	if limits.MaxGasLimit != 250_000 {
		t.Errorf("gas limit = %d, want 250000", limits.MaxGasLimit)
	}
}
