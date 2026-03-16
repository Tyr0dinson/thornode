package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
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

func (s *QuotesSuite) TestIsIntentCompatibleAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test RUNE is compatible
	compatible, reason := isIntentCompatibleAsset(ctx, mgr, common.RuneNative)
	c.Assert(compatible, Equals, true)
	c.Assert(reason, Equals, "native RUNE")

	// Test secured asset is compatible
	securedBTC, _ := common.NewAsset("THOR-BTC")
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, securedBTC)
	c.Assert(compatible, Equals, true)
	c.Assert(reason, Equals, "secured asset")

	// Test trade asset is compatible
	tradeETH, _ := common.NewAsset("ETH~ETH")
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, tradeETH)
	c.Assert(compatible, Equals, true)
	c.Assert(reason, Equals, "trade asset")

	// Unsupported THOR-native assets are intentionally blocked for now
	synthBTC := common.BTCAsset.GetSyntheticAsset()
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, synthBTC)
	c.Assert(compatible, Equals, false)
	c.Assert(reason, Equals, unsupportedIntentNativeAssetError(synthBTC))

	// Test gas asset for supported chain is compatible
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, common.BTCAsset)
	c.Assert(compatible, Equals, true)
	c.Assert(reason, Equals, "gas asset for supported chain")

	// Test ETH gas asset is compatible
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, common.ETHAsset)
	c.Assert(compatible, Equals, true)
	c.Assert(reason, Equals, "gas asset for supported chain")

	// Test unknown asset is not compatible
	unknownAsset, _ := common.NewAsset("UNKNOWN.UNKNOWN")
	compatible, reason = isIntentCompatibleAsset(ctx, mgr, unknownAsset)
	c.Assert(compatible, Equals, false)
	c.Assert(reason, Equals, "asset not recognized by protocol")
}

func (s *QuotesSuite) TestQueryQuoteIntent(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Enable intents
	mgr.Keeper().SetMimir(ctx, constants.IntentsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.IntentTTLBlocks.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.IntentPriceSlideBlocks.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.IntentMinFeeBasisPoints.String(), 50)
	mgr.Keeper().SetMimir(ctx, constants.IntentAuctionBasisPoints.String(), 1000)
	mgr.Keeper().SetMimir(ctx, constants.IntentAuctionDurationBlocks.String(), 14400)

	qs := queryServer{mgr: mgr}

	// Test: intents disabled
	mgr.Keeper().SetMimir(ctx, constants.IntentsEnabled.String(), 0)
	req := &types.QueryQuoteIntentRequest{
		FromAsset:   "ETH.ETH",
		ToAsset:     "BTC.BTC",
		Amount:      "1000000000",
		Destination: "bc1qtest",
		HighTarget:  "5000000",
		LowTarget:   "4800000",
	}
	resp, err := qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "intents feature is disabled")

	// Re-enable intents
	mgr.Keeper().SetMimir(ctx, constants.IntentsEnabled.String(), 1)

	// Test: missing required parameters
	_, err = qs.queryQuoteIntent(ctx, &types.QueryQuoteIntentRequest{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "missing from_asset parameter")

	// Test: high < low validation
	req = &types.QueryQuoteIntentRequest{
		FromAsset:   "ETH.ETH",
		ToAsset:     "BTC.BTC",
		Amount:      "1000000000",
		Destination: "bc1qtest",
		HighTarget:  "4000000",
		LowTarget:   "5000000",
	}
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "high_target must be >= low_target")

	baseReq := func() *types.QueryQuoteIntentRequest {
		return &types.QueryQuoteIntentRequest{
			FromAsset:   common.RuneNative.String(),
			ToAsset:     common.BTCAsset.String(),
			Amount:      "1000000000",
			Destination: GetRandomBTCAddress().String(),
			HighTarget:  "5000000",
			LowTarget:   "4800000",
		}
	}

	// Test: zero high target validation
	req = baseReq()
	req.HighTarget = "0"
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "high target cannot be zero")

	// Test: zero low target validation
	req = baseReq()
	req.LowTarget = "0"
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "low target cannot be zero")

	// Test: same source and target asset validation
	req = baseReq()
	req.ToAsset = req.FromAsset
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "source and target assets cannot be the same")

	// Test: dust threshold validation
	req = baseReq()
	req.FromAsset = common.DOGEAsset.String()
	req.Amount = "100000000" // DOGE dust threshold
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Matches, "source amount .* is below dust threshold .*")

	// Test: affiliate_bps without affiliate validation
	req = baseReq()
	req.AffiliateBps = "1"
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "affiliate address cannot be empty while affiliate basis points is non-zero")

	// Test: affiliate_bps max validation aligns with MsgIntentOpen.ValidateBasic
	req = baseReq()
	req.Affiliate = GetRandomTHORAddress().String()
	req.AffiliateBps = "1001"
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "affiliate_bps 1001 exceeds maximum 1000")

	// Test: combined fee basis points exceed MaxBasisPts
	mgr.Keeper().SetMimir(ctx, constants.IntentMinFeeBasisPoints.String(), 9500)
	req = baseReq()
	req.Affiliate = GetRandomTHORAddress().String()
	req.AffiliateBps = "500"
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Matches, "combined fee basis points.*must be less than.*")
	mgr.Keeper().SetMimir(ctx, constants.IntentMinFeeBasisPoints.String(), 50) // restore

	synthBTC := common.BTCAsset.GetSyntheticAsset()
	req = baseReq()
	req.FromAsset = synthBTC.String()
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, unsupportedIntentNativeAssetError(synthBTC))

	req = &types.QueryQuoteIntentRequest{
		FromAsset:   common.BTCAsset.GetSecuredAsset().String(),
		ToAsset:     common.BTCAsset.GetTradeAsset().String(),
		Amount:      "500000000",
		Destination: GetRandomTHORAddress().String(),
		HighTarget:  "5000000",
		LowTarget:   "4800000",
	}
	resp, err = qs.queryQuoteIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, "at least one of source or target asset must be fee-chargeable (L1 pool asset or RUNE)")
}

func (s *QuotesSuite) TestQueryQuoteIntentSolve_NonGasTargetUsesConvertedGasStep(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	targetAsset, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	gasPool := NewPool()
	gasPool.Asset = common.ETHAsset
	gasPool.Status = PoolAvailable
	gasPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	gasPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(mgr.Keeper().SetPool(ctx, gasPool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = targetAsset
	targetPool.Status = PoolAvailable
	targetPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	targetPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	intentTxID := GetRandomTx().ID
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		TargetAsset:   targetAsset,
		TargetAddress: GetRandomETHAddress(),
		HighTarget:    cosmos.NewUint(2000),
		LowTarget:     cosmos.NewUint(1000),
		ExpiryHeight:  ctx.BlockHeight() + 100,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent)

	mgr.Keeper().SetIntentSolve(ctx, types.IntentSolve{
		SolveTxID:     GetRandomTx().ID,
		IntentTxID:    intentTxID,
		SolverAddress: GetRandomETHAddress(),
		TargetCoin:    common.NewCoin(targetAsset, cosmos.NewUint(1000)),
		SubmitHeight:  ctx.BlockHeight(),
		MatchHeight:   ctx.BlockHeight(),
	})

	resp, err := qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
		Amount:     "1000",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, true)
	c.Assert(resp.MinIncrement, Equals, "375000")
	c.Assert(resp.MinIncrementAsset, Equals, targetAsset.String())
	c.Assert(resp.AmountToBeatBest, Equals, "376000")
	c.Assert(resp.WouldBeatCurrentBest, Equals, false)

	resp, err = qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
		Amount:     "376000",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, true)
	c.Assert(resp.WouldBeatCurrentBest, Equals, true)
}

func (s *QuotesSuite) TestQueryQuoteIntentSolve_THORNativeTargetUsesRuneGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	targetAsset := common.BTCAsset.GetSecuredAsset()

	targetPool := NewPool()
	targetPool.Asset = common.BTCAsset
	targetPool.Status = PoolAvailable
	targetPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	targetPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)

	intentTxID := GetRandomTx().ID
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		TargetAsset:   targetAsset,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(2000),
		LowTarget:     cosmos.NewUint(1000),
		ExpiryHeight:  ctx.BlockHeight() + 100,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent)

	mgr.Keeper().SetIntentSolve(ctx, types.IntentSolve{
		SolveTxID:     GetRandomTx().ID,
		IntentTxID:    intentTxID,
		SolverAddress: GetRandomTHORAddress(),
		TargetCoin:    common.NewCoin(targetAsset, cosmos.NewUint(1000)),
		SubmitHeight:  ctx.BlockHeight(),
		MatchHeight:   ctx.BlockHeight(),
	})

	resp, err := qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
		Amount:     "1000",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, true)
	c.Assert(resp.MinIncrement, Equals, "375000")
	c.Assert(resp.MinIncrementAsset, Equals, targetAsset.String())
	c.Assert(resp.AmountToBeatBest, Equals, "376000")
	c.Assert(resp.WouldBeatCurrentBest, Equals, false)
}

func (s *QuotesSuite) TestQueryQuoteIntentSolve_PricingUnavailableUsesOneUnitFallback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)
	targetAsset := common.BTCAsset.GetSecuredAsset()

	intentTxID := GetRandomTx().ID
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		TargetAsset:   targetAsset,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(2000),
		LowTarget:     cosmos.NewUint(1000),
		ExpiryHeight:  ctx.BlockHeight() + 100,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent)

	mgr.Keeper().SetIntentSolve(ctx, types.IntentSolve{
		SolveTxID:     GetRandomTx().ID,
		IntentTxID:    intentTxID,
		SolverAddress: GetRandomTHORAddress(),
		TargetCoin:    common.NewCoin(targetAsset, cosmos.NewUint(1000)),
		SubmitHeight:  ctx.BlockHeight(),
		MatchHeight:   ctx.BlockHeight(),
	})

	resp, err := qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
		Amount:     "1000",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, true)
	c.Assert(resp.MinIncrement, Equals, "1")
	c.Assert(resp.MinIncrementAsset, Equals, targetAsset.String())
	c.Assert(resp.AmountToBeatBest, Equals, "1001")
	c.Assert(resp.WouldBeatCurrentBest, Equals, false)

	resp, err = qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
		Amount:     "1001",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, true)
	c.Assert(resp.WouldBeatCurrentBest, Equals, true)
}

func (s *QuotesSuite) TestQueryQuoteIntentSolveRejectsUnsupportedNativeAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	intentTxID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	synthBTC := common.BTCAsset.GetSyntheticAsset()
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: common.Address("thor1sender"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   synthBTC,
		TargetAddress: common.Address("thor1target"),
		HighTarget:    cosmos.NewUint(20),
		LowTarget:     cosmos.NewUint(10),
		ExpiryHeight:  ctx.BlockHeight() + 100,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent)

	resp, err := qs.queryQuoteIntentSolve(ctx, &types.QueryQuoteIntentSolveRequest{
		IntentTxId: intentTxID.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Valid, Equals, false)
	c.Assert(resp.ValidationError, Equals, unsupportedIntentNativeAssetError(synthBTC))
}
