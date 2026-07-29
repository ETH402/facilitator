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
// estimate, all still capped by the same ceiling. ok is false when the
// ceiling leaves no room to outbid the original — the replacement would be
// rejected by the mempool's price-bump rule, so the caller keeps waiting.
func BumpFees(oldMaxFee, oldTip, baseFee, ceiling *big.Int) (maxFee, priority *big.Int, ok bool) {
	if oldMaxFee == nil || oldMaxFee.Sign() <= 0 || oldTip == nil || oldTip.Sign() < 0 {
		return nil, nil, false
	}
	bumpedTip := new(big.Int).Mul(oldTip, big.NewInt(9))
	bumpedTip.Div(bumpedTip, big.NewInt(8))
	minimum := new(big.Int).Add(oldTip, big.NewInt(1))
	if bumpedTip.Cmp(minimum) < 0 {
		bumpedTip = minimum
	}
	maxFee, priority, err := EstimateFees(baseFee, bumpedTip, ceiling)
	if err != nil {
		return nil, nil, false
	}
	if maxFee.Cmp(oldMaxFee) <= 0 {
		return nil, nil, false
	}
	return maxFee, priority, true
}
