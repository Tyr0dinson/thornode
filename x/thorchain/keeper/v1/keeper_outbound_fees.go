package keeperv1

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
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
	_, err := k.getUint64(ctx, k.GetKey(ctx, prefixOutboundFeeWithheldRune, outAsset.String()), &record)
	return cosmos.NewUint(record), err
}

// AddToOutboundFeeWithheldRune - add to record of RUNE collected by the Reserve for an Asset's outbound fees
func (k KVStore) AddToOutboundFeeWithheldRune(ctx cosmos.Context, outAsset common.Asset, withheld cosmos.Uint) error {
	outboundFeeWithheldRune, err := k.GetOutboundFeeWithheldRune(ctx, outAsset)
	if err != nil {
		return err
	}

	outboundFeeWithheldRune = outboundFeeWithheldRune.Add(withheld)
	k.setUint64(ctx, k.GetKey(ctx, prefixOutboundFeeWithheldRune, outAsset.String()), outboundFeeWithheldRune.Uint64())
	return nil
}

// GetOutboundFeeWithheldRuneIterator to iterate through all Assets' OutboundFeeWithheldRune
// (e.g. for hard-fork GenesisState export)
func (k KVStore) GetOutboundFeeWithheldRuneIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixOutboundFeeWithheldRune)
}

// GetOutboundGasReimbursedRune - record of RUNE reimbursed by the Reserve for an Asset's outbounds' gas costs
func (k KVStore) GetOutboundGasReimbursedRune(ctx cosmos.Context, outAsset common.Asset) (cosmos.Uint, error) {
	var record uint64
	_, err := k.getUint64(ctx, k.GetKey(ctx, prefixOutboundGasReimbursedRune, outAsset.String()), &record)
	return cosmos.NewUint(record), err
}

// AddToOutboundGasReimbursedRune - add to record of RUNE reimbursed by the Reserve for an Asset's outbounds' gas costs
func (k KVStore) AddToOutboundGasReimbursedRune(ctx cosmos.Context, outAsset common.Asset, reimbursed cosmos.Uint) error {
	outboundGasReimbursedRune, err := k.GetOutboundGasReimbursedRune(ctx, outAsset)
	if err != nil {
		return err
	}

	outboundGasReimbursedRune = outboundGasReimbursedRune.Add(reimbursed)
	k.setUint64(ctx, k.GetKey(ctx, prefixOutboundGasReimbursedRune, outAsset.String()), outboundGasReimbursedRune.Uint64())
	return nil
}

// GetOutboundGasReimbursedRuneIterator to iterate through all Assets' OutboundGasReimbursedRune
// (e.g. for hard-fork GenesisState export)
func (k KVStore) GetOutboundGasReimbursedRuneIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixOutboundGasReimbursedRune)
}
