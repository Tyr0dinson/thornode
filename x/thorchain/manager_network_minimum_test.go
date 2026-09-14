package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

func newMinimumMigrationTest(c *C, chain common.Chain, amount cosmos.Uint) (cosmos.Context, *Mgrs, Vault, Vault) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 5)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NewNetworkFee(chain, 1, 1)), IsNil)

	pool := NewPool()
	pool.Asset = chain.GetGasAsset()
	pool.Status = PoolAvailable
	pool.Decimals = 8
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	source := GetRandomVault()
	source.Status = RetiringVault
	source.StatusSince = ctx.BlockHeight()
	source.Chains = []string{chain.String()}
	coin := common.NewCoin(chain.GetGasAsset(), amount)
	coin.Decimals = 8
	source.Coins = common.NewCoins(coin)
	c.Assert(mgr.Keeper().SetVault(ctx, source), IsNil)

	target := GetRandomVault()
	target.Status = ActiveVault
	target.Chains = []string{chain.String()}
	target.Coins = nil
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)
	return ctx, mgr, source, target
}

func assertMigrationNotScheduled(c *C, ctx cosmos.Context, mgr *Mgrs, source, target Vault) {
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)
	c.Check(mgr.Keeper().GetPendingOutbounds(ctx, source.Coins[0].Asset), HasLen, 0)
	storedSource, err := mgr.Keeper().GetVault(ctx, source.PubKey)
	c.Assert(err, IsNil)
	c.Check(storedSource.PendingTxBlockHeights, HasLen, 0)
	c.Check(storedSource.Coins, DeepEquals, source.Coins)
	c.Check(storedSource.Status, Equals, RetiringVault)
	storedTarget, err := mgr.Keeper().GetVault(ctx, target.PubKey)
	c.Assert(err, IsNil)
	c.Check(storedTarget.HasFunds(), Equals, false)
}

func (*NetworkManagerTestSuite) TestMigrateFundsDefersBelowMinimumUntilAffordable(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		minimum := chain.ExistentialDeposit()
		balance := minimum.MulUint64(3)
		ctx, mgr, source, target := newMinimumMigrationTest(c, chain, balance)
		network := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
		gas, err := mgr.GasMgr().GetMaxGas(ctx, chain)
		c.Assert(err, IsNil)
		c.Assert(gas.Amount.Equal(cosmos.OneUint()), Equals, true)
		pool, err := mgr.Keeper().GetPool(ctx, chain.GetGasAsset())
		c.Assert(err, IsNil)
		reserve := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native())

		// The first three rounds stay below ED after gas; no funds have moved.
		for round := int64(1); round <= 3; round++ {
			ctx = ctx.WithBlockHeight(source.StatusSince + round - 1)
			candidate := balance.QuoUint64(uint64(6 - round)).Sub(gas.Amount)
			c.Assert(candidate.LT(minimum), Equals, true)
			c.Assert(network.migrateFunds(ctx, mgr), IsNil, Commentf("%s round %d", chain, round))
			assertMigrationNotScheduled(c, ctx, mgr, source, target)
			afterPool, poolErr := mgr.Keeper().GetPool(ctx, chain.GetGasAsset())
			c.Assert(poolErr, IsNil)
			c.Check(afterPool, DeepEquals, pool)
			c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()), DeepEquals, reserve)
		}

		ctx = ctx.WithBlockHeight(source.StatusSince + 3)
		c.Assert(network.migrateFunds(ctx, mgr), IsNil)
		items, err := mgr.TxOutStore().GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1, Commentf("%s round 4", chain))
		item := items[0]
		expected := balance.QuoUint64(2).Sub(gas.Amount)
		c.Check(item.Coin.Amount.Equal(expected), Equals, true)
		c.Check(item.Coin.Amount.GTE(minimum), Equals, true)
		c.Check(balance.Sub(item.Coin.Amount).Sub(gas.Amount).GTE(minimum), Equals, true)
		c.Check(item.VaultPubKey, Equals, source.PubKey)
		c.Check(item.VaultPubKeyEddsa, Equals, source.PubKeyEddsa)
		address, err := target.GetAddress(chain)
		c.Assert(err, IsNil)
		c.Check(item.ToAddress, Equals, address)
		c.Check(item.Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
		c.Check(item.MaxGas, DeepEquals, common.Gas{gas})
		scheduledHeight := ctx.BlockHeight()
		storedSource, err := mgr.Keeper().GetVault(ctx, source.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedSource.PendingTxBlockHeights, DeepEquals, []int64{scheduledHeight})
		c.Check(storedSource.Coins, DeepEquals, source.Coins)
		storedTarget, err := mgr.Keeper().GetVault(ctx, target.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedTarget.HasFunds(), Equals, false)
		afterPool, err := mgr.Keeper().GetPool(ctx, chain.GetGasAsset())
		c.Assert(err, IsNil)
		c.Check(afterPool, DeepEquals, pool)
		c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()), DeepEquals, reserve)

		// A pending migration prevents another round from scheduling a duplicate.
		ctx = ctx.WithBlockHeight(scheduledHeight + 1)
		c.Assert(network.migrateFunds(ctx, mgr), IsNil)
		items, err = mgr.TxOutStore().GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Check(items, HasLen, 0)
		c.Check(mgr.Keeper().GetPendingOutbounds(ctx, chain.GetGasAsset()), HasLen, 1)
		storedSource, err = mgr.Keeper().GetVault(ctx, source.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedSource.PendingTxBlockHeights, DeepEquals, []int64{scheduledHeight})
	}
}

func (*NetworkManagerTestSuite) TestMigrateFundsCleansUpBelowMinimumDust(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		balance := chain.ExistentialDeposit().QuoUint64(2)
		ctx, mgr, source, target := newMinimumMigrationTest(c, chain, balance)
		network := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
		asset := chain.GetGasAsset()
		pool, err := mgr.Keeper().GetPool(ctx, asset)
		c.Assert(err, IsNil)
		reserve := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native())
		asgard := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native())

		for round := int64(1); round <= 5; round++ {
			ctx = ctx.WithBlockHeight(source.StatusSince + round - 1)
			c.Assert(network.migrateFunds(ctx, mgr), IsNil, Commentf("%s round %d", chain, round))
			assertMigrationNotScheduled(c, ctx, mgr, source, target)
			afterPool, poolErr := mgr.Keeper().GetPool(ctx, asset)
			c.Assert(poolErr, IsNil)
			c.Check(afterPool, DeepEquals, pool)
			c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()), DeepEquals, reserve)
			c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native()), DeepEquals, asgard)
		}

		// Existing cleanup removes tracked dust and reimburses the pool from Reserve.
		ctx = ctx.WithBlockHeight(source.StatusSince + 5)
		c.Assert(network.migrateFunds(ctx, mgr), IsNil)
		storedSource, err := mgr.Keeper().GetVault(ctx, source.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedSource.HasFunds(), Equals, false)
		c.Check(storedSource.PendingTxBlockHeights, HasLen, 0)
		c.Check(mgr.Keeper().GetPendingOutbounds(ctx, asset), HasLen, 0)
		storedTarget, err := mgr.Keeper().GetVault(ctx, target.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedTarget.HasFunds(), Equals, false)
		afterPool, err := mgr.Keeper().GetPool(ctx, asset)
		c.Assert(err, IsNil)
		reimbursement := pool.AssetValueInRune(balance)
		c.Check(afterPool.BalanceAsset.Equal(pool.BalanceAsset.Sub(balance)), Equals, true)
		c.Check(afterPool.BalanceRune.Equal(pool.BalanceRune.Add(reimbursement)), Equals, true)
		c.Check(afterPool.LPUnits.Equal(pool.LPUnits), Equals, true)
		afterReserve := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native())
		afterAsgard := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native())
		c.Check(reserve.Sub(afterReserve).Equal(reimbursement), Equals, true)
		c.Check(afterAsgard.Sub(asgard).Equal(reimbursement), Equals, true)

		// The next pass retires the empty vault without reimbursing it again.
		ctx = ctx.WithBlockHeight(source.StatusSince + 6)
		c.Assert(network.migrateFunds(ctx, mgr), IsNil)
		storedSource, err = mgr.Keeper().GetVault(ctx, source.PubKey)
		c.Assert(err, IsNil)
		c.Check(storedSource.Status, Equals, InactiveVault)
		finalPool, err := mgr.Keeper().GetPool(ctx, asset)
		c.Assert(err, IsNil)
		c.Check(finalPool, DeepEquals, afterPool)
		c.Check(mgr.Keeper().GetPendingOutbounds(ctx, asset), HasLen, 0)
		c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()), DeepEquals, afterReserve)
		c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native()), DeepEquals, afterAsgard)
	}
}
