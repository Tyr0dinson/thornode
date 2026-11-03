package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Migration rounds follow the wall-clock age of the retiring vault across a
// block time change: each round fires once, in the block its age in reference
// blocks crosses another FundMigrationInterval, and moves the share of funds
// that round's number calls for, ending with everything in the last round.
func (*NetworkManagerTestSuite) TestMigrateFundsRoundsAcrossBlockTimeChange(c *C) {
	ctx, mgr := setupManagerForTest(c)
	dummy := NewTxStoreDummy()
	mgr.txOutStore = dummy
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	ref := constants.ThorchainBlockTime.Milliseconds()
	k := mgr.Keeper()

	const since = int64(100)
	k.SetMimir(ctx, constants.FundMigrationInterval.String(), 10)
	k.SetMimir(ctx, constants.ChurnMigrateRounds.String(), 5)
	// blocks 101-130 are half-length, later ones back at the reference
	k.SetBlockTimeHistory(ctx, ref, constants.BlockTimeHistory{{Height: since + 1, Ms: ref / 2}, {Height: since + 31, Ms: ref}})

	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain:              common.BTCChain,
		TransactionSize:    1,
		TransactionFeeRate: 1,
	}), IsNil)
	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	retiring.StatusSince = since
	retiring.Chains = common.Chains{common.BTCChain}.Strings()
	retiring.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(k.SetVault(ctx, retiring), IsNil)
	active := GetRandomVault()
	active.Status = ActiveVault
	active.Chains = common.Chains{common.BTCChain}.Strings()
	c.Assert(k.SetVault(ctx, active), IsNil)

	rounds := map[int64]cosmos.Uint{}
	for h := since; h <= since+70; h++ {
		before := len(dummy.blockOut.TxArray)
		c.Assert(networkMgr.migrateFunds(ctx.WithBlockHeight(h), mgr), IsNil)
		items := dummy.blockOut.TxArray[before:]
		if len(items) == 0 {
			continue
		}
		c.Assert(items, HasLen, 1, Commentf("height %d", h))
		rounds[h] = items[0].Coin.Amount
	}

	gas, err := mgr.GasMgr().GetMaxGas(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	share := func(n uint64) cosmos.Uint {
		return common.SafeSub(cosmos.NewUint(100*common.One).QuoUint64(n), gas.Amount)
	}
	// age 0 at 100; 10 reference blocks at 120 (20 half-length blocks); 15 at
	// 130, so 20 at 135 and 30 at 145; then 40 at 155 and 50 at 165. Counting
	// blocks would have fired at 110, 120, 130, ... with ages 5, 10, 15, ...
	c.Check(rounds, DeepEquals, map[int64]cosmos.Uint{
		100: share(5),                                                   // round 1 of 5: a fifth
		120: share(4),                                                   // round 2: a quarter of what is left
		135: share(3),                                                   // round 3
		145: share(2),                                                   // round 4
		155: common.SafeSub(cosmos.NewUint(100*common.One), gas.Amount), // round 5: all of it
		165: common.SafeSub(cosmos.NewUint(100*common.One), gas.Amount), // past the last round: all of it
	})
}

// A block time change keeps every queued outbound visible to the solvency
// scan, including a backlog that runs past TxOutDelayMax: unpaid outbounds are
// still subtracted from the vault, so they are not counted as surplus.
func (*NetworkManagerTestSuite) TestSolvencyCountsQueueTailAfterBlockTimeChange(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MaxTxOutOffset.String(), 5)
	k.SetMimir(ctx, constants.TxOutDelayMax.String(), 10)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(85 * common.One)
	pool.BalanceRune = cosmos.NewUint(85 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// 15 unpaid 1 BTC outbounds, one per height out to offset + delay max
	const h = int64(100)
	ctx = ctx.WithBlockHeight(h - 1)
	for d := int64(1); d <= 15; d++ {
		c.Assert(k.AppendTxOut(ctx, h-1+d, TxOutItem{
			Chain:       common.BTCChain,
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: vault.PubKey,
			InHash:      GetRandomTxHash(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		}), IsNil)
	}

	// blocks slow down at h
	k.SetMimir(ctx, constants.BlockTimeMs.String(), constants.ThorchainBlockTime.Milliseconds()*2)
	ctx = ctx.WithBlockHeight(h)
	k.UpdateBlockTime(ctx)

	// 100 held - 85 owed to the pool - 15 queued: nothing over (converting only
	// to TxOutDelayMax left 5 BTC behind a gap, counted as surplus)
	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(solvency, HasLen, 1)
	c.Check(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(solvency[0].Amount.String(), Equals, "0")
}
