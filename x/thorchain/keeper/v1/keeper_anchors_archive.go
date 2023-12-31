package keeperv1

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

func (k KVStore) DollarInRune(ctx cosmos.Context) cosmos.Uint {
	// check for mimir override
	dollarInRune, err := k.GetMimir(ctx, "DollarInRune")
	if err == nil && dollarInRune > 0 {
		return cosmos.NewUint(uint64(dollarInRune))
	}

	usdAssets := k.GetAnchors(ctx, common.TOR)

	return k.AnchorMedian(ctx, usdAssets)
}

func (k KVStore) AnchorMedianV1(ctx cosmos.Context, assets []common.Asset) cosmos.Uint {
	p := make([]cosmos.Uint, 0)
	for _, asset := range assets {
		if k.IsGlobalTradingHalted(ctx) || k.IsChainTradingHalted(ctx, asset.Chain) {
			continue
		}
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get usd pool", "asset", asset.String(), "error", err)
			continue
		}
		if !pool.IsAvailable() {
			continue
		}
		// value := common.GetUncappedShare(pool.BalanceAsset, pool.BalanceRune, cosmos.NewUint(common.One))
		value := pool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti * common.One))

		if !value.IsZero() {
			p = append(p, value)
		}
	}
	return common.GetMedianUint(p)
}
