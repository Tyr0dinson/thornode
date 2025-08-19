package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SwapAffiliateSuite struct{}

var _ = Suite(&SwapAffiliateSuite{})

// TestSwap_WithAffiliateFee_SecuredAssetInput_EmitsAffiliateOutTx reproduces
// the affiliate fee bug described in https://gitlab.com/thorchain/thornode/-/issues/2238
// where swaps from secured assets (like ETH.WBTC) fail to generate affiliate
// fees due to incorrect fromAddress handling in affiliate swap creation.
//
// Real-world examples that should work but currently fail:
//   - TXID: 026868B6A15976354D9DD2331E44E99AB63B0937F00C19A91EB6352EC278E2F7
//     ETH.WBTC → BTC.BTC, ~1.573 wBTC.ETH in, expected affiliate fee ≈ $890
//   - TXID: 8F09652F14E97A803B529A8D369042B574611A2F288299AEBB841D62181C3673
//     ETH.ETH → BTC.BTC, 15 ETH in, expected affiliate fee ≈ $250
//
// The bug manifests as:
// - Error: "fail to parse from address: 2 errors occurred: * internal error * decoding bech32 failed: invalid separator index 41"
// - Root cause: affiliate swap uses ETH fromAddress instead of THOR fromAddress
// - Result: only user outbound tx created, no affiliate outbound tx or events
func (s *SwapAffiliateSuite) TestSwap_WithAffiliateFee_SecuredAssetInput_EmitsAffiliateOutTx(c *C) {
	// Override the bech32 config to use mainnet prefix
	config := cosmos.GetConfig()
	config.SetBech32PrefixForAccount("thor", "thorpub")
	/*
		defer func() {                                                                                                                                                                                 // Reset to default test config
				config.SetBech32PrefixForAccount("tthor", "tthorpub")
		}()
	*/
	ctx, mgr := setupManagerForTest(c)

	// Create WBTC secured asset (ETH.WBTC)
	wbtcAsset, err := common.NewAsset("ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599")
	c.Assert(err, IsNil)

	// Set up realistic pools for proper swap calculations
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One) // 1000 RUNE
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One) // 100 ETH
	ethPool.LPUnits = ethPool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create WBTC pool for secured asset swaps
	wbtcPool := NewPool()
	wbtcPool.Asset = wbtcAsset
	wbtcPool.BalanceRune = cosmos.NewUint(1500 * common.One) // 1500 RUNE
	wbtcPool.BalanceAsset = cosmos.NewUint(5 * common.One)   // 5 WBTC (higher value asset)
	wbtcPool.LPUnits = wbtcPool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, wbtcPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One) // 2000 RUNE
	btcPool.BalanceAsset = cosmos.NewUint(50 * common.One)  // 50 BTC
	btcPool.LPUnits = btcPool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set up affiliate THORName with the real "rj" data from issue #2238
	// Keep the current THOR address but update with real thorname data
	affAddr := GetRandomTHORAddress() // Keep your current random THOR address
	affAccAddr, err := affAddr.AccAddress()
	c.Assert(err, IsNil)

	// Create real "rj" THORName structure from issue #2238
	ownerAddr, err := common.NewAddress("thor1hjc3utg884sdpmquz0a87k3gschen204nu6twu")
	c.Assert(err, IsNil)
	ownerAccAddr, err := ownerAddr.AccAddress()
	c.Assert(err, IsNil)

	// Real thor address from the actual "rj" thorname
	realRjAddr, err := common.NewAddress("thor1mcy9jtp4kzl8q2lvdgfgsl8jvqrf504uphkf0pz2p9wud8tsntesjvccew")
	c.Assert(err, IsNil)

	ethAlias, err := common.NewAddress("0x6601432b0bF4541D413CAF5e84040E707583Dccd")
	c.Assert(err, IsNil)

	rujiAsset, err := common.NewAsset("THOR.RUJI")
	c.Assert(err, IsNil)

	thorname := THORName{
		Name:              "rj",
		Owner:             ownerAccAddr,
		ExpireBlockHeight: 47953202,
		PreferredAsset:    rujiAsset,
		Aliases: []THORNameAlias{
			{Chain: common.THORChain, Address: realRjAddr},
			{Chain: common.ETHChain, Address: ethAlias},
			{Chain: common.BASEChain, Address: ethAlias},
		},
	}
	mgr.Keeper().SetTHORName(ctx, thorname)

	// Set up the affiliate collector with the real RUNE amount from issue #2238
	affiliateCollector := AffiliateFeeCollector{
		RuneAmount: cosmos.NewUint(623935813), // affiliate_collector_rune from real data
	}
	mgr.Keeper().SetAffiliateCollector(ctx, affiliateCollector)

	// Test cases based on real transactions from issue #2238
	testCases := []struct {
		name        string
		inputAsset  common.Asset
		inputAmount cosmos.Uint
		expectedFee cosmos.Uint // 50 bps of input amount
		description string
	}{
		{
			name:        "ETH.WBTC to BTC with affiliate fee",
			inputAsset:  wbtcAsset,
			inputAmount: cosmos.NewUint(157308300), // 1.573083 WBTC (8 decimals)
			expectedFee: cosmos.NewUint(786541),    // 50 bps = 0.00786541 WBTC
			description: "Reproduces TXID 026868B6A15976354D9DD2331E44E99AB63B0937F00C19A91EB6352EC278E2F7",
		},
		{
			name:        "ETH.ETH to BTC with affiliate fee",
			inputAsset:  common.ETHAsset,
			inputAmount: cosmos.NewUint(15 * common.One), // 15 ETH
			expectedFee: cosmos.NewUint(750000000),       // 50 bps = 0.075 ETH
			description: "Reproduces TXID 8F09652F14E97A803B529A8D369042B574611A2F288299AEBB841D62181C3673",
		},
	}

	for _, tc := range testCases {
		c.Logf("Running test case: %s - %s", tc.name, tc.description)

		// Create inbound transaction from ETH chain with affiliate memo
		// This reproduces the bug: ETH fromAddress will be copied to affiliate swap
		ethFromAddr := GetRandomETHAddress()
		btcDestAddr := GetRandomBTCAddress()

		// Memo format: =:BTC.BTC:destination::affiliate:bps
		// This should create affiliate fee for "rj" at 50 basis points
		memo := fmt.Sprintf("=:BTC.BTC:%s::rj:50", btcDestAddr)

		tx := common.NewTx(
			GetRandomTxHash(),
			ethFromAddr, // ETH address - this causes the bug per issue #2238
			GetRandomTHORAddress(),
			common.NewCoins(common.NewCoin(tc.inputAsset, tc.inputAmount)),
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(200000)),
			},
			memo,
		)
		tx.Chain = common.ETHChain

		// Track initial affiliate balance
		initialAffBalance := mgr.Keeper().GetBalance(ctx, affAccAddr)
		initialAffRuneBalance := initialAffBalance.AmountOf(common.RuneNative.Native())

		// Create a mock txOut store to track outbound transactions
		txOutStore := NewTxStoreDummy()
		mgr.txOutStore = txOutStore

		// Execute the swap through the full handler to trigger affiliate processing
		handler := NewSwapHandler(mgr)

		// Create the swap message that will trigger affiliate fee processing
		swapMsg := NewMsgSwap(
			tx,
			common.BTCAsset,       // target asset
			btcDestAddr,           // destination
			cosmos.ZeroUint(),     // trade target
			affAddr,               // affiliate address
			cosmos.NewUint(50),    // affiliate bps
			"",                    // aggregator
			"",                    // aggregator target address
			nil,                   // aggregator target limit (pointer)
			types.SwapType_market, // swap type
			0,                     // streaming interval
			0,                     // streaming quantity
			types.SwapVersion_v1,  // version
			GetRandomBech32Addr(), // signer
		)

		// NOTE: This is where the bug occurs - the affiliate processing path
		// should trigger the bech32 parsing error due to ETH fromAddress
		// being used in affiliate swap creation instead of THOR fromAddress
		result, emitAmount, swapErr := handler.RunWithEmit(ctx, swapMsg)
		c.Assert(swapErr, NotNil, Commentf("expected this to fail, we have NOT reproduced the bug yet"))

		// The main swap should succeed, but affiliate processing should cause issues
		// We expect this to either fail with the bech32 error, or succeed but not distribute affiliate fees
		// Check if we got the expected bech32 parsing error
		expectedError := "fail to parse from address"
		if !strings.Contains(swapErr.Error(), expectedError) {
			c.Fatalf("Expected bech32 parsing error, got: %s", swapErr.Error())
		}
		c.Logf("✓ Got expected parsing error: %s", swapErr.Error())

		c.Assert(result, NotNil)
		c.Assert(emitAmount.IsZero(), Equals, false)

		// BUG REPRODUCTION: These assertions should fail due to issue #2238

		// 1. Check that affiliate fee was calculated and distributed
		finalAffBalance := mgr.Keeper().GetBalance(ctx, affAccAddr)
		finalAffRuneBalance := finalAffBalance.AmountOf(common.RuneNative.Native())
		affiliateFeeReceived := finalAffRuneBalance.Sub(initialAffRuneBalance)

		// This should pass but currently fails - affiliate receives no fee
		c.Assert(affiliateFeeReceived.IsZero(), Equals, false,
			Commentf("Expected affiliate to receive fee, got %s (issue #2238)", affiliateFeeReceived.String()))

		// 2. Check that TWO outbound transactions were created (user + affiliate)
		// Currently only the user outbound is created due to affiliate parsing failure
		outboundItems, err := txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		outboundCount := len(outboundItems)
		c.Assert(outboundCount, Equals, 2,
			Commentf("Expected 2 outbound txs (user + affiliate), got %d (issue #2238)", outboundCount))

		// 3. Verify one of the outbounds is to the affiliate address
		foundAffiliateOutbound := false
		for _, item := range outboundItems {
			if item.ToAddress.Equals(affAddr) {
				foundAffiliateOutbound = true
				// Verify it's a RUNE outbound (affiliate fees converted to RUNE)
				c.Assert(item.Coin.Asset.IsRune(), Equals, true)
				break
			}
		}
		c.Assert(foundAffiliateOutbound, Equals, true,
			Commentf("No affiliate outbound transaction found (issue #2238)"))

		// 4. Check for EventAffiliateFee emission
		// Note: Since DummyEventMgr doesn't store events, we can't test event emission directly
		// The real test is whether the affiliate actually received RUNE and outbound txs were created
		// This is sufficient to demonstrate the bug in issue #2238

		c.Logf("✓ Test case passed: %s", tc.name)
	}
}
