package settlement

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/policy"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

// transferWithAuthorization takes nine static arguments behind a 4-byte
// selector, so the payload is exactly 4 + 9×32 bytes. Pinning the length means
// a future edit — a tuple refactor, a dynamic type — cannot silently change
// the encoding shape while leaving every field-level assertion passing.
func TestTransferWithAuthorizationDataLengthIsPinned(t *testing.T) {
	t.Parallel()
	data, err := TransferWithAuthorizationData(testAuthorization())
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	const want = 4 + 9*32
	if len(data) != want {
		t.Fatalf("calldata length = %d, want %d (4-byte selector + 9×32-byte words)", len(data), want)
	}
}

func TestTransferWithAuthorizationDataRejectsBadInput(t *testing.T) {
	cases := map[string]func(*Authorization){
		"bad from":    func(a *Authorization) { a.From = "not-an-address" },
		"zero value":  func(a *Authorization) { a.Value = "0" },
		"non-numeric": func(a *Authorization) { a.Value = "1.5" },
		"short nonce": func(a *Authorization) { a.Nonce = "0xabcd" },
		"short sig":   func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 32) },
		// Recovery ids 0 and 1 used to be listed here as bad input. They are not:
		// the official verifier accepts them, so rejecting them here meant a
		// payment could verify and then fail to settle. They are now normalized to
		// 27/28 and covered positively by TestSplitSignatureRecoveryIds, which also
		// keeps genuinely invalid ids rejected.
		"recovery id 2":  func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 64) + "02" },
		"recovery id 29": func(a *Authorization) { a.Signature = "0x" + strings.Repeat("11", 64) + "1d" },
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

// TestVerifiableSignaturesAreAlsoSettleable pins the invariant the two halves of
// this service previously broke: anything /verify accepts must be able to settle.
//
// The official verifier applies v-27 before recovery, so it accepts a recovery id
// of 0/1 — which crypto.Sign and many wallet libraries emit. Calldata construction
// required 27/28, so such a payment verified and then could not settle. The
// verification tests happened to use 0/1 and the settlement tests 0x1b, so neither
// side saw the disagreement.
func TestVerifiableSignaturesAreAlsoSettleable(t *testing.T) {
	t.Parallel()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payer := crypto.PubkeyToAddress(key.PublicKey).Hex()
	auth := Authorization{
		From: payer, To: "0x2222222222222222222222222222222222222222",
		Value:      "1000000",
		ValidAfter: time.Unix(1700000000, 0), ValidBefore: time.Unix(1700003600, 0),
		Nonce: "0x" + strings.Repeat("ab", 32),
	}
	digest, err := x402evm.HashEIP3009Authorization(
		x402evm.ExactEIP3009Authorization{
			From: auth.From, To: auth.To, Value: auth.Value,
			ValidAfter:  strconv.FormatInt(auth.ValidAfter.Unix(), 10),
			ValidBefore: strconv.FormatInt(auth.ValidBefore.Unix(), 10),
			Nonce:       auth.Nonce,
		},
		big.NewInt(1), common.HexToAddress(config.MainnetUSDC).Hex(), "USD Coin", "2")
	if err != nil {
		t.Fatal(err)
	}
	// crypto.Sign yields v in {0,1}, exactly what a wallet library may send.
	raw, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	if raw[64] > 1 {
		t.Fatalf("expected a 0/1 recovery id from crypto.Sign, got %d", raw[64])
	}

	// The signature is valid for the payer — which is precisely what the official
	// verifier concludes, since it normalizes v before recovery.
	recovered, err := crypto.SigToPub(digest, raw)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*recovered).Hex(); got != payer {
		t.Fatalf("recovered %s, want %s", got, payer)
	}

	// ...so calldata must build from it too.
	auth.Signature = "0x" + common.Bytes2Hex(raw)
	data, err := TransferWithAuthorizationData(auth)
	if err != nil {
		t.Fatalf("a signature the verifier accepts could not settle: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty calldata")
	}
}

// Every encoding a payer might send builds calldata; nothing else does.
func TestSplitSignatureRecoveryIds(t *testing.T) {
	t.Parallel()
	base := testAuthorization()
	for _, tc := range []struct {
		v      string
		wantOK bool
	}{{"00", true}, {"01", true}, {"1b", true}, {"1c", true}, {"02", false}, {"1d", false}, {"ff", false}} {
		auth := base
		auth.Signature = "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + tc.v
		_, err := TransferWithAuthorizationData(auth)
		if tc.wantOK != (err == nil) {
			t.Fatalf("v=%s: err=%v, wantOK=%v", tc.v, err, tc.wantOK)
		}
	}
}

// TestPolicyBoundaryPacksIdenticalCalldata is the check that makes two
// implementations safe rather than merely duplicated.
//
// The signing boundary builds transferWithAuthorization calldata independently
// (internal/policy) so that it does not trust code running inside the process it
// protects. Independence is only useful if the two agree: if they diverge, the
// boundary either refuses to sign what settlement wants signed — every payment
// fails — or signs something settlement did not intend, which is worse. This is
// the same pattern that already guards the selector, and that pattern has caught
// a real defect before.
func TestPolicyBoundaryPacksIdenticalCalldata(t *testing.T) {
	cases := map[string]Authorization{
		"ordinary payment": {
			From:        "0x1111111111111111111111111111111111111111",
			To:          "0x2222222222222222222222222222222222222222",
			Value:       "1500000",
			ValidAfter:  time.Unix(1700000000, 0),
			ValidBefore: time.Unix(1700003600, 0),
			Nonce:       "0x" + strings.Repeat("ab", 32),
			Signature:   "0x" + strings.Repeat("cd", 64) + "1b",
		},
		"low recovery id": {
			From:        "0x1111111111111111111111111111111111111111",
			To:          "0x2222222222222222222222222222222222222222",
			Value:       "1",
			ValidAfter:  time.Unix(0, 0),
			ValidBefore: time.Unix(1, 0),
			Nonce:       "0x" + strings.Repeat("00", 31) + "01",
			Signature:   "0x" + strings.Repeat("11", 64) + "00",
		},
		"large value and mixed-case addresses": {
			From:        "0xAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAa",
			To:          "0xbBbBBBBbbBBBbbbBbbBbbbbbBBbBbbbbBbBbbBBb",
			Value:       "340282366920938463463374607431768211455",
			ValidAfter:  time.Unix(1700000000, 0),
			ValidBefore: time.Unix(2000000000, 0),
			Nonce:       "0x" + strings.Repeat("ff", 32),
			Signature:   "0x" + strings.Repeat("ee", 64) + "1c",
		},
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			mine, err := TransferWithAuthorizationData(auth)
			if err != nil {
				t.Fatalf("settlement builder: %v", err)
			}
			theirs, err := policy.TransferWithAuthorizationData(auth.Wire())
			if err != nil {
				t.Fatalf("policy builder: %v", err)
			}
			if !bytes.Equal(mine, theirs) {
				t.Errorf("the two builders disagree:\n settlement %x\n policy     %x", mine, theirs)
			}
		})
	}
}
