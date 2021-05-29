package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	. "gopkg.in/check.v1"
)

type LPManagerV1TestSuite struct{}

var _ = Suite(&LPManagerV1TestSuite{})

func (m LPManagerV1TestSuite) TestLPManager(c *C) {
	ctx, k := setupKeeperForTest(c)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.PoolUnits = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	poolBNB := NewPool()
	poolBNB.Asset = common.BNBAsset
	poolBNB.BalanceRune = cosmos.NewUint(1000 * common.One)
	poolBNB.BalanceAsset = cosmos.NewUint(1000 * common.One)
	poolBNB.PoolUnits = cosmos.NewUint(1000 * common.One)
	poolBNB.Status = PoolStaged
	c.Assert(k.SetPool(ctx, poolBNB), IsNil)

	mgr := NewDummyMgr()

	h := NewAddLiquidityHandler(k, mgr)
	v := GetCurrentVersion()
	constantAccessor := constants.GetConstantValues(v)
	// add some liquidity
	for i := 0; i <= 10; i++ {
		c.Assert(h.addLiquidityV46(ctx,
			common.BTCAsset,
			cosmos.NewUint(common.One),
			cosmos.NewUint(common.One),
			GetRandomTHORAddress(),
			GetRandomBTCAddress(),
			GetRandomTxHash(),
			false,
			constantAccessor), IsNil)
	}
	pendingAssetAddr := GetRandomTHORAddress()
	c.Assert(h.addLiquidityV46(ctx,
		common.BTCAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(1000),
		pendingAssetAddr,
		GetRandomBTCAddress(),
		GetRandomTxHash(),
		true,
		constantAccessor), IsNil)
	pendingRuneAddr := GetRandomTHORAddress()
	c.Assert(h.addLiquidityV46(ctx,
		common.BTCAsset,
		cosmos.NewUint(1000),
		cosmos.ZeroUint(),
		pendingRuneAddr,
		GetRandomBTCAddress(),
		GetRandomTxHash(),
		true,
		constantAccessor), IsNil)
	btcPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btcPool.PendingInboundAsset.Equal(cosmos.NewUint(1000)), Equals, true)
	c.Assert(btcPool.PendingInboundRune.Equal(cosmos.NewUint(1000)), Equals, true)

	ctx = ctx.WithBlockHeight(10 * constantAccessor.GetInt64Value(constants.PendingLiquidityCheckInterval))
	lpm := NewLPManager(k)
	c.Assert(lpm.EndBlock(ctx, mgr, v, constantAccessor), IsNil)
	lp, err := k.GetLiquidityProvider(ctx, common.BTCAsset, pendingAssetAddr)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingAsset.IsZero(), Equals, true)
	c.Assert(lp.Units.GT(cosmos.ZeroUint()), Equals, true)
	btcPool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btcPool.PendingInboundAsset.IsZero(), Equals, true)
	c.Assert(btcPool.PendingInboundRune.IsZero(), Equals, true)
}
