package settlement

import (
	"math/big"
	"testing"
)

func TestEstimateFees(t *testing.T) {
	t.Parallel()
	// 2·baseFee + tip, comfortably under the ceiling.
	maxFee, tip, err := EstimateFees(big.NewInt(1_000_000_000), big.NewInt(2_000_000_000), big.NewInt(30_000_000_000))
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if maxFee.String() != "4000000000" || tip.String() != "2000000000" {
		t.Fatalf("maxFee=%s tip=%s", maxFee, tip)
	}
	// The ceiling caps the estimate and the tip never exceeds the max fee.
	maxFee, tip, err = EstimateFees(big.NewInt(100_000_000_000), big.NewInt(50_000_000_000), big.NewInt(30_000_000_000))
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if maxFee.String() != "30000000000" || tip.String() != "30000000000" {
		t.Fatalf("maxFee=%s tip=%s", maxFee, tip)
	}
	if _, _, err := EstimateFees(big.NewInt(0), big.NewInt(0), big.NewInt(1)); err == nil {
		t.Fatal("zero max fee accepted")
	}
	if _, _, err := EstimateFees(big.NewInt(1), big.NewInt(0), big.NewInt(0)); err == nil {
		t.Fatal("zero ceiling accepted")
	}
}

func TestBumpFees(t *testing.T) {
	t.Parallel()
	// 12.5% tip bump with fresh base fee, under the ceiling; the mempool's
	// price-bump rule then raises the cap to 110% of the original (4.4 gwei).
	maxFee, tip, ok := BumpFees(big.NewInt(4_000_000_000), big.NewInt(2_000_000_000), big.NewInt(1_000_000_000), big.NewInt(30_000_000_000))
	if !ok {
		t.Fatal("bump rejected")
	}
	if tip.String() != "2250000000" || maxFee.String() != "4400000000" {
		t.Fatalf("maxFee=%s tip=%s", maxFee, tip)
	}
	// The realistic case: tip far below base fee. The estimate (21.125 gwei)
	// would be rejected as underpriced; the cap must reach 110% of the
	// original's 21 gwei, or the node never accepts the replacement.
	maxFee, tip, ok = BumpFees(big.NewInt(21_000_000_000), big.NewInt(1_000_000_000), big.NewInt(10_000_000_000), big.NewInt(30_000_000_000))
	if !ok {
		t.Fatal("bump rejected")
	}
	if tip.String() != "1125000000" || maxFee.String() != "23100000000" {
		t.Fatalf("maxFee=%s tip=%s, want the 110%% floor", maxFee, tip)
	}
	// A ceiling beneath the 110% floor means no acceptable replacement exists.
	if _, _, ok = BumpFees(big.NewInt(21_000_000_000), big.NewInt(1_000_000_000), big.NewInt(10_000_000_000), big.NewInt(22_000_000_000)); ok {
		t.Fatal("bump beneath the mempool price-bump floor accepted")
	}
	// A zero tip still climbs by the 1 wei minimum.
	_, tip, ok = BumpFees(big.NewInt(2_000_000_000), big.NewInt(0), big.NewInt(1_000_000_000), big.NewInt(30_000_000_000))
	if !ok || tip.String() != "1" {
		t.Fatalf("tip=%s ok=%v", tip, ok)
	}
	// Ceiling reached with a stagnant market: no headroom, no replacement.
	if _, _, ok = BumpFees(big.NewInt(30_000_000_000), big.NewInt(5_000_000_000), big.NewInt(100_000_000_000), big.NewInt(30_000_000_000)); ok {
		t.Fatal("bump beyond the ceiling accepted")
	}
}
