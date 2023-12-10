package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

func GetAnchors(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset) []common.Asset {
	if asset.GetChain().IsTHORChain() {
		assets := make([]common.Asset, 0)
		pools, err := keeper.GetPools(ctx)
		if err != nil {
			ctx.Logger().Error("unable to fetch pools for anchor", "error", err)
			return assets
		}
		for _, pool := range pools {
			mimirKey := fmt.Sprintf("TorAnchor-%s", pool.Asset.String())
			mimirKey = strings.ReplaceAll(mimirKey, ".", "-")
			val, err := keeper.GetMimir(ctx, mimirKey)
			if err != nil {
				ctx.Logger().Error("unable to fetch pool for anchor", "mimir", mimirKey, "error", err)
				continue
			}
			if val > 0 {
				assets = append(assets, pool.Asset)
			}
		}
		return assets
	}
	return []common.Asset{asset.GetLayer1Asset()}
}

// DollarsPerRune gets in 1e8 terms the amount of USD that is equal to 1 RUNE (in other words, 1 RUNE's price in USD)
func DollarsPerRune(ctx cosmos.Context, keeper keeper.Keeper) cosmos.Uint {
	// check for mimir override
	dollarsPerRune, err := keeper.GetMimir(ctx, "DollarsPerRune")
	if err == nil && dollarsPerRune > 0 {
		return cosmos.NewUint(uint64(dollarsPerRune))
	}

	usdAssets := GetAnchors(ctx, keeper, common.TOR)

	// anchorMedian returns a 1e8*constants.DollarMulti number,
	// so divide by constants.DollarMulti in order to return a 1e8 number
	// (consistent with a DollarsPerRune Mimir key being 1e8).
	return AnchorMedian(ctx, keeper, usdAssets).QuoUint64(constants.DollarMulti)
}

func AnchorMedian(ctx cosmos.Context, keeper keeper.Keeper, assets []common.Asset) cosmos.Uint {
	p := make([]cosmos.Uint, 0)
	for _, asset := range assets {
		if keeper.IsGlobalTradingHalted(ctx) || keeper.IsChainTradingHalted(ctx, asset.Chain) {
			continue
		}
		pool, err := keeper.GetPool(ctx, asset)
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
