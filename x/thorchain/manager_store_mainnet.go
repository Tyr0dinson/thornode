//go:build !stagenet && !mocknet && !regtest
// +build !stagenet,!mocknet,!regtest

package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func migrateStoreV136(ctx cosmos.Context, mgr *Mgrs) {
	defer func() {
		if err := recover(); err != nil {
			ctx.Logger().Error("fail to migrate store to v136", "error", err)
		}
	}()

	// #2054, clean up Affiliate Collector Module and Pool Module oversolvencies after v2 hardfork.

	// https://thornode-v2.ninerealms.com/thorchain/invariant/affiliate_collector?height=17562001
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(97862126317))
	if err := mgr.Keeper().SendFromModuleToModule(ctx, AffiliateCollectorName, ReserveName, common.NewCoins(coin)); err != nil {
		ctx.Logger().Error("failed to migrate affiliate collector rune oversolvency to reserve", "error", err)
	}

	// https://thornode-v2.ninerealms.com/thorchain/invariant/asgard?height=17562001
	poolOversol := []struct {
		amount uint64
		asset  string
	}{
		{1588356075, "bnb/bnb"},
		{11970000, "bnb/btcb-1de"},
		{10368725099, "bnb/busd-bd1"},
		{1688100000, "bnb/twt-8c2"},
		{14326209633, "bsc/bnb"},
		{4745942336700, "eth/usdc-0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"},
		{173198253248, "eth/usdt-0xdac17f958d2ee523a2206206994597c13d831ec7"},
		{5973894700, "ltc/ltc"},
		{27251950916, "rune"}, // BalanceAsset of Suspended (Ragnaroked) BNB.BNB pool dropped in hardfork
		{1381124490000, "tor"},
	}

	var coinsToSend common.Coins
	for i := range poolOversol {
		amount := cosmos.NewUint(poolOversol[i].amount)
		asset, err := common.NewAsset(poolOversol[i].asset)
		if err != nil {
			ctx.Logger().Error("failed to create asset", "error", err, "asset", poolOversol[i].asset)
			continue
		}
		coin = common.NewCoin(asset, amount)

		// Attempt to burn Ragnaroked (worthless) BNB assets directly, rather than transferring them.
		if asset.Chain.String() != "BNB" {
			coinsToSend = append(coinsToSend, coin)
			continue
		}
		if err := mgr.Keeper().BurnFromModule(ctx, AsgardName, coin); err != nil {
			ctx.Logger().Error("failed to burn bnb coin from pool module", "error", err, "coin", coin.String())
		}
	}
	if err := mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coinsToSend); err != nil {
		ctx.Logger().Error("failed to migrate pool module oversolvencies to reserve", "error", err)
	}
}
