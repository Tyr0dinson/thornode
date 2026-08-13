package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *KeeperMempoolSwapSuite) TestAnchorPricingUsesFinalizedPoolBalances(c *C) {
	ctx, k := setupKeeperForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	k.SetMimir(ctx, "TorAnchor-BTC-BTC", 1)

	baselineAnchor := k.AnchorMedian(ctx, []common.Asset{common.BTCAsset})
	baselineDollarsPerRune := k.DollarsPerRune(ctx)
	c.Assert(baselineAnchor.IsZero(), Equals, false)
	c.Assert(baselineDollarsPerRune.IsZero(), Equals, false)

	// Apply the speculative pool mutation first, as swap execution does. Until
	// its lifecycle record exists, canonical pricing sees the live balances.
	pool.BalanceRune = cosmos.NewUint(800 * common.One)
	pool.BalanceAsset = cosmos.NewUint(125 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	speculativeAnchor := k.AnchorMedian(ctx, []common.Asset{common.BTCAsset})
	speculativeDollarsPerRune := k.DollarsPerRune(ctx)
	c.Assert(speculativeAnchor.Equal(baselineAnchor), Equals, false)
	c.Assert(speculativeDollarsPerRune.Equal(baselineDollarsPerRune), Equals, false)

	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	effect := &record.PendingEffects.Pools[0]
	effect.Asset = common.BTCAsset
	effect.BalanceRuneIncrease = cosmos.ZeroUint()
	effect.BalanceRuneDecrease = cosmos.NewUint(200 * common.One)
	effect.BalanceAssetIncrease = cosmos.NewUint(25 * common.One)
	effect.BalanceAssetDecrease = cosmos.ZeroUint()
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)

	// Active speculative balances are excluded from both direct anchor pricing
	// and its canonical DollarsPerRune consumer.
	c.Check(k.AnchorMedian(ctx, []common.Asset{common.BTCAsset}).Equal(baselineAnchor), Equals, true)
	c.Check(k.DollarsPerRune(ctx).Equal(baselineDollarsPerRune), Equals, true)

	// Finality makes the already-applied live mutation authoritative.
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	c.Check(k.AnchorMedian(ctx, []common.Asset{common.BTCAsset}).Equal(speculativeAnchor), Equals, true)
	c.Check(k.DollarsPerRune(ctx).Equal(speculativeDollarsPerRune), Equals, true)

	// A later deep-reorg marker makes the same effects reversible again and
	// restores their exclusion while rollback is pending.
	record.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	record.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	c.Check(k.AnchorMedian(ctx, []common.Asset{common.BTCAsset}).Equal(baselineAnchor), Equals, true)
	c.Check(k.DollarsPerRune(ctx).Equal(baselineDollarsPerRune), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestAnchorPricingFallsBackToRawPool(c *C) {
	ctx, k := setupKeeperForTest(c)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = types.PoolStatus_Available
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(5 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	effect := &record.PendingEffects.Pools[0]
	effect.Asset = common.BTCAsset
	effect.BalanceRuneIncrease = cosmos.ZeroUint()
	effect.BalanceRuneDecrease = cosmos.ZeroUint()
	effect.BalanceAssetIncrease = cosmos.NewUint(10 * common.One)
	effect.BalanceAssetDecrease = cosmos.ZeroUint()
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)

	value := k.AnchorMedian(ctx, []common.Asset{common.BTCAsset})
	expected := pool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti * common.One))
	c.Check(value.Equal(expected), Equals, true)
	c.Check(value.IsZero(), Equals, false)
}
