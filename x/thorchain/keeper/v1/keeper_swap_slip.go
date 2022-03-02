package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// AddToSwapSlip - add swap slip to block
func (k KVStore) AddToSwapSlip(ctx cosmos.Context, asset common.Asset, amt cosmos.Uint) error {
	currentHeight := common.BlockHeight(ctx)

	poolSlip, err := k.GetPoolSwapSlip(ctx, currentHeight, asset)
	if err != nil {
		return err
	}

	poolSlip = poolSlip.Add(amt)

	// update pool slip
	k.setUint64(ctx, k.GetKey(ctx, prefixPoolSwapSlip, fmt.Sprintf("%d-%s", currentHeight, asset.String())), poolSlip.Uint64())
	return nil
}

func (k KVStore) getSwapSlip(ctx cosmos.Context, key string) (cosmos.Uint, error) {
	var record uint64
	_, err := k.getUint64(ctx, key, &record)
	return cosmos.NewUint(record), err
}

// GetPoolSwapSlip - total of slip in each block per pool
func (k KVStore) GetPoolSwapSlip(ctx cosmos.Context, height int64, asset common.Asset) (cosmos.Uint, error) {
	key := k.GetKey(ctx, prefixPoolSwapSlip, fmt.Sprintf("%d-%s", height, asset.String()))
	return k.getSwapSlip(ctx, key)
}

func (k KVStore) RollupSwapSlip(ctx cosmos.Context, targetCount int64, asset common.Asset) (cosmos.Uint, error) {
	var currCount int64
	currCountKey := k.GetKey(ctx, prefixPoolSwapSlip, fmt.Sprintf("rollup-count/%s", asset.String()))
	_, _ = k.getInt64(ctx, currCountKey, &currCount)

	var currRollup uint64
	currRollupKey := k.GetKey(ctx, prefixPoolSwapSlip, fmt.Sprintf("rollup/%s", asset.String()))
	_, _ = k.getUint64(ctx, currRollupKey, &currRollup)

	reset := func(err error) (cosmos.Uint, error) {
		if err != nil {
			ctx.Logger().Error("resetting pool swap slip rollup", "asset", asset.String(), "err", err)
		}
		k.setInt64(ctx, currCountKey, 0)
		k.setUint64(ctx, currRollupKey, 0)
		return cosmos.ZeroUint(), err
	}

	if currCount > targetCount {
		// we need to reset, likely the target count was changed to a lower
		// number than it was before
		ctx.Logger().Info("resetting pool swap rollup", "asset", asset.String())
		return reset(nil)
	}

	// add the swap slip from the previous block to the rollup
	prevBlockSlip, err := k.GetPoolSwapSlip(ctx, common.BlockHeight(ctx)-1, asset)
	if err != nil {
		return reset(err)
	}
	currRollup += prevBlockSlip.Uint64()
	currCount++

	if currCount > targetCount {
		// remove the oldest swap slip block from the count
		oldBlockSlip, err := k.GetPoolSwapSlip(ctx, common.BlockHeight(ctx)-targetCount, asset)
		if err != nil {
			return reset(err)
		}
		currRollup -= oldBlockSlip.Uint64()
		currCount--
	}

	k.setInt64(ctx, currCountKey, currCount)
	k.setUint64(ctx, currRollupKey, currRollup)

	return cosmos.NewUint(currRollup), nil
}
