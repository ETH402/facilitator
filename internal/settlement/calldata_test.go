package settlement

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

func testAuthorization() Authorization {
	return Authorization{
		From:        "0x1111111111111111111111111111111111111111",
		To:          "0x2222222222222222222222222222222222222222",
		Value:       "1000000",
		ValidAfter:  time.Unix(1700000000, 0).UTC(),
		ValidBefore: time.Unix(1700003600, 0).UTC(),
		Nonce:       "0x" + strings.Repeat("ab", 32),
		Signature:   "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + "1b",
	}
}

func TestTransferWithAuthorizationDataRoundTrip(t *testing.T) {
	auth := testAuthorization()
	data, err := TransferWithAuthorizationData(auth)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	parsed, err := abi.JSON(strings.NewReader(string(x402evm.TransferWithAuthorizationVRSABI)))
	if err != nil {
		t.Fatalf("parse ABI: %v", err)
	}
	method := parsed.Methods[x402evm.FunctionTransferWithAuthorization]
	if got := hex.EncodeToString(data[:4]); got != hex.EncodeToString(method.ID) {
		t.Fatalf("selector = %s, want %s", got, hex.EncodeToString(method.ID))
	}
	args, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(args) != 9 {
		t.Fatalf("unpacked %d arguments, want 9", len(args))
	}
	if args[0].(common.Address) != common.HexToAddress(auth.From) {
		t.Errorf("from = %v", args[0])
	}
	if args[1].(common.Address) != common.HexToAddress(auth.To) {
		t.Errorf("to = %v", args[1])
	}
	if args[2].(interface{ String() string }).String() != auth.Value {
		t.Errorf("value = %v", args[2])
	}
	if args[3].(interface{ String() string }).String() != "1700000000" {
		t.Errorf("validAfter = %v", args[3])
	}
	if args[4].(interface{ String() string }).String() != "1700003600" {
		t.Errorf("validBefore = %v", args[4])
	}
	var wantNonce [32]byte
	copy(wantNonce[:], common.FromHex(auth.Nonce))
	if args[5].([32]byte) != wantNonce {
		t.Errorf("nonce = %x", args[5])
	}
	if args[6].(uint8) != 27 {
		t.Errorf("v = %d, want 27", args[6])
	}
	var wantR, wantS [32]byte
	copy(wantR[:], common.FromHex("0x"+strings.Repeat("11", 32)))
	copy(wantS[:], common.FromHex("0x"+strings.Repeat("22", 32)))
	if args[7].([32]byte) != wantR {
		t.Errorf("r = %x", args[7])
	}
	if args[8].([32]byte) != wantS {
		t.Errorf("s = %x", args[8])
	}
}

func TestTransferWithAuthorizationDataRejectsBadInput(t *testing.T) {
	cases := map[string]func(*Authorization){
		"bad from":      func(a *Authorization) { a.From = "not-an-address" },
		"zero value":    func(a *Authorization) { a.Value = "0" },
		"non-numeric":   func(a *Authorization) { a.Value = "1.5" },
		"short nonce":   func(a *Authorization) { a.Nonce = "0xabcd" },
		"short sig":     func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 32) },
		"recovery id 0": func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 64) + "00" },
		"recovery id 1": func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 64) + "01" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			auth := testAuthorization()
			mutate(&auth)
			if _, err := TransferWithAuthorizationData(auth); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestBuiltCalldataPassesSignerAllowlist cross-checks the two independent
// derivations of the transferWithAuthorization selector: the calldata builder
// packs it from the official ABI, while signer.Transaction.Validate derives it
// from the canonical signature string. If either drifts, settlement would either
// sign the wrong function or refuse to sign at all, so this pins them together.
func TestBuiltCalldataPassesSignerAllowlist(t *testing.T) {
	t.Parallel()
	data, err := TransferWithAuthorizationData(testAuthorization())
	if err != nil {
		t.Fatal(err)
	}
	tx := signer.Transaction{
		ChainID: config.MainnetChainID, Nonce: 3, To: config.MainnetUSDC,
		Data: data, Value: "0", GasLimit: 120000,
		MaxFeePerGas: "30000000000", MaxPriorityFeePerGas: "1000000000",
	}
	if err := tx.Validate(); err != nil {
		t.Fatalf("real settlement calldata rejected by the signer allowlist: %v", err)
	}
}
