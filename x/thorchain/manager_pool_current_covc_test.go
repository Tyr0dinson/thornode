package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type PoolMgrCovCBSuite struct{}

var _ = Suite(&PoolMgrCovCBSuite{})

func (s *PoolMgrCovCBSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func covCBSetPool(c *C, ctx cosmos.Context, mgr Manager, asset common.Asset, status PoolStatus, rune, assetAmt uint64) Pool {
	pool := NewPool()
	pool.Asset = asset
	pool.Status = status
	pool.BalanceRune = cosmos.NewUint(rune)
	pool.BalanceAsset = cosmos.NewUint(assetAmt)
	pool.LPUnits = cosmos.NewUint(assetAmt)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return pool
}

// TestEndBlockCyclesPoolsAndCleansPendingCovCB drives PoolMgr.EndBlock with
// mimir-configured cycling parameters: the staged pool with the deepest rune
// is promoted, the shallowest available pool is demoted to stay under the max,
// the staged pool pays the stage cost to the reserve, and aged pending
// liquidity is committed.
func (s *PoolMgrCovCBSuite) TestEndBlockCyclesPoolsAndCleansPendingCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()

	k.SetMimir(ctx, constants.PoolCycle.String(), 10)
	k.SetMimir(ctx, constants.MaxAvailablePools.String(), 1)
	k.SetMimir(ctx, constants.MinRunePoolDepth.String(), 1)
	k.SetMimir(ctx, constants.StagedPoolCost.String(), 1000)
	k.SetMimir(ctx, constants.PendingLiquidityAgeLimit.String(), 10)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	assetA, err := common.NewAsset("ETH.AAA-0XAAAA")
	c.Assert(err, IsNil)
	assetB, err := common.NewAsset("ETH.BBB-0XBBBB")
	c.Assert(err, IsNil)
	assetC, err := common.NewAsset("ETH.CCC-0XCCCC")
	c.Assert(err, IsNil)

	covCBSetPool(c, ctx, mgr, assetA, PoolAvailable, 100*common.One, 100*common.One)
	covCBSetPool(c, ctx, mgr, assetB, PoolAvailable, 200*common.One, 100*common.One)
	covCBSetPool(c, ctx, mgr, assetC, PoolStaged, 500*common.One, 100*common.One)

	// gas pool with aged pending rune
	btc := NewPool()
	btc.Asset = common.BTCAsset
	btc.Status = PoolAvailable
	btc.BalanceRune = cosmos.NewUint(100_000)
	btc.BalanceAsset = cosmos.NewUint(100_000)
	btc.LPUnits = cosmos.NewUint(1000)
	btc.PendingInboundRune = cosmos.NewUint(501)
	c.Assert(k.SetPool(ctx, btc), IsNil)
	lp := LiquidityProvider{
		Asset:         common.BTCAsset,
		RuneAddress:   GetRandomRUNEAddress(),
		AssetAddress:  GetRandomBTCAddress(),
		PendingRune:   cosmos.NewUint(501),
		Units:         cosmos.NewUint(300),
		LastAddHeight: 1,
	}
	k.SetLiquidityProvider(ctx, lp)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	pm := newPoolMgr()
	c.Assert(pm.EndBlock(ctx, mgr), IsNil)

	poolA, err := k.GetPool(ctx, assetA)
	c.Assert(err, IsNil)
	c.Assert(poolA.Status, Equals, PoolStaged)
	c.Assert(poolA.StatusSince, Equals, ctx.BlockHeight())
	poolB, err := k.GetPool(ctx, assetB)
	c.Assert(err, IsNil)
	c.Assert(poolB.Status, Equals, PoolAvailable)
	poolC, err := k.GetPool(ctx, assetC)
	c.Assert(err, IsNil)
	c.Assert(poolC.Status, Equals, PoolAvailable)
	c.Assert(poolC.BalanceRune.Uint64(), Equals, uint64(500*common.One-1000))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveAfter.Sub(reserveBefore).Uint64(), Equals, uint64(1000))

	// pending liquidity was committed into the BTC pool
	btcAfter, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btcAfter.BalanceRune.Uint64(), Equals, uint64(100_501))
	c.Assert(btcAfter.PendingInboundRune.IsZero(), Equals, true)
	lpAfter, err := k.GetLiquidityProvider(ctx, common.BTCAsset, lp.GetAddress())
	c.Assert(err, IsNil)
	c.Assert(lpAfter.PendingRune.IsZero(), Equals, true)
}

// TestEndBlockMimirFallbackCovCB covers the constant fallbacks used when the
// cycling mimirs are unset: with the default (huge) minimum rune depth a
// shallow available pool is demoted.
func (s *PoolMgrCovCBSuite) TestEndBlockMimirFallbackCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(20)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.PoolCycle.String(), 10)

	asset, err := common.NewAsset("ETH.DDD-0XDDDD")
	c.Assert(err, IsNil)
	covCBSetPool(c, ctx, mgr, asset, PoolAvailable, common.One, common.One)

	c.Assert(newPoolMgr().EndBlock(ctx, mgr), IsNil)
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(pool.Status, Equals, PoolStaged)

	// ragnarok in progress: no cycling happens
	asset2, err := common.NewAsset("ETH.EEE-0XEEEE")
	c.Assert(err, IsNil)
	covCBSetPool(c, ctx, mgr, asset2, PoolAvailable, common.One, common.One)
	k.SetRagnarokBlockHeight(ctx, 1)
	c.Assert(newPoolMgr().EndBlock(ctx, mgr), IsNil)
	pool2, err := k.GetPool(ctx, asset2)
	c.Assert(err, IsNil)
	c.Assert(pool2.Status, Equals, PoolAvailable)
}

func (s *PoolMgrCovCBSuite) TestCleanupPendingLiquiditySkipsCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	pm := newPoolMgr()

	mk := func(asset common.Asset, status PoolStatus, assetAddr common.Address) {
		p := NewPool()
		p.Asset = asset
		p.Status = status
		p.BalanceRune = cosmos.NewUint(100 * common.One)
		p.BalanceAsset = cosmos.NewUint(100 * common.One)
		p.LPUnits = cosmos.NewUint(100 * common.One)
		p.PendingInboundRune = cosmos.NewUint(common.One)
		c.Assert(k.SetPool(ctx, p), IsNil)
		k.SetLiquidityProvider(ctx, LiquidityProvider{
			Asset:         asset,
			RuneAddress:   GetRandomRUNEAddress(),
			AssetAddress:  assetAddr,
			PendingRune:   cosmos.NewUint(common.One),
			Units:         cosmos.NewUint(common.One),
			LastAddHeight: 1,
		})
	}
	// suspended pool and halted-chain pool are never cleaned
	mk(common.BTCAsset, PoolSuspended, GetRandomBTCAddress())
	mk(common.ETHAsset, PoolAvailable, GetRandomETHAddress())
	k.SetMimir(ctx, "HaltETHChain", 1)

	// disabled when age limit <= 0
	k.SetMimir(ctx, constants.PendingLiquidityAgeLimit.String(), 0)
	pm.cleanupPendingLiquidity(ctx, mgr)
	k.SetMimir(ctx, constants.PendingLiquidityAgeLimit.String(), 10)
	pm.cleanupPendingLiquidity(ctx, mgr)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		p, err := k.GetPool(ctx, asset)
		c.Assert(err, IsNil)
		c.Assert(p.PendingInboundRune.Uint64(), Equals, uint64(common.One))
		c.Assert(p.BalanceRune.Uint64(), Equals, uint64(100*common.One))
	}

	// more pools than the age limit: separator falls back to 1 and every
	// eligible pool is processed in the same block
	k.SetMimir(ctx, "HaltETHChain", 0)
	k.SetMimir(ctx, constants.PendingLiquidityAgeLimit.String(), 1)
	mk(common.DOGEAsset, PoolStaged, GetRandomDOGEAddress())
	pm.cleanupPendingLiquidity(ctx, mgr)
	for _, asset := range []common.Asset{common.DOGEAsset, common.ETHAsset} {
		p, err := k.GetPool(ctx, asset)
		c.Assert(err, IsNil)
		c.Assert(p.PendingInboundRune.IsZero(), Equals, true, Commentf("%s", asset))
		c.Assert(p.BalanceRune.Uint64(), Equals, uint64(101*common.One), Commentf("%s", asset))
	}
	btc, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btc.PendingInboundRune.Uint64(), Equals, uint64(common.One))
}

func (s *PoolMgrCovCBSuite) setupSaverCovCB(c *C, ctx cosmos.Context, mgr Manager) (Pool, LiquidityProvider) {
	k := mgr.Keeper()
	l1 := NewPool()
	l1.Asset = common.BTCAsset
	l1.Status = PoolAvailable
	l1.BalanceRune = cosmos.NewUint(100 * common.One)
	l1.BalanceAsset = cosmos.NewUint(100 * common.One)
	l1.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, l1), IsNil)

	synthAsset := common.BTCAsset.GetSyntheticAsset()
	synth := NewPool()
	synth.Asset = synthAsset
	synth.Status = PoolAvailable
	synth.BalanceAsset = cosmos.NewUint(50 * common.One)
	synth.LPUnits = cosmos.NewUint(50 * common.One)
	c.Assert(k.SetPool(ctx, synth), IsNil)

	// synth supply above 10% of 2x pool depth (20 BTC)
	k.SetMimir(ctx, constants.MaxSynthPerPoolDepth.String(), 1000)
	coin := common.NewCoin(synthAsset, cosmos.NewUint(50*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)
	return synth, LiquidityProvider{}
}

func (s *PoolMgrCovCBSuite) TestCheckSaversUtilizationCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	k := mgr.Keeper()
	pm := newPoolMgr()

	// disabled by default (SaversEjectInterval == 0)
	c.Assert(pm.checkSaversUtilization(ctx, mgr), IsNil)

	k.SetMimir(ctx, constants.SaversEjectInterval.String(), 10)
	// no active nodes
	err := pm.checkSaversUtilization(ctx, mgr)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "dev err: no active node accounts")

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// global trading halt short-circuits
	k.SetMimir(ctx, constants.MimirKeyHaltTradingGlobal, 1)
	c.Assert(pm.checkSaversUtilization(ctx, mgr), IsNil)
	k.SetMimir(ctx, constants.MimirKeyHaltTradingGlobal, 0)

	// non-interval block
	c.Assert(pm.checkSaversUtilization(ctx.WithBlockHeight(101), mgr), IsNil)

	synth, _ := s.setupSaverCovCB(c, ctx, mgr)

	// a saver pool above the cap but with no LP positions is logged and skipped
	c.Assert(pm.checkSaversUtilization(ctx, mgr), IsNil)

	// add saver positions: the latest add (and smallest at equal height) is ejected
	older := LiquidityProvider{
		Asset:         synth.Asset,
		AssetAddress:  GetRandomBTCAddress(),
		Units:         cosmos.NewUint(10 * common.One),
		LastAddHeight: 3,
	}
	bigger := LiquidityProvider{
		Asset:         synth.Asset,
		AssetAddress:  GetRandomBTCAddress(),
		Units:         cosmos.NewUint(30 * common.One),
		LastAddHeight: 5,
	}
	target := LiquidityProvider{
		Asset:         synth.Asset,
		AssetAddress:  GetRandomBTCAddress(),
		Units:         cosmos.NewUint(10 * common.One),
		LastAddHeight: 5,
	}
	for _, lp := range []LiquidityProvider{older, bigger, target} {
		k.SetLiquidityProvider(ctx, lp)
	}

	// halted chain trading skips the pool entirely
	k.SetMimir(ctx, "HaltBTCTrading", 1)
	c.Assert(pm.checkSaversUtilization(ctx, mgr), IsNil)
	lp, err := k.GetLiquidityProvider(ctx, synth.Asset, target.AssetAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Equal(target.Units), Equals, true)
	k.SetMimir(ctx, "HaltBTCTrading", 0)

	c.Assert(pm.checkSaversUtilization(ctx, mgr), IsNil)

	lp, err = k.GetLiquidityProvider(ctx, synth.Asset, target.AssetAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.IsZero(), Equals, true, Commentf("target units %s", lp.Units))
	lp, err = k.GetLiquidityProvider(ctx, synth.Asset, bigger.AssetAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Equal(bigger.Units), Equals, true)
	lp, err = k.GetLiquidityProvider(ctx, synth.Asset, older.AssetAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Equal(older.Units), Equals, true)
}

func (s *PoolMgrCovCBSuite) TestRelatedAssetLiabilityReasonsCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset, err := common.NewAsset("ETH.FFF-0XFFFF")
	c.Assert(err, IsNil)
	none := map[string]struct{}{}

	reason, err := pm.getRelatedAssetLiabilityReason(ctx, common.TCY, mgr, none)
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "")

	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, none)
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "")

	queued := func(a common.Asset) map[string]struct{} {
		return map[string]struct{}{a.String(): {}}
	}
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, queued(asset.GetSyntheticAsset()))
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "queued synth swap liabilities remain")
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, queued(asset.GetTradeAsset()))
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "queued trade asset liabilities remain")
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, queued(asset.GetSecuredAsset()))
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "queued secured asset liabilities remain")

	// pending secured outbound
	vault := GetRandomVault()
	c.Assert(k.SetVault(ctx, vault), IsNil)
	item := TxOutItem{
		Chain:       asset.GetChain(),
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(asset.GetSecuredAsset(), cosmos.NewUint(common.One)),
		InHash:      GetRandomTxHash(),
		Memo:        "OUT:secured",
	}
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+1, item), IsNil)
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, none)
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "pending secured outbound liabilities remain")

	// pending synth outbound takes priority
	item.Coin = common.NewCoin(asset.GetSyntheticAsset(), cosmos.NewUint(common.One))
	item.InHash = GetRandomTxHash()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+1, item), IsNil)
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset, mgr, none)
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "pending synth outbound liabilities remain")

	// synthetic pool state on a fresh asset
	asset2, err := common.NewAsset("ETH.GGG-0XGGGG")
	c.Assert(err, IsNil)
	sp := NewPool()
	sp.Asset = asset2.GetSyntheticAsset()
	sp.BalanceAsset = cosmos.NewUint(1)
	c.Assert(k.SetPool(ctx, sp), IsNil)
	reason, err = pm.getRelatedAssetLiabilityReason(ctx, asset2, mgr, none)
	c.Assert(err, IsNil)
	c.Assert(reason, Equals, "synthetic pool state remains")
}

func (s *PoolMgrCovCBSuite) TestBurnStagedPoolGuardsCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset, err := common.NewAsset("ETH.HHH-0XHHHH")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolStaged
	pool.BalanceAsset = cosmos.NewUint(common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	none := map[string]struct{}{}

	pending := pool
	pending.PendingInboundAsset = cosmos.NewUint(1)
	err = pm.burnStagedPool(ctx, pending, mgr, none)
	c.Assert(err, ErrorMatches, "cannot burn staged pool .* with pending inbound liquidity")

	err = pm.burnStagedPool(ctx, pool, mgr, map[string]struct{}{asset.String(): {}})
	c.Assert(err, ErrorMatches, "cannot burn staged pool .* with queued swap liabilities")

	err = pm.burnStagedPool(ctx, pool, mgr, map[string]struct{}{asset.GetTradeAsset().String(): {}})
	c.Assert(err, ErrorMatches, "cannot burn staged pool .* while related asset liabilities remain: queued trade asset liabilities remain")

	vault := GetRandomVault()
	c.Assert(k.SetVault(ctx, vault), IsNil)
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+1, TxOutItem{
		Chain:       asset.GetChain(),
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(asset, cosmos.NewUint(common.One)),
		InHash:      GetRandomTxHash(),
		Memo:        "OUT:x",
	}), IsNil)
	err = pm.burnStagedPool(ctx, pool, mgr, none)
	c.Assert(err, ErrorMatches, "cannot burn staged pool .* with pending outbound liabilities")

	// pool still exists after all rejected burns
	after, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(after.IsEmpty(), Equals, false)
}

func (s *PoolMgrCovCBSuite) TestQueuedSwapLiabilityAssetsCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	// queued swap with empty coins is ignored
	c.Assert(k.SetSwapQueueItem(ctx, MsgSwap{
		Tx:          common.Tx{ID: GetRandomTxHash(), Chain: common.BTCChain},
		TargetAsset: common.ETHAsset,
	}, 0), IsNil)

	// advanced queue item with no state is ignored; with state marks liabilities
	noState := MsgSwap{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		},
		TargetAsset: common.DOGEAsset,
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, noState), IsNil)

	withState := MsgSwap{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		},
		TargetAsset: common.ETHAsset,
		State: &types.SwapState{
			Deposit:   cosmos.NewUint(common.One),
			In:        cosmos.NewUint(common.One / 2),
			Out:       cosmos.NewUint(5),
			Withdrawn: cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, withState), IsNil)

	liab, err := pm.getQueuedSwapLiabilityAssets(ctx, mgr)
	c.Assert(err, IsNil)
	_, hasBTC := liab[common.BTCAsset.String()]
	_, hasETH := liab[common.ETHAsset.String()]
	_, hasDOGE := liab[common.DOGEAsset.String()]
	c.Assert(hasBTC, Equals, true)
	c.Assert(hasETH, Equals, true)
	c.Assert(hasDOGE, Equals, false)
	c.Assert(liab, HasLen, 2)
}

// TestCyclePoolsNoSwapWhenOnDeckShallowerCovCB: at the available-pool limit a
// staged pool shallower than the weakest available pool is not promoted.
func (s *PoolMgrCovCBSuite) TestCyclePoolsNoSwapWhenOnDeckShallowerCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	assetA, err := common.NewAsset("ETH.III-0XIIII")
	c.Assert(err, IsNil)
	assetB, err := common.NewAsset("ETH.JJJ-0XJJJJ")
	c.Assert(err, IsNil)
	assetC, err := common.NewAsset("ETH.KKK-0XKKKK")
	c.Assert(err, IsNil)
	covCBSetPool(c, ctx, mgr, assetA, PoolAvailable, 100*common.One, 100*common.One)
	covCBSetPool(c, ctx, mgr, assetB, PoolAvailable, 200*common.One, 100*common.One)
	covCBSetPool(c, ctx, mgr, assetC, PoolStaged, 50*common.One, 100*common.One)

	c.Assert(newPoolMgr().cyclePools(ctx, 2, 1, 0, mgr), IsNil)

	expected := []struct {
		asset  common.Asset
		status PoolStatus
	}{
		{assetA, PoolAvailable},
		{assetB, PoolAvailable},
		{assetC, PoolStaged},
	}
	for _, e := range expected {
		p, err := k.GetPool(ctx, e.asset)
		c.Assert(err, IsNil)
		c.Assert(p.Status, Equals, e.status, Commentf("%s", e.asset))
	}
}

func (s *PoolMgrCovCBSuite) TestCommitPendingLiquidityGuardsCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pm := newPoolMgr()

	pool := NewPool()
	pool.Asset = common.BTCAsset
	// nothing pending: no-op even without active nodes
	c.Assert(pm.commitPendingLiquidity(ctx, pool, mgr), IsNil)

	pool.PendingInboundRune = cosmos.NewUint(1)
	err := pm.commitPendingLiquidity(ctx, pool, mgr)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "dev err: no active node accounts")
}

func (s *PoolMgrCovCBSuite) TestQueuedLegacyStreamingDepositLiabilityCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	txID := GetRandomTxHash()
	c.Assert(k.SetSwapQueueItem(ctx, MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.BTCChain,
			FromAddress: GetRandomBTCAddress(),
			Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:    common.ETHAsset,
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}, 0), IsNil)
	swp := NewStreamingSwap(txID, 5, 1, cosmos.ZeroUint(), cosmos.NewUint(10*common.One))
	swp.In = cosmos.NewUint(2 * common.One)
	k.SetStreamingSwap(ctx, swp)

	liab, err := newPoolMgr().getQueuedSwapLiabilityAssets(ctx, mgr)
	c.Assert(err, IsNil)
	_, hasBTC := liab[common.BTCAsset.String()]
	_, hasETH := liab[common.ETHAsset.String()]
	c.Assert(hasBTC, Equals, true)
	c.Assert(hasETH, Equals, false)
}
