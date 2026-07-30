package settlement

import (
	"errors"
	"fmt"
	"math/big"
)

// Fee estimation stays inside the operator's configured ceiling at all times:
// the ceiling is the hard spend bound (ADR-0004 decision 6), estimation only
// avoids overpaying beneath it and leaves headroom for replacement bumps.

// EstimateFees computes the initial EIP-1559 fee pair for a broadcast:
// maxFee = min(2·baseFee + tipCap, ceiling), with the tip capped by the
// resulting maxFee. An error means the inputs cannot produce a usable,
// ceiling-bounded fee — the caller leaves the intent for a later tick.
func EstimateFees(baseFee, tipCap, ceiling *big.Int) (maxFee, priority *big.Int, err error) {
	if ceiling == nil || ceiling.Sign() <= 0 {
		return nil, nil, errors.New("fee ceiling must be positive")
	}
	if baseFee == nil || baseFee.Sign() < 0 || tipCap == nil || tipCap.Sign() < 0 {
		return nil, nil, errors.New("base fee and tip must be unsigned")
	}
	maxFee = new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), tipCap)
	if maxFee.Cmp(ceiling) > 0 {
		maxFee = new(big.Int).Set(ceiling)
	}
	if maxFee.Sign() == 0 {
		return nil, nil, fmt.Errorf("base fee %s and tip %s produce a zero max fee", baseFee, tipCap)
	}
	priority = new(big.Int).Set(tipCap)
	if priority.Cmp(maxFee) > 0 {
		priority = new(big.Int).Set(maxFee)
	}
	return maxFee, priority, nil
}

// BumpFees computes a replacement's fee pair. The tip rises by 12.5% (with a
// 1 wei minimum so a zero tip can still climb) against a fresh base-fee
// estimate, all still capped by the same ceiling. The mempool's price-bump
// rule rejects a replacement unless *both* its fee cap and its tip reach 110%
// of the transaction it replaces, so both floors are enforced: ok is false
// when the ceiling leaves no room to satisfy them, and the caller keeps
// waiting. Without the fee-cap floor a bump the node would never accept was
// recorded as the new baseline, and the stuck transaction converged over a
// dozen replacement windows instead of one.
func BumpFees(oldMaxFee, oldTip, baseFee, ceiling *big.Int) (maxFee, priority *big.Int, ok bool) {
	if oldMaxFee == nil || oldMaxFee.Sign() <= 0 || oldTip == nil || oldTip.Sign() < 0 {
		return nil, nil, false
	}
	minTip := minBump(oldTip)
	bumpedTip := new(big.Int).Mul(oldTip, big.NewInt(9))
	bumpedTip.Div(bumpedTip, big.NewInt(8))
	for _, floor := range []*big.Int{minTip, new(big.Int).Add(oldTip, big.NewInt(1))} {
		if bumpedTip.Cmp(floor) < 0 {
			bumpedTip.Set(floor)
		}
	}
	maxFee, priority, err := EstimateFees(baseFee, bumpedTip, ceiling)
	if err != nil {
		return nil, nil, false
	}
	if minFee := minBump(oldMaxFee); maxFee.Cmp(minFee) < 0 {
		// The estimate does not outbid the original's cap by the mempool's
		// rule; raise it to the floor if the ceiling allows.
		if ceiling.Cmp(minFee) < 0 {
			return nil, nil, false
		}
		maxFee = minFee
	}
	if priority.Cmp(maxFee) > 0 {
		priority = new(big.Int).Set(maxFee)
	}
	if priority.Cmp(minTip) < 0 || maxFee.Cmp(minBump(oldMaxFee)) < 0 {
		return nil, nil, false
	}
	return maxFee, priority, true
}

// minBump returns the smallest integer at least 110% of n: the mempool's
// price-bump threshold for the field a replacement must outbid.
func minBump(n *big.Int) *big.Int {
	bump := new(big.Int).Mul(n, big.NewInt(11))
	return bump.Add(bump, big.NewInt(9)).Div(bump, big.NewInt(10))
}
