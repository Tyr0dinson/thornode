package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type ManagerMiscCovCSuite struct{}

var _ = Suite(&ManagerMiscCovCSuite{})

// covCErrKeeper fails selected keeper reads.
type covCErrKeeper struct {
	keeper.Keeper
	failMimir         bool
	failChainContract bool
}

func (k covCErrKeeper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if k.failMimir {
		return -1, errKaboom
	}
	return k.Keeper.GetMimir(ctx, key)
}

func (k covCErrKeeper) GetChainContract(ctx cosmos.Context, chain common.Chain) (ChainContract, error) {
	if k.failChainContract {
		return ChainContract{}, errKaboom
	}
	return k.Keeper.GetChainContract(ctx, chain)
}

func (s *ManagerMiscCovCSuite) TestOracleBeginBlockClearsPricesCovC(c *C) {
	ctx, k := setupKeeperForTest(c)
	om := newOracleMgr(k)

	// nothing stored: no-op
	c.Assert(om.BeginBlock(ctx), IsNil)

	c.Assert(k.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "100000"}), IsNil)
	c.Assert(k.SetPrice(ctx, OraclePrice{Symbol: "ETH", Price: "3000"}), IsNil)

	// corrupt one of the stored entries in place
	var corruptKey []byte
	iter := k.GetPriceIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), "ETH") {
			corruptKey = append([]byte(nil), iter.Key()...)
		}
	}
	c.Assert(iter.Close(), IsNil)
	c.Assert(corruptKey, NotNil)
	ctx.KVStore(keyThorchain).Set(corruptKey, []byte{0xff, 0xff, 0xff})

	c.Assert(om.BeginBlock(ctx), IsNil)

	// all entries, including the malformed one, are removed
	iter = k.GetPriceIterator(ctx)
	c.Check(iter.Valid(), Equals, false)
	c.Assert(iter.Close(), IsNil)
	_, err := k.GetPrice(ctx, "BTC")
	c.Check(err, NotNil)
	c.Check(ctx.KVStore(keyThorchain).Has(corruptKey), Equals, false)
}

func (s *ManagerMiscCovCSuite) TestDummySwitchManagerCovC(c *C) {
	ctx, _ := setupKeeperForTest(c)
	sm := NewDummySwitchManager()
	c.Check(sm.IsSwitch(ctx, common.BTCAsset), Equals, false)
	addr, err := sm.Switch(ctx, common.BTCAsset, cosmos.NewUint(1), GetRandomBech32Addr(), GetRandomBTCAddress(), GetRandomTxHash())
	c.Check(err, IsNil)
	c.Check(addr, Equals, common.NoAddress)
}

func (s *ManagerMiscCovCSuite) TestScheduledMigrationMimirErrorCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.K = covCErrKeeper{Keeper: mgr.K, failMimir: true}
	sm := newScheduledMigrationMgr(mgr)
	c.Check(sm.EndBlock(ctx, mgr), Equals, errKaboom)
}

func (s *ManagerMiscCovCSuite) TestRouterUpgradeCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	r := NewRouterUpgradeController(mgr)

	// router lookups for every supported chain and an unsupported one
	for _, chain := range []common.Chain{common.ETHChain, common.AVAXChain, common.BSCChain, common.BASEChain} {
		_, _, err := r.getChainOldAndNewRouters(chain)
		c.Check(err, IsNil, Commentf("%s", chain))
	}
	_, _, err := r.getChainOldAndNewRouters(common.BTCChain)
	c.Check(err, ErrorMatches, ".*invalid chain")

	// ETH: current router is not the expected old router -> untouched
	other := GetRandomETHAddress()
	k.SetChainContract(ctx, ChainContract{Chain: common.ETHChain, Router: other})
	// AVAX: current router is the old router -> upgraded
	avaxOld, err := common.NewAddress(avaxOldRouter)
	c.Assert(err, IsNil)
	k.SetChainContract(ctx, ChainContract{Chain: common.AVAXChain, Router: avaxOld})
	// BSC: on mocknet old == new == "" so "new router already set" short circuits
	for _, chain := range []common.Chain{common.ETHChain, common.AVAXChain, common.BSCChain} {
		k.SetMimir(ctx, "MimirUpgradeContract"+chain.String(), 1)
	}
	// BASE: mimir explicitly disabled
	k.SetMimir(ctx, "MimirUpgradeContract"+common.BASEChain.String(), 0)

	r.Process(ctx)

	eth, err := k.GetChainContract(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(eth.Router.Equals(other), Equals, true)
	avax, err := k.GetChainContract(ctx, common.AVAXChain)
	c.Assert(err, IsNil)
	c.Check(strings.EqualFold(avax.Router.String(), avaxNewRouter), Equals, true)

	mimir := func(chain common.Chain) int64 {
		v, mErr := k.GetMimir(ctx, "MimirUpgradeContract"+chain.String())
		c.Assert(mErr, IsNil)
		return v
	}
	// the AVAX upgrade mimir is cleared after the upgrade, the others remain
	c.Check(mimir(common.AVAXChain), Equals, int64(-1))
	c.Check(mimir(common.ETHChain), Equals, int64(1))
	c.Check(mimir(common.BSCChain), Equals, int64(1))
	c.Check(mimir(common.BASEChain), Equals, int64(0))

	// keeper read errors are logged and the chain skipped
	k.SetChainContract(ctx, ChainContract{Chain: common.AVAXChain, Router: avaxOld})
	k.SetMimir(ctx, "MimirUpgradeContract"+common.AVAXChain.String(), 1)
	mgr.K = covCErrKeeper{Keeper: k, failChainContract: true}
	c.Assert(r.upgradeContract(ctx, mgr.GetVersion()), IsNil)
	mgr.K = covCErrKeeper{Keeper: k, failMimir: true}
	c.Assert(r.upgradeContract(ctx, mgr.GetVersion()), IsNil)
	avax, err = k.GetChainContract(ctx, common.AVAXChain)
	c.Assert(err, IsNil)
	c.Check(avax.Router.Equals(avaxOld), Equals, true)
}
