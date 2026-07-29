package policy

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ETH402/facilitator/internal/config"
)

// FuzzUnsigned fuzzes the signing boundary's request parser.
//
// This is the highest-value fuzz target in the repository: every field here comes
// from a process the boundary is explicitly designed not to trust, and the output
// is a transaction that will be signed with a mainnet key. The invariant is not
// merely "does not panic" — it is that **any** input either fails or produces a
// zero-value call to transferWithAuthorization on canonical mainnet USDC within the
// configured ceilings. If that ever holds false for some input, the boundary is not
// a boundary.
func FuzzUnsigned(f *testing.F) {
	limits := Limits{
		MaxFeePerGasWei:         big.NewInt(80_000_000_000),
		MaxPriorityFeePerGasWei: big.NewInt(2_000_000_000),
		MaxGasLimit:             250_000,
	}
	// transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)
	const selector = "e3ee160e"

	f.Add(uint64(7), uint64(120_000), "40000000000", "1000000000",
		"0x1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222",
		"1500000", "1700000000", "1700003600",
		"0x"+strings.Repeat("ab", 32), "0x"+strings.Repeat("cd", 64)+"1b")
	f.Add(uint64(0), uint64(1), "1", "0", "", "", "", "", "", "", "")
	f.Add(^uint64(0), ^uint64(0), "-1", "-1", "0x0", "0x0", "0", "-1", "-1", "0x", "0x")
	f.Add(uint64(1), uint64(21_000), "1e9", "0x1", "0X1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222", "0001500000", "+1700000000", "1700003600",
		strings.Repeat("f", 64), strings.Repeat("e", 130))

	f.Fuzz(func(t *testing.T,
		nonce, gasLimit uint64,
		maxFee, priorityFee string,
		from, to, value, validAfter, validBefore, authNonce, signature string,
	) {
		request := Request{
			Nonce: nonce, GasLimit: gasLimit,
			MaxFeePerGas: maxFee, MaxPriorityFeePerGas: priorityFee,
			Authorization: Authorization{
				From: from, To: to, Value: value,
				ValidAfter: validAfter, ValidBefore: validBefore,
				Nonce: authNonce, Signature: signature,
			},
		}
		tx, err := Unsigned(request, limits)
		if err != nil {
			// Refusing is always an acceptable answer. What is not acceptable is
			// returning a transaction alongside an error, which a caller ignoring the
			// error could then sign.
			if tx != nil {
				t.Fatalf("returned both a transaction and an error %v", err)
			}
			return
		}
		if tx.ChainId() == nil || tx.ChainId().Uint64() != config.MainnetChainID {
			t.Fatalf("chain id %v is not mainnet", tx.ChainId())
		}
		if recipient := tx.To(); recipient == nil || !strings.EqualFold(recipient.Hex(), config.MainnetUSDC) {
			t.Fatalf("recipient %v is not canonical USDC", recipient)
		}
		if tx.Value().Sign() != 0 {
			t.Fatalf("value %s is not zero", tx.Value())
		}
		data := tx.Data()
		if len(data) < 4 || hexOf(data[:4]) != selector {
			t.Fatalf("calldata does not call transferWithAuthorization: %x", data)
		}
		// EIP-3009 v/r/s calldata is a fixed nine-word argument list, so its length
		// is not input-dependent. A different length means something was packed that
		// this boundary did not intend.
		if want := 4 + 9*32; len(data) != want {
			t.Fatalf("calldata is %d bytes, want %d", len(data), want)
		}
		if tx.Gas() > limits.MaxGasLimit {
			t.Fatalf("gas limit %d exceeds ceiling %d", tx.Gas(), limits.MaxGasLimit)
		}
		if tx.GasFeeCap().Cmp(limits.MaxFeePerGasWei) > 0 {
			t.Fatalf("max fee %s exceeds ceiling %s", tx.GasFeeCap(), limits.MaxFeePerGasWei)
		}
		if tx.GasTipCap().Cmp(limits.MaxPriorityFeePerGasWei) > 0 {
			t.Fatalf("priority fee %s exceeds ceiling %s", tx.GasTipCap(), limits.MaxPriorityFeePerGasWei)
		}
		if tx.GasTipCap().Cmp(tx.GasFeeCap()) > 0 {
			t.Fatalf("priority fee %s exceeds max fee %s", tx.GasTipCap(), tx.GasFeeCap())
		}
	})
}

// FuzzSplitSignature covers the recovery-id handling on its own, because a bug
// there previously made verification and settlement disagree about whether a
// payment was valid — verification accepted 0/1 while settlement rejected them, so
// payments that verified could not settle.
func FuzzSplitSignature(f *testing.F) {
	f.Add("0x" + strings.Repeat("cd", 64) + "1b")
	f.Add("0x" + strings.Repeat("cd", 64) + "00")
	f.Add("0x" + strings.Repeat("00", 65))
	f.Add("")
	f.Add("0x")
	f.Fuzz(func(t *testing.T, signature string) {
		v, _, _, err := splitSignature(signature)
		if err != nil {
			return
		}
		// ecrecover only accepts 27 or 28. Anything else that gets past this would
		// be packed into calldata and revert on chain, at the facilitator's expense.
		if v != 27 && v != 28 {
			t.Fatalf("accepted signature with recovery id %d", v)
		}
	})
}
