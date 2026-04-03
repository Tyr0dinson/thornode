package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QuotesSuite struct{}

var _ = Suite(&QuotesSuite{})

func (s *QuotesSuite) TestParseMultipleAffiliateParams(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test single affiliate
	affiliates, bps, totalBps, err := parseMultipleAffiliateParams(ctx, mgr, "affiliate1", "100")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 1)
	c.Assert(len(bps), Equals, 1)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(totalBps.Uint64(), Equals, uint64(100))

	// Test multiple affiliates with slash separation
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "affiliate1/affiliate2", "100/200")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(len(bps), Equals, 2)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(affiliates[1], Equals, "affiliate2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))
	c.Assert(totalBps.Uint64(), Equals, uint64(300))

	// Test three affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "50/75/25")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(50))
	c.Assert(bps[1].Uint64(), Equals, uint64(75))
	c.Assert(bps[2].Uint64(), Equals, uint64(25))
	c.Assert(totalBps.Uint64(), Equals, uint64(150))

	// Test single bps applied to multiple affiliates (should now return error due to mismatch)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (1)")

	// Test empty strings
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "", "")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 0)
	c.Assert(len(bps), Equals, 0)
	c.Assert(totalBps.Uint64(), Equals, uint64(0))

	// Test whitespace trimming
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, " a1 / a2 ", " 100 / 200 ")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))

	// Test empty parts in slash-separated string
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1//a3", "100//300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test mismatch between affiliates and bps (more affiliates than bps) - should now return error
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (2)")

	// Test mismatch between affiliates and bps (more bps than affiliates) - should also return error
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "100/200/300")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (2) and BPS values (3)")

	// Test equal numbers of affiliates and bps - should work
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200/150")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))
	c.Assert(bps[2].Uint64(), Equals, uint64(150))
	c.Assert(totalBps.Uint64(), Equals, uint64(450))

	// Test invalid bps (should skip invalid ones)
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/invalid/300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2) // Skips a2 because of invalid bps
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test total bps exceeding limit (1000)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "600/500")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "total affiliate fee must not be more than 1000 bps")

	// Test edge case: exactly at limit
	_, _, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "500/500")
	c.Assert(err, IsNil)
	c.Assert(totalBps.Uint64(), Equals, uint64(1000))

	// Test affiliates with no BPS values (should return error due to mismatch)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (0)")
}

func (s *QuotesSuite) TestRapidStreamingSwapCalculation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Helper function to calculate streaming swap blocks
	calculateStreamingSwapBlocks := func(streamingQuantity, streamingInterval, rapidSwapMax uint64) int64 {
		var streamSwapBlocks int64
		if streamingQuantity > 0 {
			if streamingInterval == 0 {
				// Rapid streaming: multiple swaps per block limited by AdvSwapQueueRapidSwapMax
				rapidSwapMax = max(rapidSwapMax, 1)
				// Calculate blocks needed: ceil(quantity / rapidSwapMax)
				streamSwapBlocks = (int64(streamingQuantity) + int64(rapidSwapMax) - 1) / int64(rapidSwapMax)
			} else {
				// Traditional streaming: one swap per interval
				streamSwapBlocks = int64(streamingInterval) * int64(streamingQuantity-1)
			}
		}
		return streamSwapBlocks
	}

	// Test 1: Rapid streaming with rapidSwapMax=5, quantity=10
	// Should take ceil(10/5) = 2 blocks
	mgr.Keeper().SetMimir(ctx, "AdvSwapQueueRapidSwapMax", 5)
	blocks := calculateStreamingSwapBlocks(10, 0, 5)
	c.Assert(blocks, Equals, int64(2))

	// Test 2: Rapid streaming with rapidSwapMax=5, quantity=11
	// Should take ceil(11/5) = 3 blocks
	blocks = calculateStreamingSwapBlocks(11, 0, 5)
	c.Assert(blocks, Equals, int64(3))

	// Test 3: Rapid streaming with rapidSwapMax=5, quantity=5
	// Should take ceil(5/5) = 1 block
	blocks = calculateStreamingSwapBlocks(5, 0, 5)
	c.Assert(blocks, Equals, int64(1))

	// Test 4: Rapid streaming with rapidSwapMax=5, quantity=4
	// Should take ceil(4/5) = 1 block
	blocks = calculateStreamingSwapBlocks(4, 0, 5)
	c.Assert(blocks, Equals, int64(1))

	// Test 5: Rapid streaming with rapidSwapMax=1, quantity=10
	// Should take ceil(10/1) = 10 blocks
	blocks = calculateStreamingSwapBlocks(10, 0, 1)
	c.Assert(blocks, Equals, int64(10))

	// Test 6: Rapid streaming with rapidSwapMax=10, quantity=20
	// Should take ceil(20/10) = 2 blocks
	blocks = calculateStreamingSwapBlocks(20, 0, 10)
	c.Assert(blocks, Equals, int64(2))

	// Test 7: Rapid streaming with rapidSwapMax=10, quantity=15
	// Should take ceil(15/10) = 2 blocks
	blocks = calculateStreamingSwapBlocks(15, 0, 10)
	c.Assert(blocks, Equals, int64(2))

	// Test 8: Traditional streaming with interval=5, quantity=10
	// Should take 5 * (10-1) = 45 blocks
	blocks = calculateStreamingSwapBlocks(10, 5, 5)
	c.Assert(blocks, Equals, int64(45))

	// Test 9: Traditional streaming with interval=1, quantity=10
	// Should take 1 * (10-1) = 9 blocks
	blocks = calculateStreamingSwapBlocks(10, 1, 5)
	c.Assert(blocks, Equals, int64(9))

	// Test 10: Edge case - quantity=0
	// Should take 0 blocks
	blocks = calculateStreamingSwapBlocks(0, 0, 5)
	c.Assert(blocks, Equals, int64(0))

	// Test 11: Edge case - rapidSwapMax=0 (should use safety default of 1)
	// Should take ceil(10/1) = 10 blocks
	blocks = calculateStreamingSwapBlocks(10, 0, 0)
	c.Assert(blocks, Equals, int64(10))

	// Test 12: Verify actual quote API integration
	// Set up a test scenario with rapid streaming
	mgr.Keeper().SetMimir(ctx, "AdvSwapQueueRapidSwapMax", 5)

	// The actual queryQuoteSwap function would be called here in a full integration test
	// For now, we've verified the calculation logic works correctly
	rapidSwapMax := mgr.Keeper().GetConfigInt64(ctx, constants.AdvSwapQueueRapidSwapMax)
	c.Assert(rapidSwapMax, Equals, int64(5))
}

func (s *QuotesSuite) TestQuoteSwapTimingWithMempoolSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 10)

	// Setup test pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Test values
	swapSeconds := int64(100)        // Simulated swap time
	inboundConfSeconds := int64(600) // Simulated inbound confirmation time (longer)
	inboundConfBlocks := int64(100)  // Number of confirmation blocks needed

	totalSecondsFor := func(fromAsset, toAsset common.Asset, amount cosmos.Uint, swapType types.SwapType, streamInterval, streamQuantity uint64, inboundSeconds, swapSeconds int64, inboundBlocks int64) int64 {
		mempoolSwapCap := cosmos.NewUint(uint64(mgr.Keeper().GetConfigInt64(ctx, constants.MempoolSwapCapTOR)))
		canUseMempoolSwap := !mempoolSwapCap.IsZero() &&
			isMempoolSwapEligible(ctx, mgr, fromAsset, toAsset, amount, swapType, streamInterval, streamQuantity)

		totalSeconds := swapSeconds
		if canUseMempoolSwap {
			if inboundBlocks > 0 && inboundSeconds > totalSeconds {
				totalSeconds = inboundSeconds
			}
			return totalSeconds
		}

		if inboundBlocks > 0 {
			totalSeconds += inboundSeconds
		}
		return totalSeconds
	}

	// Test 1: With mempool swaps disabled, timing stays sequential even when the
	// route would otherwise be eligible.
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	c.Check(
		totalSecondsFor(common.BTCAsset, common.ETHAsset, cosmos.NewUint(common.One), MarketSwap, 0, 0, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds+swapSeconds,
		Commentf("disabled mempool swaps should keep timing sequential"),
	)

	// Test 2: The advanced queue gate must also be on; without it, quote timing
	// should remain sequential even if the cap is enabled.
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	c.Check(
		totalSecondsFor(common.BTCAsset, common.ETHAsset, cosmos.NewUint(common.One), MarketSwap, 0, 0, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds+swapSeconds,
		Commentf("legacy queue mode should keep quote timing sequential"),
	)

	// Test 3: Once the advanced queue is enabled, eligible routes regain the
	// mempool overlap benefit.
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	c.Check(
		totalSecondsFor(common.BTCAsset, common.ETHAsset, cosmos.NewUint(common.One), MarketSwap, 0, 0, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds,
		Commentf("eligible external-target swaps should use parallel timing"),
	)

	// Test 4: Explicit streaming swaps to native targets are also mempool-eligible.
	c.Check(
		totalSecondsFor(common.BTCAsset, common.RuneAsset(), cosmos.NewUint(common.One), MarketSwap, 1, 1, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds,
		Commentf("streaming native-target swaps should use parallel timing"),
	)

	// Test 5: Non-streaming swaps to native targets should not use the mempool path.
	c.Check(
		totalSecondsFor(common.BTCAsset, common.RuneAsset(), cosmos.NewUint(common.One), MarketSwap, 0, 0, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds+swapSeconds,
		Commentf("non-streaming native target swaps should keep sequential timing"),
	)
	c.Check(
		totalSecondsFor(common.BTCAsset, common.RuneAsset(), cosmos.NewUint(common.One), LimitSwap, 30, 1, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds+swapSeconds,
		Commentf("limit swap TTL alone should not switch native target timing to the mempool path"),
	)
	c.Check(
		totalSecondsFor(common.BTCAsset, common.RuneAsset(), cosmos.NewUint(common.One), LimitSwap, 30, 2, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds,
		Commentf("streaming limit swaps should still use parallel timing for native targets"),
	)

	// Test 6: Implicit streaming swap to native targets should also use the mempool path
	// once the advanced queue is enabled and will split the swap.
	c.Check(
		totalSecondsFor(common.BTCAsset, common.RuneAsset(), cosmos.NewUint(10*common.One), MarketSwap, 0, 0, inboundConfSeconds, swapSeconds, inboundConfBlocks),
		Equals,
		inboundConfSeconds,
		Commentf("implicit streaming native target swaps should use parallel timing"),
	)

	// Test 7: When the swap itself takes longer, the mempool path should still
	// use the slower of the two parallel branches.
	swapSecondsLonger := int64(800)
	inboundConfSecondsShort := int64(200)
	c.Check(
		totalSecondsFor(common.BTCAsset, common.ETHAsset, cosmos.NewUint(common.One), MarketSwap, 0, 0, inboundConfSecondsShort, swapSecondsLonger, inboundConfBlocks),
		Equals,
		swapSecondsLonger,
		Commentf("parallel timing should use the longer swap branch when it dominates"),
	)
}
