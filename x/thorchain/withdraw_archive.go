package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func calculateWithdrawV129(poolUnits, poolRune, poolAsset, lpUnits, withdrawBasisPoints cosmos.Uint, withdrawalAsset common.Asset) (cosmos.Uint, cosmos.Uint, cosmos.Uint, error) {
	if poolUnits.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("poolUnits can't be zero")
	}
	if poolRune.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("pool rune balance can't be zero")
	}
	if poolAsset.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("pool asset balance can't be zero")
	}
	if lpUnits.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("liquidity provider unit can't be zero")
	}
	if withdrawBasisPoints.GT(cosmos.NewUint(MaxWithdrawBasisPoints)) {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("withdraw basis point %s is not valid", withdrawBasisPoints.String())
	}

	unitsToClaim := common.GetSafeShare(withdrawBasisPoints, cosmos.NewUint(10000), lpUnits)
	unitAfter := common.SafeSub(lpUnits, unitsToClaim)
	if withdrawalAsset.IsEmpty() {
		withdrawRune := common.GetSafeShare(unitsToClaim, poolUnits, poolRune)
		withdrawAsset := common.GetSafeShare(unitsToClaim, poolUnits, poolAsset)
		return withdrawRune, withdrawAsset, unitAfter, nil
	}
	if withdrawalAsset.IsRune() {
		return calcAsymWithdrawal(unitsToClaim, poolUnits, poolRune), cosmos.ZeroUint(), unitAfter, nil
	}
	return cosmos.ZeroUint(), calcAsymWithdrawal(unitsToClaim, poolUnits, poolAsset), unitAfter, nil
}

func calcAsymWithdrawal(s, t, a cosmos.Uint) cosmos.Uint {
	// share = (s * A * (2 * T^2 - 2 * T * s + s^2))/T^3
	// s = liquidity provider units for member (after factoring in withdrawBasisPoints)
	// T = totalPoolUnits for pool
	// A = assetDepth to be withdrawn
	// (part1 * (part2 - part3 + part4)) / part5
	part1 := s.Mul(a)
	part2 := t.Mul(t).MulUint64(2)
	part3 := t.Mul(s).MulUint64(2)
	part4 := s.Mul(s)
	numerator := part1.Mul(common.SafeSub(part2, part3).Add(part4))
	part5 := t.Mul(t).Mul(t)
	return numerator.Quo(part5)
}
