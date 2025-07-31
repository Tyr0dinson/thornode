package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func (k KVStore) GetOutboundTxFee(ctx cosmos.Context) cosmos.Uint {
	if k.usdFeesEnabled(ctx) {
		return k.DollarConfigInRune(ctx, constants.NativeOutboundFeeUSD)
	}
	fee := k.GetConfigInt64(ctx, constants.OutboundTransactionFee)
	return cosmos.NewUint(uint64(fee))
}

// GetOutboundFeeWithheldRune - record of RUNE collected by the Reserve for an Asset's outbound fees
func (k KVStore) GetOutboundFeeWithheldRune(ctx cosmos.Context, outAsset common.Asset) (cosmos.Uint, error) {
	var record uint64
	_, err := k.getUint64(ctx, k.GetKey(prefixOutboundFeeWithheldRune, outAsset.String()), &record)
	withheld := cosmos.NewUint(record)

	// If the withheld amount is zero, initialize it to the initial withheld amount
	if withheld.IsZero() {
		withheld = k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000))
		err = k.AddToOutboundFeeWithheldRune(ctx, outAsset, withheld)
		if err != nil {
			return cosmos.ZeroUint(), err
		}
		return withheld, nil
	}

	return withheld, err
}

// AddToOutboundFeeWithheldRune - add to record of RUNE collected by the Reserve for an Asset's outbound fees
func (k KVStore) AddToOutboundFeeWithheldRune(ctx cosmos.Context, outAsset common.Asset, withheld cosmos.Uint) error {
	outboundFeeWithheldRune, err := k.GetOutboundFeeWithheldRune(ctx, outAsset)
	if err != nil {
		return err
	}

	// If the withheld amount is zero, initialize it to the initial withheld amount before adding the new amount
	if outboundFeeWithheldRune.IsZero() {
		outboundFeeWithheldRune = k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000))
	}

	outboundFeeWithheldRune = outboundFeeWithheldRune.Add(withheld)
	k.setUint64(ctx, k.GetKey(prefixOutboundFeeWithheldRune, outAsset.String()), outboundFeeWithheldRune.Uint64())
	return nil
}

func (k KVStore) GetSurplusForTargetMultiplier(ctx cosmos.Context, targetMultiplierBps cosmos.Uint) cosmos.Uint {
	// Calculate the surplus level at which the multiplier reaches 100%.
	// The multiplier formula is:
	//
	//	multiplier = max - ((surplus / target) * (max - min))
	//
	// To find the equilibrium surplus where multiplier = 100% (i.e., 10_000 bps):
	//
	//	10_000 = max - ((surplus / target) * (max - min))
	//
	// Rearranged:
	//
	//	(max - 10_000) = (surplus / target) * (max - min)
	//	surplus = ((max - 10_000) / (max - min)) * target

	targetSurplus := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)))
	maxMultiplier := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)))
	minMultiplier := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)))

	if targetMultiplierBps.LT(minMultiplier) {
		targetMultiplierBps = minMultiplier
	}

	if targetMultiplierBps.GT(maxMultiplier) {
		targetMultiplierBps = maxMultiplier
	}

	deltaToTarget := maxMultiplier.Sub(targetMultiplierBps)
	maxMinusMin := maxMultiplier.Sub(minMultiplier)
	equilibriumSurplus := deltaToTarget.Quo(maxMinusMin).Mul(targetSurplus)
	return equilibriumSurplus
}

// GetOutboundFeeWithheldRuneIterator to iterate through all Assets' OutboundFeeWithheldRune
// (e.g. for hard-fork GenesisState export)
func (k KVStore) GetOutboundFeeWithheldRuneIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixOutboundFeeWithheldRune)
}

// GetOutboundFeeSpentRune - record of RUNE spent by the Reserve for an Asset's outbounds' gas costs
func (k KVStore) GetOutboundFeeSpentRune(ctx cosmos.Context, outAsset common.Asset) (cosmos.Uint, error) {
	var record uint64
	_, err := k.getUint64(ctx, k.GetKey(prefixOutboundFeeSpentRune, outAsset.String()), &record)
	return cosmos.NewUint(record), err
}

// AddToOutboundFeeSpentRune - add to record of RUNE spent by the Reserve for an Asset's outbounds' gas costs
func (k KVStore) AddToOutboundFeeSpentRune(ctx cosmos.Context, outAsset common.Asset, spent cosmos.Uint) error {
	outboundFeeSpentRune, err := k.GetOutboundFeeSpentRune(ctx, outAsset)
	if err != nil {
		return err
	}

	outboundFeeSpentRune = outboundFeeSpentRune.Add(spent)
	k.setUint64(ctx, k.GetKey(prefixOutboundFeeSpentRune, outAsset.String()), outboundFeeSpentRune.Uint64())
	return nil
}

// GetOutboundFeeSpentRuneIterator to iterate through all Assets' OutboundFeeSpentRune
// (e.g. for hard-fork GenesisState export)
func (k KVStore) GetOutboundFeeSpentRuneIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixOutboundFeeSpentRune)
}
