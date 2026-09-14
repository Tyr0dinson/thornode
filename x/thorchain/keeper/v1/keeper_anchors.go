package keeperv1

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// TorAnchorMimirKey is the mimir key whose positive value marks the pool of
// asset as a TOR anchor.
func TorAnchorMimirKey(asset common.Asset) string {
	return "TorAnchor-" + strings.ReplaceAll(asset.String(), ".", "-")
}

func (k KVStore) GetAnchors(ctx cosmos.Context, asset common.Asset) []common.Asset {
	isTor := asset.Equals(common.TOR)
	if isTor {
		cache := keeper.AnchorsCacheFromContext(ctx)
		if assets, ok := cache.Get(); ok {
			return assets
		}
		assets := make([]common.Asset, 0)
		pools, err := k.GetPools(ctx)
		if err != nil {
			ctx.Logger().Error("unable to fetch pools for anchor", "error", err)
			return assets
		}
		var poolKeys map[string]struct{}
		if cache != nil {
			poolKeys = make(map[string]struct{}, len(pools))
		}
		cacheable := true
		for _, pool := range pools {
			if poolKeys != nil {
				poolKeys[string(k.GetKey(prefixPool, pool.Asset.String()))] = struct{}{}
			}
			mimirKey := TorAnchorMimirKey(pool.Asset)
			var val int64
			val, err = k.GetMimir(ctx, mimirKey)
			if err != nil {
				ctx.Logger().Error("unable to fetch pool for anchor", "mimir", mimirKey, "error", err)
				cacheable = false
				continue
			}
			if val > 0 {
				assets = append(assets, pool.Asset)
			}
		}
		if cacheable {
			cache.Set(assets, poolKeys)
		}
		return assets
	}
	return []common.Asset{asset.GetLayer1Asset()}
}

// RunePerDollar gets in 1e8 terms the amount of Rune that is equal to 1 USD (in other words, 1 USD's price in Rune)
func (k KVStore) RunePerDollar(ctx cosmos.Context) cosmos.Uint {
	// get the usd price of a rune, this is being returned in 1e8 number
	runePriceInUSD := k.DollarsPerRune(ctx)

	// in order to calculate the runes per USD, it is needed to address 1e8 conversion
	return common.GetUncappedShare(
		// one single tor
		cosmos.NewUint(common.One),
		// one rune has total assets of USD equal to runePriceInUSD
		runePriceInUSD, cosmos.NewUint(common.One),
	)
}

// DollarsPerRune gets in 1e8 terms the amount of USD that is equal to 1 RUNE (in other words, 1 RUNE's price in USD)
func (k KVStore) DollarsPerRune(ctx cosmos.Context) cosmos.Uint {
	// check for mimir override
	dollarsPerRune, err := k.GetMimir(ctx, "DollarsPerRune")
	if err == nil && dollarsPerRune > 0 {
		return cosmos.NewUint(uint64(dollarsPerRune))
	}

	usdAssets := k.GetAnchors(ctx, common.TOR)

	// anchorMedian returns a 1e8*constants.DollarMulti number,
	// so divide by constants.DollarMulti in order to return a 1e8 number
	// (consistent with a DollarsPerRune Mimir key being 1e8).
	return k.AnchorMedian(ctx, usdAssets).QuoUint64(constants.DollarMulti)
}

func (k KVStore) AnchorMedian(ctx cosmos.Context, assets []common.Asset) cosmos.Uint {
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
		// Fail closed: a failure here means the speculative overlay could not be safely
		// removed, so the raw pool still carries the unfinalized mutation this view exists
		// to exclude. Exclude the anchor rather than price TOR from speculative depth.
		pool, err = k.finalityAdjustedPool(ctx, pool)
		if err != nil {
			ctx.Logger().Error("fail to remove active mempool swap balances from anchor pool", "asset", asset.String(), "error", err)
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
