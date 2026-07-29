package policy

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ETH402/facilitator/internal/config"
)

func testLimits() Limits {
	return Limits{
		MaxFeePerGasWei:         big.NewInt(80_000_000_000),
		MaxPriorityFeePerGasWei: big.NewInt(2_000_000_000),
		MaxGasLimit:             250_000,
	}
}

func testAuthorization() Authorization {
	return Authorization{
		From:        "0x1111111111111111111111111111111111111111",
		To:          "0x2222222222222222222222222222222222222222",
		Value:       "1500000",
		ValidAfter:  "1700000000",
		ValidBefore: "1700003600",
		Nonce:       "0x" + strings.Repeat("ab", 32),
		Signature:   "0x" + strings.Repeat("cd", 64) + "1b",
	}
}

func testRequest() Request {
	return Request{
		Nonce:                7,
		GasLimit:             120_000,
		MaxFeePerGas:         "40000000000",
		MaxPriorityFeePerGas: "1000000000",
		Authorization:        testAuthorization(),
	}
}

// TestTheBoundaryCanOnlyEverBuildAUSDCTransfer is the property the whole package
// exists for. A caller cannot name a recipient, a chain, an ether value, or a
// function, so no request — however hostile — produces a transaction that does
// anything other than call transferWithAuthorization on mainnet USDC with zero
// value. That is the difference between an allowlist and a structural
// restriction: there is nothing to bypass.
func TestTheBoundaryCanOnlyEverBuildAUSDCTransfer(t *testing.T) {
	hostile := []struct {
		name    string
		mutate  func(*Request)
		selfBad bool // rejected outright rather than built into a safe transaction
	}{
		{name: "ordinary request", mutate: func(*Request) {}},
		{name: "payee is the signer itself", mutate: func(r *Request) {
			r.Authorization.To = "0x3333333333333333333333333333333333333333"
		}},
		{name: "enormous authorization value", mutate: func(r *Request) {
			r.Authorization.Value = strings.Repeat("9", 60)
		}},
		{name: "zero authorization value", mutate: func(r *Request) {
			r.Authorization.Value = "0"
		}, selfBad: true},
		{name: "empty authorization", mutate: func(r *Request) {
			r.Authorization = Authorization{}
		}, selfBad: true},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			request := testRequest()
			tc.mutate(&request)
			tx, err := Unsigned(request, testLimits())
			if tc.selfBad {
				if err == nil {
					t.Fatalf("expected refusal, built a transaction instead")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unsigned: %v", err)
			}
			if tx.ChainId().Uint64() != config.MainnetChainID {
				t.Errorf("chain id = %v, want %d", tx.ChainId(), config.MainnetChainID)
			}
			if to := tx.To(); to == nil || !strings.EqualFold(to.Hex(), config.MainnetUSDC) {
				t.Errorf("recipient = %v, want %s", to, config.MainnetUSDC)
			}
			if tx.Value().Sign() != 0 {
				t.Errorf("value = %s, want 0", tx.Value())
			}
			// transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)
			const selector = "e3ee160e"
			if got := tx.Data(); len(got) < 4 || strings.ToLower(hexOf(got[:4])) != selector {
				t.Errorf("selector = %x, want %s", got[:min(4, len(got))], selector)
			}
		})
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// TestCeilingsComeFromTheBoundaryNotTheRequest is the second half of the design:
// a compromised caller that cannot change *what* is signed would otherwise simply
// raise the gas it can burn. There is no field for that, and the ceilings are
// enforced against the boundary's own configuration.
func TestCeilingsComeFromTheBoundaryNotTheRequest(t *testing.T) {
	limits := testLimits()
	over := []struct {
		name    string
		mutate  func(*Request)
		wantErr error
	}{
		{"gas limit above ceiling", func(r *Request) { r.GasLimit = limits.MaxGasLimit + 1 }, ErrOverLimit},
		{"max fee above ceiling", func(r *Request) { r.MaxFeePerGas = "80000000001" }, ErrOverLimit},
		{"priority fee above ceiling", func(r *Request) { r.MaxPriorityFeePerGas = "2000000001" }, ErrOverLimit},
		{"priority above max fee", func(r *Request) {
			r.MaxFeePerGas, r.MaxPriorityFeePerGas = "1000000000", "1500000000"
		}, ErrInvalidRequest},
		{"zero gas limit", func(r *Request) { r.GasLimit = 0 }, ErrInvalidRequest},
		{"negative max fee", func(r *Request) { r.MaxFeePerGas = "-1" }, ErrInvalidRequest},
		{"non-numeric max fee", func(r *Request) { r.MaxFeePerGas = "0x9184e72a000" }, ErrInvalidRequest},
	}
	for _, tc := range over {
		t.Run(tc.name, func(t *testing.T) {
			request := testRequest()
			tc.mutate(&request)
			if _, err := Unsigned(request, limits); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	// At the ceiling exactly, not above it: an off-by-one here would either
	// reject legitimate settlements during a fee spike or allow one over budget.
	atCeiling := testRequest()
	atCeiling.GasLimit = limits.MaxGasLimit
	atCeiling.MaxFeePerGas = limits.MaxFeePerGasWei.String()
	atCeiling.MaxPriorityFeePerGas = limits.MaxPriorityFeePerGasWei.String()
	if _, err := Unsigned(atCeiling, limits); err != nil {
		t.Fatalf("a request exactly at the ceilings must be signable: %v", err)
	}
}

// TestUnconfiguredBoundaryRefusesToSign guards a deployment mistake with an
// unbounded blast radius: ceilings left unset must mean "sign nothing", never
// "sign anything".
func TestUnconfiguredBoundaryRefusesToSign(t *testing.T) {
	for _, limits := range []Limits{
		{},
		{MaxGasLimit: 250_000},
		{MaxGasLimit: 250_000, MaxFeePerGasWei: big.NewInt(1)},
	} {
		if _, err := Unsigned(testRequest(), limits); !errors.Is(err, ErrOverLimit) {
			t.Errorf("limits %+v: error = %v, want ErrOverLimit", limits, err)
		}
	}
}

func TestMalformedAuthorizationsAreRefused(t *testing.T) {
	cases := map[string]func(*Authorization){
		"payer not an address":     func(a *Authorization) { a.From = "not-an-address" },
		"payee not an address":     func(a *Authorization) { a.To = "0x1234" },
		"nonce too short":          func(a *Authorization) { a.Nonce = "0xabcd" },
		"nonce not hex":            func(a *Authorization) { a.Nonce = "0x" + strings.Repeat("zz", 32) },
		"signature too short":      func(a *Authorization) { a.Signature = "0x" + strings.Repeat("cd", 32) },
		"unknown recovery id":      func(a *Authorization) { a.Signature = "0x" + strings.Repeat("cd", 64) + "07" },
		"validBefore before after": func(a *Authorization) { a.ValidBefore = "1699999999" },
		"validBefore equals after": func(a *Authorization) { a.ValidBefore = a.ValidAfter },
		"validAfter not a number":  func(a *Authorization) { a.ValidAfter = "yesterday" },
		"value not a number":       func(a *Authorization) { a.Value = "1.5" },
		"negative value":           func(a *Authorization) { a.Value = "-1500000" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := testRequest()
			mutate(&request.Authorization)
			if _, err := Unsigned(request, testLimits()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestBothRecoveryIdEncodingsAreAccepted mirrors the verify/settle disagreement
// that a previous fix uncovered: the official verifier accepts 0/1 while
// ecrecover needs 27/28, so a boundary rejecting 0/1 would refuse payments that
// had already verified. Both encodings must produce identical calldata.
func TestBothRecoveryIdEncodingsAreAccepted(t *testing.T) {
	low := testAuthorization()
	low.Signature = "0x" + strings.Repeat("cd", 64) + "00"
	high := testAuthorization()
	high.Signature = "0x" + strings.Repeat("cd", 64) + "1b"

	lowData, err := TransferWithAuthorizationData(low)
	if err != nil {
		t.Fatalf("recovery id 0: %v", err)
	}
	highData, err := TransferWithAuthorizationData(high)
	if err != nil {
		t.Fatalf("recovery id 27: %v", err)
	}
	if hexOf(lowData) != hexOf(highData) {
		t.Error("recovery ids 0 and 27 must pack to identical calldata")
	}
}
