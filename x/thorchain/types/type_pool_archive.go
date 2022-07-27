package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (m *Pool) CalcUnitsV80(s cosmos.Uint) cosmos.Uint {
	// Calculate synth units
	// (L*S)/(2*A-S)
	// S := k.GetTotalSupply(ctx, p.Asset.GetSyntheticAsset())
	if m.BalanceAsset.IsZero() {
		m.SynthUnits = cosmos.ZeroUint()
	} else {
		numerator := m.LPUnits.Mul(s)
		denominator := common.SafeSub(m.BalanceAsset.MulUint64(2), s)
		if denominator.IsZero() {
			denominator = cosmos.OneUint()
		}
		m.SynthUnits = numerator.Quo(denominator)
	}
	return m.GetPoolUnits()
}
