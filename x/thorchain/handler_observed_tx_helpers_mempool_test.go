package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxHelpersMempoolSuite struct{}

var _ = Suite(&HandlerObservedTxHelpersMempoolSuite{})

func runSwapQueues(c *C, ctx cosmos.Context, mgr *Mgrs) {
	c.Assert(mgr.SwapQ().EndBlock(ctx, mgr), IsNil)
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	}
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutbounds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// Setup vault
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create inbound transaction
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{
		ObservedTx{
			Tx: common.Tx{
				ID:    inTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			Signers:        []string{},
			FinaliseHeight: 0, // Not finalized yet
		},
	})
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = 0

	// Create TxOutItem (staged for mempool swap)
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}

	// Add to voter actions
	voter.Actions = []TxOutItem{txOutItem}

	// Stage the TxOutItem (instead of adding directly to TxOut)
	outboundHeight := ctx.BlockHeight() + 10
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, txOutItem, outboundHeight), IsNil)

	// Save voter
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Test 1: Successfully finalize outbounds
	err := finalizeOutboundsAtomic(ctx, mgr, voter)
	c.Assert(err, IsNil, Commentf("finalizeOutbounds failed: %v", err))

	// Verify staged outbound was moved to real TxOut
	updatedTxOut, err := mgr.Keeper().GetTxOut(ctx, outboundHeight)
	c.Assert(err, IsNil)
	c.Assert(updatedTxOut.TxArray, HasLen, 1, Commentf("Staged outbound should be moved to TxOut"))

	// Verify staged outbounds are deleted
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(len(stagedItems), Equals, 0, Commentf("Staged outbounds should be deleted after finalization"))

	// Test 2: Multiple actions for single inbound
	voter2 := NewObservedTxVoter(GetRandomTxHash(), ObservedTxs{})
	voter2.UnfinalizedHeight = ctx.BlockHeight()
	voter2.Actions = []TxOutItem{
		{Chain: common.BTCChain, InHash: voter2.TxID, ToAddress: GetRandomBTCAddress(), VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		{Chain: common.BTCChain, InHash: voter2.TxID, ToAddress: GetRandomBTCAddress(), VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One))},
	}

	// Stage both items
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter2.Actions[0], ctx.BlockHeight()+20), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter2.Actions[1], ctx.BlockHeight()+20), IsNil)

	err = finalizeOutboundsAtomic(ctx, mgr, voter2)
	c.Assert(err, IsNil)

	// Verify both staged outbounds were moved to TxOut
	updatedTxOut2, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight()+20)
	c.Assert(err, IsNil)
	c.Assert(updatedTxOut2.TxArray, HasLen, 2, Commentf("Both staged outbounds should be moved to TxOut"))

	// Test 3: Skip when no staged outbounds (idempotency)
	voter3 := NewObservedTxVoter(GetRandomTxHash(), ObservedTxs{})
	voter3.Actions = []TxOutItem{
		{Chain: common.BTCChain, InHash: voter3.TxID, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
	}
	// Don't stage anything - simulates already finalized

	err = finalizeOutboundsAtomic(ctx, mgr, voter3)
	c.Assert(err, IsNil, Commentf("Should succeed when no staged outbounds exist"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsRollbackOnError(c *C) {
	ctx, mgr := setupManagerForTest(c)
	dummyMgr := NewDummyMgrWithKeeper(mgr.Keeper())

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.MempoolSwapTOR = cosmos.NewUint(123 * common.One)

	stagedItem := TxOutItem{
		Chain:  common.THORChain,
		InHash: inTxID,
		Coin:   common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
	}

	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedItem, ctx.BlockHeight()+5), IsNil)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(999*common.One))

	err := finalizeOutboundsAtomic(ctx, dummyMgr, voter)
	c.Assert(err, NotNil)

	usage := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)
	c.Check(usage.Equal(cosmos.NewUint(999*common.One)), Equals, true, Commentf("mempool usage should roll back on finalize failure"))

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 1, Commentf("staged outbounds should remain after finalize failure"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToNativeNonRuneTargets(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// Set up the shared chain state used by all native non-RUNE target cases.
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.LPUnits = pool.BalanceRune
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}

	affiliateBps := cosmos.NewUint(500)
	outboundAmount := cosmos.NewUint(1000 * common.One)
	affiliateAmount := cosmos.NewUint(50 * common.One)
	netSynthAmount := outboundAmount.Sub(affiliateAmount)

	// Reuse the same finalization flow for synth, trade, and secured targets.
	runCase := func(name string, finalAsset, stagedAsset common.Asset, moduleName string, checkBalance func(cosmos.AccAddress)) {
		ownerAddr := GetRandomTHORAddress()
		ownerAcc, err := ownerAddr.AccAddress()
		c.Assert(err, IsNil, Commentf("case %s", name))
		affiliateAddr := GetRandomTHORAddress()
		inTxID := GetRandomTxHash()
		// Build one finalized inbound with an affiliate fee in the memo.
		inboundMemo := NewSwapMemo(ctx, mgr, finalAsset, ownerAddr, cosmos.ZeroUint(), affiliateAddr.String(), affiliateBps)
		observedTx := ObservedTx{
			Tx: common.Tx{
				ID:          inTxID,
				Chain:       common.BTCChain,
				FromAddress: GetRandomBTCAddress(),
				ToAddress:   GetRandomBTCAddress(),
				Coins: common.Coins{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
				},
				Memo: inboundMemo,
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			FinaliseHeight: ctx.BlockHeight(),
			Signers:        []string{na.NodeAddress.String()},
		}
		voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
		voter.Tx = observedTx
		voter.FinalisedHeight = ctx.BlockHeight()

		finalItem := TxOutItem{
			Chain:      common.THORChain,
			InHash:     inTxID,
			ToAddress:  ownerAddr,
			Coin:       common.NewCoin(finalAsset, outboundAmount),
			Memo:       NewOutboundMemo(inTxID).String(),
			ModuleName: moduleName,
		}
		voter.Actions = []TxOutItem{finalItem}
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)

		// Stage the outbound first. The affiliate side effect must wait until finalization.
		stagedItem := finalItem
		stagedItem.Coin.Asset = stagedAsset
		c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedItem, ctx.BlockHeight()+5), IsNil, Commentf("case %s", name))

		_, err = mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
		c.Assert(err, NotNil, Commentf("case %s should not queue affiliate sidecars before finalization", name))

		// Finalization should apply the deferred affiliate work exactly once.
		err = finalizeOutboundsAtomic(ctx, mgr, voter)
		c.Assert(err, IsNil, Commentf("case %s", name))

		stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
		c.Assert(stagedErr, IsNil, Commentf("case %s", name))
		c.Check(stagedItems, HasLen, 0, Commentf("case %s should clear staged outbounds on finalization", name))

		affiliateSwap, err := mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
		c.Assert(err, IsNil, Commentf("case %s should queue affiliate sidecar on finalization", name))
		c.Check(affiliateSwap.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(finalAsset, affiliateAmount)}, Commentf("case %s should queue the deferred affiliate input", name))
		c.Check(affiliateSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true, Commentf("case %s should swap the affiliate fee to RUNE", name))

		// Each asset type should still leave the user's final balance in the expected state.
		checkBalance(ownerAcc)
	}

	runCase("synth target", common.ETHAsset.GetSyntheticAsset(), common.ETHAsset.GetSyntheticAsset(), ModuleName, func(ownerAcc cosmos.AccAddress) {
		balance := mgr.Keeper().GetBalance(ctx, ownerAcc)
		c.Check(balance.AmountOf(common.ETHAsset.GetSyntheticAsset().Native()).Equal(cosmos.NewIntFromBigInt(netSynthAmount.BigInt())), Equals, true, Commentf("synth target should finalize net of affiliate fee"))
	})

	runCase("trade target", common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetTradeAsset(), "", func(ownerAcc cosmos.AccAddress) {
		balance := mgr.TradeAccountManager().BalanceOf(ctx, common.BTCAsset.GetTradeAsset(), ownerAcc)
		c.Check(balance.Equal(outboundAmount), Equals, true, Commentf("trade target should keep the full user balance on finalization"))
	})

	runCase("secured target", common.ETHAsset.GetSecuredAsset(), common.ETHAsset, "", func(ownerAcc cosmos.AccAddress) {
		balance := mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, ownerAcc)
		c.Check(balance.Equal(outboundAmount), Equals, true, Commentf("secured target should keep the full user balance on finalization"))
	})
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToRuneTarget(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// This case checks the direct RUNE payout path.
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	ownerAddr := GetRandomTHORAddress()
	ownerAcc, err := ownerAddr.AccAddress()
	c.Assert(err, IsNil)
	affiliateAddr := GetRandomTHORAddress()
	affiliateAcc, err := affiliateAddr.AccAddress()
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	firstAmount := cosmos.NewUint(600 * common.One)
	secondAmount := cosmos.NewUint(400 * common.One)
	outboundAmount := firstAmount.Add(secondAmount)
	affiliateAmount := cosmos.NewUint(50 * common.One)
	netAmount := outboundAmount.Sub(affiliateAmount)
	inboundMemo := NewSwapMemo(ctx, mgr, common.RuneAsset(), ownerAddr, cosmos.ZeroUint(), affiliateAddr.String(), cosmos.NewUint(500))
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:          inTxID,
			Chain:       common.BTCChain,
			FromAddress: GetRandomBTCAddress(),
			ToAddress:   GetRandomBTCAddress(),
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			},
			Memo: inboundMemo,
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Actions = []TxOutItem{{
		Chain:     common.THORChain,
		InHash:    inTxID,
		ToAddress: ownerAddr,
		Coin:      common.NewCoin(common.RuneAsset(), outboundAmount),
		Memo:      NewOutboundMemo(inTxID).String(),
	}}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	// Stage one combined RUNE payout and finalize it.
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], ctx.BlockHeight()+5), IsNil)

	err = finalizeOutboundsAtomic(ctx, mgr, voter)
	c.Assert(err, IsNil)

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 0)

	_, queueErr := mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
	c.Assert(queueErr, NotNil, Commentf("RUNE target should not queue an affiliate sidecar on finalization"))

	// The owner should get net RUNE and the affiliate should get the deferred fee.
	ownerBalance := mgr.Keeper().GetBalance(ctx, ownerAcc)
	affiliateBalance := mgr.Keeper().GetBalance(ctx, affiliateAcc)
	c.Check(ownerBalance.AmountOf(common.RuneNative.Native()).Equal(cosmos.NewIntFromBigInt(netAmount.BigInt())), Equals, true,
		Commentf("RUNE target should finalize net of affiliate fee"))
	c.Check(affiliateBalance.AmountOf(common.RuneNative.Native()).Equal(cosmos.NewIntFromBigInt(affiliateAmount.BigInt())), Equals, true,
		Commentf("RUNE target should transfer the deferred affiliate fee on finalization"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToExternalTarget(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// This case checks split external outbounds that later go into TxOut.
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.LPUnits = pool.BalanceRune
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}

	ownerAddr := GetRandomETHAddress()
	affiliateAddr := GetRandomTHORAddress()
	inTxID := GetRandomTxHash()
	firstAmount := cosmos.NewUint(600 * common.One)
	secondAmount := cosmos.NewUint(400 * common.One)
	outboundAmount := firstAmount.Add(secondAmount)
	affiliateAmount := cosmos.NewUint(50 * common.One)
	netAmount := outboundAmount.Sub(affiliateAmount)
	inboundMemo := NewSwapMemo(ctx, mgr, common.ETHAsset, ownerAddr, cosmos.ZeroUint(), affiliateAddr.String(), cosmos.NewUint(500))
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:          inTxID,
			Chain:       common.BTCChain,
			FromAddress: GetRandomBTCAddress(),
			ToAddress:   GetRandomBTCAddress(),
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			},
			Memo: inboundMemo,
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Actions = []TxOutItem{
		{
			Chain:       common.ETHChain,
			InHash:      inTxID,
			VaultPubKey: activeVault.PubKey,
			ToAddress:   ownerAddr,
			Coin:        common.NewCoin(common.ETHAsset, firstAmount),
			Memo:        NewOutboundMemo(inTxID).String(),
		},
		{
			Chain:       common.ETHChain,
			InHash:      inTxID,
			VaultPubKey: activeVault.PubKey,
			ToAddress:   ownerAddr,
			Coin:        common.NewCoin(common.ETHAsset, secondAmount),
			Memo:        NewOutboundMemo(inTxID).String(),
		},
	}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Stage both pieces first, then finalize them together.
	scheduledHeight := ctx.BlockHeight() + 5
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], scheduledHeight), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[1], scheduledHeight), IsNil)

	err := finalizeOutboundsAtomic(ctx, mgr, voter)
	c.Assert(err, IsNil)

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 0)

	txOut, err := mgr.Keeper().GetTxOut(ctx, scheduledHeight)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 2)
	// The two queued pieces together should equal the net amount after one fee.
	totalQueued := cosmos.ZeroUint()
	for _, item := range txOut.TxArray {
		totalQueued = totalQueued.Add(item.Coin.Amount)
	}
	c.Check(totalQueued.Equal(netAmount), Equals, true,
		Commentf("split external targets should enter TxOut net of one deferred affiliate fee"))

	affiliateSwap, err := mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, IsNil)
	c.Check(affiliateSwap.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(common.ETHAsset, affiliateAmount)},
		Commentf("external target should queue the deferred affiliate sidecar on finalization"))
	c.Check(affiliateSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true)
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.TxOutDelayMax.String(), 17280) // Set max delay for searching

	// This test covers three rollback cases: normal external output, no staged output, and native RUNE output.
	// Setup source pool (BTC)
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	// Setup target pool (ETH) - where the swap output went
	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	// Setup vault - must have both BTC and ETH for the test
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Create inbound transaction (BTC)
	inTxID := GetRandomTxHash()
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:    inTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{},
		FinaliseHeight: 0,
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx // Set the main Tx field as well
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = 0

	// Create outbound TxOutItem (swapped to ETH)
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
		Memo:        "OUT:" + inTxID.String(),
		// OutHash defaults to zero value (empty)
	}

	voter.Actions = []TxOutItem{txOutItem}

	// Stage the TxOutItem (instead of adding directly to TxOut)
	outboundHeight := ctx.BlockHeight() + 10
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, txOutItem, outboundHeight), IsNil)

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	// Build the reverse swap we expect the rollback code to use.
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, voter, common.BTCAsset, txOutItem, 0, vault)
	c.Assert(err, IsNil)

	// Test 1: Successfully roll back a mempool swap
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil, Commentf("rollbackMempoolSwap failed: %v", err))

	// Verify rollback cleared speculative voter state
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("rollback should normalize the voter"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("rollback should clear the reserved mempool TOR value"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("rollback should discard speculative actions"))

	// Verify staged outbounds are deleted
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(len(stagedItems), Equals, 0, Commentf("staged outbounds should be deleted after rollback"))

	destAddress, err := vault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(expectedReverse.Tx.Memo, Equals, fmt.Sprintf("=:%s:%s", common.BTCAsset, destAddress))
	c.Check(expectedReverse.PostSwapMemo, Equals, fmt.Sprintf("DONATE:%s", common.BTCAsset.GetLayer1Asset()))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("inline rollback should not leave a reverse swap queued"))

	updatedSourcePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(updatedSourcePool.BalanceAsset.Equal(sourcePool.BalanceAsset), Equals, false,
		Commentf("inline rollback should change the source pool immediately"))

	// Test 2: Skip already signed outbounds (voter with no staged outbounds should still succeed)
	voter2TxID := GetRandomTxHash()
	voter2ObservedTx := ObservedTx{
		Tx: common.Tx{
			ID:    voter2TxID,
			Chain: common.ETHChain,
			Coins: common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		},
	}
	voter2 := NewObservedTxVoter(voter2TxID, ObservedTxs{voter2ObservedTx})
	voter2.Tx = voter2ObservedTx // Set the main Tx field as well
	voter2.UnfinalizedHeight = ctx.BlockHeight()
	voter2.Actions = []TxOutItem{
		{
			Chain:   common.ETHChain,
			InHash:  voter2.TxID,
			Coin:    common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
			OutHash: GetRandomTxHash(), // Already signed
		},
	}

	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter2)
	c.Assert(err, IsNil, Commentf("Should succeed with no staged outbounds"))

	// Verify voter2 was normalized
	updatedVoter2, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter2TxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter2.Reverted, Equals, false, Commentf("rollback with no staged outbounds should leave voter.Reverted false"))
	c.Check(updatedVoter2.Actions, IsNil, Commentf("rollback with no staged outbounds should still clear speculative actions"))

	// Test 3: Handle RUNE source asset (native output)
	runeTxID := GetRandomTxHash()
	runeObservedTx := ObservedTx{
		Tx: common.Tx{
			ID:    runeTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		},
	}
	runeVoter := NewObservedTxVoter(runeTxID, ObservedTxs{runeObservedTx})
	runeVoter.Tx = runeObservedTx // Set the main Tx field as well
	runeVoter.UnfinalizedHeight = ctx.BlockHeight()

	// Create a native outbound (RUNE)
	nativeTxOutItem := TxOutItem{
		Chain:     common.THORChain,
		InHash:    runeTxID,
		ToAddress: GetRandomRUNEAddress(),
		Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
	}
	runeVoter.Actions = []TxOutItem{nativeTxOutItem}

	// Stage the native outbound
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeTxOutItem, ctx.BlockHeight()+15), IsNil)

	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, runeVoter)
	c.Assert(err, IsNil, Commentf("Should handle native (RUNE) output"))

	// The native case should also clear the temporary voter state.
	// Verify voter was normalized
	updatedRuneVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, runeTxID)
	c.Assert(err, IsNil)
	c.Check(updatedRuneVoter.Reverted, Equals, false, Commentf("RUNE rollback should normalize the voter"))
	c.Check(updatedRuneVoter.Actions, IsNil, Commentf("RUNE rollback should clear speculative actions"))

	// Test 4: Non-RUNE native output (synth) should use reverse swap, not direct BalanceRune adjustment
	synthTxID := GetRandomTxHash()
	synthAsset, err := common.NewAsset("ETH/ETH")
	c.Assert(err, IsNil)
	synthObservedTx := ObservedTx{
		Tx: common.Tx{
			ID:    synthTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		},
	}
	synthVoter := NewObservedTxVoter(synthTxID, ObservedTxs{synthObservedTx})
	synthVoter.Tx = synthObservedTx
	synthVoter.UnfinalizedHeight = ctx.BlockHeight()

	// Create a synth outbound on THORChain (not RUNE)
	synthTxOutItem := TxOutItem{
		Chain:     common.THORChain,
		InHash:    synthTxID,
		ToAddress: GetRandomRUNEAddress(),
		Coin:      common.NewCoin(synthAsset, cosmos.NewUint(3*common.One)),
	}
	synthVoter.Actions = []TxOutItem{synthTxOutItem}

	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, synthTxOutItem, ctx.BlockHeight()+20), IsNil)
	mgr.Keeper().SetObservedTxInVoter(ctx, synthVoter)

	// Snapshot ETH pool BalanceRune immediately before the call under test
	ethPoolBefore, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)

	err = revertMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, synthVoter)
	c.Assert(err, IsNil, Commentf("Should handle synth output via reverse swap"))

	updatedSynthVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, synthTxID)
	c.Assert(err, IsNil)
	c.Check(updatedSynthVoter.Reverted, Equals, true, Commentf("Synth voter should be marked as reverted"))

	// Verify a reverse swap was queued (not a direct pool adjustment)
	synthReverseTxID := CreateMempoolRevertTxID(synthTxID, 0)
	synthReverseSwap, err := mgr.Keeper().GetSwapQueueItem(ctx, synthReverseTxID, 0)
	c.Assert(err, IsNil, Commentf("Synth output should queue a reverse swap"))
	c.Check(synthReverseSwap.Tx.Coins[0].Asset, DeepEquals, synthAsset,
		Commentf("Reverse swap input should be the synth asset"))

	// Verify the source pool's BalanceRune was NOT corrupted by adding synth amount
	ethPoolAfter, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(ethPoolAfter.BalanceRune.Equal(ethPoolBefore.BalanceRune), Equals, true,
		Commentf("ETH pool BalanceRune should not be modified by synth revert; got %s, want %s",
			ethPoolAfter.BalanceRune, ethPoolBefore.BalanceRune))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapRollbackOnError(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// Use a keeper that fails during the rollback flow so we can check atomic rollback.
	failKeeper := SettleSwapPostSwapSetPoolFailKeeper{Keeper: mgr.Keeper()}
	dummyMgr := NewDummyMgrWithKeeper(failKeeper)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	inTxID := GetRandomTxHash()
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:    inTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One))},
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: 0,
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(700*common.One))

	nativeItem := TxOutItem{
		Chain:     common.THORChain,
		InHash:    inTxID,
		ToAddress: GetRandomRUNEAddress(),
		Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
	}
	externalItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(3*common.One)),
	}

	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeItem, ctx.BlockHeight()+5), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, externalItem, ctx.BlockHeight()+5), IsNil)

	// The rollback should fail and leave all original state unchanged.
	err := rollbackMempoolSwapAtomic(ctx, failKeeper, dummyMgr, voter)
	c.Assert(err, NotNil)

	usage := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)
	c.Check(usage.Equal(cosmos.NewUint(700*common.One)), Equals, true, Commentf("mempool usage should remain unchanged on rollback failure"))

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 2, Commentf("staged outbounds should remain after rollback failure"))

	updatedVoter, voterErr := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(voterErr, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("voter.Reverted should stay false when rollback fails"))

	updatedPool, poolErr := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(poolErr, IsNil)
	c.Check(updatedPool.BalanceRune.Equal(sourcePool.BalanceRune), Equals, true, Commentf("pool rune balance should remain unchanged on rollback failure"))
	c.Check(updatedPool.BalanceAsset.Equal(sourcePool.BalanceAsset), Equals, true, Commentf("pool asset balance should remain unchanged on rollback failure"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirEnabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Enable mempool swaps
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// Create pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Verify swap memo is recognized as admissible before finalization
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), "=:ETH.ETH:"+GetRandomETHAddress().String()+":0/1/1")
	c.Assert(err, IsNil)
	canProcessMempoolSwap := canUseMempoolSwap(
		ctx,
		mgr,
		common.Tx{Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}},
		memo,
	)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("swap should remain mempool-eligible when the feature is enabled"))
	torValue, hasCapacity, err := checkMempoolSwapCapacity(ctx, mgr.Keeper(), common.BTCAsset, cosmos.NewUint(common.One))
	c.Assert(err, IsNil)
	c.Check(hasCapacity, Equals, true, Commentf("eligible swap should have capacity with a large cap"))
	c.Check(torValue.GT(cosmos.ZeroUint()), Equals, true, Commentf("capacity check should return a TOR value for the inbound"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Disable mempool swaps (cap = 0)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 0)

	// Verify swap memo is NOT admitted before finalization
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), "=:ETH.ETH:"+GetRandomETHAddress().String()+":0/1/1")
	c.Assert(err, IsNil)
	canProcessMempoolSwap := canUseMempoolSwap(
		ctx,
		mgr,
		common.Tx{Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}},
		memo,
	)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("policy eligibility should not depend on the runtime cap setting"))
	torValue, hasCapacity, err := checkMempoolSwapCapacity(ctx, mgr.Keeper(), common.BTCAsset, cosmos.NewUint(common.One))
	c.Assert(err, IsNil)
	c.Check(hasCapacity, Equals, false, Commentf("disabled mempool swaps should report no runtime capacity"))
	c.Check(torValue.IsZero(), Equals, true, Commentf("disabled mempool swaps should not return a TOR value"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestSecuredTargetMempoolSwapStagesUnderlyingAndRollsBack(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// This test checks that a secured target is staged as the layer1 asset and reversed the same way.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.LPUnits = targetPool.BalanceRune
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomTHORAddress()
	owner, err := cosmos.AccAddressFromBech32(destination.String())
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		fmt.Sprintf("=:%s:%s:0/1/1", common.ETHAsset.GetSecuredAsset(), destination),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Start the mempool swap and let the swap queue create the staged outbound.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	runSwapQueues(c, ctx, mgr)

	postSwapVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(postSwapVoter.Actions, HasLen, 1)
	c.Check(postSwapVoter.Actions[0].Coin.Asset.Equals(common.ETHAsset.GetSecuredAsset()), Equals, true, Commentf("planned action should retain the secured target asset"))

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(stagedItems, HasLen, 1)
	c.Check(stagedItems[0].TxOutItem.Coin.Asset.Equals(common.ETHAsset), Equals, true, Commentf("mempool staging should escrow the underlying asset"))
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, owner).IsZero(), Equals, true, Commentf("secured balance should remain empty before finalization"))
	// Build the reverse swap from the staged underlying asset.
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, postSwapVoter, common.BTCAsset, stagedItems[0].TxOutItem, 0, activeVault)
	c.Assert(err, IsNil)

	// Roll back the mempool swap before the inbound finalizes.
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, postSwapVoter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("secured target rollback should normalize the voter"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("secured target rollback should clear speculative actions"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("secured target rollback should release mempool swap capacity"))
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, owner).IsZero(), Equals, true, Commentf("secured target rollback should not credit the secured balance"))

	stagedItems, err = mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("secured target rollback should delete staged outbounds"))

	c.Check(expectedReverse.Tx.Coins[0].Asset.Equals(common.ETHAsset), Equals, true, Commentf("secured target rollback should reverse-swap the escrowed underlying asset"))
	c.Check(expectedReverse.TargetAsset.Equals(common.BTCAsset), Equals, true, Commentf("secured target rollback should target the original source asset"))
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("secured target rollback should settle inline instead of queueing a reverse swap"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestSecuredTargetAdvQueuePartialRollbackExecutesInlineRollbackSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// This test checks the partial advanced-queue case, before any staged outbound exists.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.LPUnits = targetPool.BalanceRune
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomTHORAddress()
	owner, err := cosmos.AccAddressFromBech32(destination.String())
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		fmt.Sprintf("SWAP:%s:%s:0/1/1", common.ETHAsset.GetSecuredAsset(), destination),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	queuedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, IsNil)
	c.Assert(queuedSwap.State, NotNil)

	queuedSwap.State.Quantity = 2
	queuedSwap.State.Interval = 1
	queuedSwap.State.Deposit = queuedSwap.Tx.Coins[0].Amount
	queuedSwap.State.In = cosmos.ZeroUint()
	queuedSwap.State.Out = cosmos.ZeroUint()
	queuedSwap.State.Count = 0
	queuedSwap.State.LastHeight = ctx.BlockHeight() - 1
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queuedSwap), IsNil)

	// Run one queue step so the swap has partial output stored in queue state.
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	partialSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, IsNil)
	c.Check(partialSwap.State.Out.GT(cosmos.ZeroUint()), Equals, true, Commentf("partial advanced-queue execution should accumulate emitted output"))

	partialVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("partial advanced-queue execution should not stage the secured outbound yet"))
	pendingAction, err := buildPendingMempoolRollbackAction(ctx, mgr.Keeper(), partialVoter)
	c.Assert(err, IsNil)
	c.Assert(pendingAction, NotNil)
	// Build the reverse swap from the partial output recovered from queue state.
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, partialVoter, common.BTCAsset, *pendingAction, 0, activeVault)
	c.Assert(err, IsNil)

	// Roll back the partial mempool execution.
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, partialVoter)
	c.Assert(err, IsNil)

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, NotNil, Commentf("partial rollback should remove the pending advanced-queue swap"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("partial rollback should release mempool swap capacity"))
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, owner).IsZero(), Equals, true, Commentf("partial rollback should not credit the secured balance"))

	c.Check(expectedReverse.Tx.Coins[0].Asset.Equals(common.ETHAsset), Equals, true, Commentf("partial rollback should reverse-swap the accumulated underlying output"))
	c.Check(expectedReverse.Tx.Coins[0].Amount.Equal(partialSwap.State.Out), Equals, true, Commentf("partial rollback should reverse exactly the accumulated output"))
	c.Check(expectedReverse.TargetAsset.Equals(common.BTCAsset), Equals, true, Commentf("partial rollback should target the original source asset"))
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("partial rollback should settle inline instead of queueing a reverse swap"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestInactiveVaultSkipsMempoolSwapBeforeFinalization(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// Before finalization, an inactive vault should block mempool execution completely.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	na := GetRandomValidatorNode(NodeActive)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)

	toAddr, err := inactiveVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 15)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// The handler should refuse to start the mempool swap.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	// Check that no mempool capacity or queue state was created.
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("inactive vault should not reserve mempool swap capacity"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("inactive vault should not consume mempool swap capacity"))
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, inTxID, 0), Equals, false, Commentf("inactive vault should not queue mempool swap"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false, Commentf("inactive vault should not queue mempool swap"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestTradingHaltSkipsMempoolProcessingUntilFinalization(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, "HaltTrading", 1)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("trading halt should not reserve mempool swap capacity"))
	c.Check(len(updatedVoter.Actions), Equals, 0, Commentf("trading halt should not create speculative actions"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("trading halt should not consume mempool swap capacity"))
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, inTxID, 0), Equals, false, Commentf("trading halt should not leave a legacy queue item behind"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false, Commentf("trading halt should not leave an advanced queue item behind"))

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("trading halt should not stage speculative outbounds before finalization"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 0, Commentf("trading halt should not schedule refund outbounds before finalization"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestInactiveVaultFinalizedMempoolSwapRevertsBeforeRefund(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// After finalization, the code should undo the mempool swap first and then refund the user.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)

	na := GetRandomValidatorNode(NodeActive)

	toAddr, err := inactiveVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	finalizedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 12)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(50*common.One))

	stagedOutbound := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(3*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}
	voter.Actions = []TxOutItem{stagedOutbound}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedOutbound, ctx.BlockHeight()+5), IsNil)

	// Finalized handling should clean up the staged rollback state and then schedule a refund.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), finalizedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("inactive-vault finalization should not leave staged mempool outbounds behind"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("inactive-vault finalization should normalize the voter"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("inactive-vault finalization should clear the voter mempool TOR value"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("inactive-vault rollback should release mempool swap capacity"))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("inactive-vault finalization should settle the reverse swap inline"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("inactive-vault finalization should schedule exactly one refund outbound"))
	// Check the refund details.
	c.Check(outItems[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(outItems[0].Coin.Amount.GT(cosmos.ZeroUint()), Equals, true)
	c.Check(outItems[0].Coin.Amount.LTE(cosmos.NewUint(1*common.One)), Equals, true)
	c.Check(outItems[0].ToAddress.Equals(inboundTx.FromAddress), Equals, true)
	c.Check(outItems[0].GetMemo(), Equals, NewRefundMemo(inTxID).String())
}

// TestRevertMempoolSwapSynthOutboundExecutesInlineRollbackSwap verifies that
// synth outbounds use the same inline rollback-swap path as other reversible outputs
// and settle inline during rollback.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapSynthOutboundExecutesInlineRollbackSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set up realistic source and target pools so the swap mutates both pool
	// state and synth supply before the rollback path tries to unwind it.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1111 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(34 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1111 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(34 * common.One)
	targetPool.LPUnits = targetPool.BalanceRune
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	activeVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	destination := GetRandomTHORAddress()
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(200_000))},
		"",
	)
	tx.Chain = common.BTCChain

	// Execute a real BTC -> synth ETH swap first so the subsequent rollback runs
	// against genuine post-swap pool balances rather than hand-crafted numbers.
	synthAmount, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.ETHAsset.GetSyntheticAsset(), destination, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr)
	c.Assert(err, IsNil)

	postSwapSourcePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	postSwapTargetPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	postSwapSynthSupply := mgr.Keeper().GetTotalSupply(ctx, common.ETHAsset.GetSyntheticAsset())
	c.Assert(postSwapSynthSupply.GT(cosmos.ZeroUint()), Equals, true)
	c.Assert(postSwapSourcePool.BalanceRune.Equal(sourcePool.BalanceRune), Equals, false)
	c.Assert(postSwapTargetPool.BalanceRune.Equal(targetPool.BalanceRune), Equals, false)

	observedTx := ObservedTx{Tx: tx}
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observedTx})
	voter.Tx = observedTx

	// Stage the synth outbound exactly the way the mempool path would: native
	// THORChain txout with a synth asset coin.
	stagedOutbound := TxOutItem{
		Chain:     common.THORChain,
		InHash:    tx.ID,
		ToAddress: destination,
		Coin:      common.NewCoin(common.ETHAsset.GetSyntheticAsset(), synthAmount),
	}
	voter.Actions = []TxOutItem{stagedOutbound}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedOutbound, ctx.BlockHeight()+5), IsNil)

	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("synth mempool rollback should normalize the voter"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("synth mempool rollback should clear speculative actions"))

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("synth mempool rollback should delete staged outbounds"))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(tx.ID, 0), 0)
	c.Assert(err, NotNil, Commentf("synth mempool rollback should settle the reverse swap inline"))

	updatedSourcePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	updatedTargetPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedSourcePool.BalanceRune.Equal(postSwapSourcePool.BalanceRune), Equals, false, Commentf("synth mempool rollback should unwind the source pool immediately"))
	c.Check(updatedSourcePool.BalanceAsset.Equal(postSwapSourcePool.BalanceAsset), Equals, false, Commentf("synth mempool rollback should unwind the source pool asset immediately"))
	c.Check(updatedTargetPool.BalanceRune.Equal(postSwapTargetPool.BalanceRune), Equals, false, Commentf("synth mempool rollback should unwind the target pool immediately"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapSynthAffiliateTargetBuildsGrossRollbackSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// This test checks that synth staging keeps the full amount until finalization.
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.LPUnits = pool.BalanceRune
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}

	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	ownerAddr := GetRandomTHORAddress()
	affiliateAddr := GetRandomTHORAddress()
	inTxID := GetRandomTxHash()
	outboundAmount := cosmos.NewUint(1000 * common.One)
	targetAsset := common.ETHAsset.GetSyntheticAsset()
	inboundMemo := NewSwapMemo(ctx, mgr, targetAsset, ownerAddr, cosmos.ZeroUint(), affiliateAddr.String(), cosmos.NewUint(500))

	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:          inTxID,
			Chain:       common.BTCChain,
			FromAddress: GetRandomBTCAddress(),
			ToAddress:   GetRandomBTCAddress(),
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			},
			Memo: inboundMemo,
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: 0,
		Signers:        []string{na.NodeAddress.String()},
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	voter.Actions = []TxOutItem{{
		Chain:      common.THORChain,
		InHash:     inTxID,
		ToAddress:  ownerAddr,
		Coin:       common.NewCoin(targetAsset, outboundAmount),
		ModuleName: ModuleName,
	}}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Add the outbound through the normal txout path so the staged item matches production behavior.
	ok, err := txOutStore.TryAddTxOutItem(ctx, mgr, voter.Actions[0], cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(stagedItems, HasLen, 1)
	c.Check(stagedItems[0].TxOutItem.Coin.Equals(common.NewCoin(targetAsset, outboundAmount)), Equals, true,
		Commentf("mempool staging should keep the gross synth amount until finalization"))
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, voter, common.BTCAsset, stagedItems[0].TxOutItem, 0, activeVault)
	c.Assert(err, IsNil)

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, NotNil, Commentf("mempool staging should not create affiliate sidecars before finalization"))

	// Mirror the forward swap's synth mint path so rollback can consume the
	// staged synth from Asgard, just like it would in the real mempool flow.
	synthCoin := common.NewCoin(targetAsset, outboundAmount)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)

	// Roll back and make sure the gross amount is used.
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("synth affiliate rollback should normalize the voter"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("synth affiliate rollback should clear speculative actions"))

	c.Check(expectedReverse.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(targetAsset, outboundAmount)},
		Commentf("rollback should reverse the full staged synth amount, not a fee-reduced amount"))
	c.Check(expectedReverse.TargetAsset.Equals(common.BTCAsset), Equals, true)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("rollback should settle the synth reverse swap inline"))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, NotNil, Commentf("rollback should not leave deferred affiliate sidecars queued"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestSelectActiveVaultForChainPrefersUsableSourceChainVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Build one bad vault and one good vault for the source chain.
	sourcePool := NewPool()
	sourcePool.Asset = common.SOLAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	unsupportedVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	unsupportedVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, unsupportedVault), IsNil)

	supportedVault := NewVaultV2(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.SOLChain, common.THORChain}.Strings(),
		[]ChainContract{},
		types.GetRandomEd25519PubKey(),
	)
	supportedVault.Coins = common.Coins{
		common.NewCoin(common.SOLAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, supportedVault), IsNil)

	supportedSOLAddress, err := supportedVault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:    inTxID,
			Chain: common.SOLChain,
			Coins: common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(1*common.One))},
		},
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: 0,
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.Actions = []TxOutItem{{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: GetRandomPubKey(),
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
	}}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], ctx.BlockHeight()+5), IsNil)

	// The selector should pick the vault that can actually receive the source chain asset.
	selectedVault, err := selectActiveVaultForChain(ctx, mgr.Keeper(), common.SOLChain)
	c.Assert(err, IsNil)
	c.Check(selectedVault.PubKey.Equals(supportedVault.PubKey), Equals, true)
	// The rollback swap should use that same vault address in the memo and destination.
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, voter, common.SOLAsset, voter.Actions[0], 0, selectedVault)
	c.Assert(err, IsNil)

	c.Check(expectedReverse.Destination.Equals(supportedSOLAddress), Equals, true,
		Commentf("reverse swap should target the active vault that can receive the SOL source asset"))
	c.Check(expectedReverse.Tx.Memo, Equals, fmt.Sprintf("=:%s:%s", common.SOLAsset, supportedSOLAddress))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestTimeoutRevertedMempoolSwapFinalizesNormallyWithoutResume(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// This test checks a timeout-style rollback followed later by a real finalized inbound.
	// Setup vault
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	// Create a voter with mempool-swap state, then roll it back.
	inTxID := GetRandomTxHash()
	fromAddr := GetRandomBTCAddress()
	ethDestination := GetRandomETHAddress()
	unfinalizedTx := ObservedTx{
		Tx: common.Tx{
			ID:          inTxID,
			Chain:       common.BTCChain,
			FromAddress: fromAddr,
			ToAddress:   GetRandomBTCAddress(),
			Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
			Memo:        fmt.Sprintf("=:ETH.ETH:%s", ethDestination),
		},
		ObservedPubKey: vault.PubKey,
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{unfinalizedTx})
	voter.Tx = unfinalizedTx
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50000000000)
	voter.Actions = []TxOutItem{{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
	}}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], ctx.BlockHeight()+5), IsNil)

	// First remove the mempool-only state.
	err := rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("timeout rollback should normalize the voter"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("timeout rollback should clear mempool TOR bookkeeping immediately"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("timeout rollback should discard actions immediately"))

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	nas := NodeAccounts{na}

	finalizedTx := unfinalizedTx
	finalizedTx.FinaliseHeight = ctx.BlockHeight()
	finalizedTx.Signers = []string{na.NodeAddress.String()}
	updatedVoter.Txs = ObservedTxs{finalizedTx}
	updatedVoter.Tx = finalizedTx
	updatedVoter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, updatedVoter)

	// Then process the real finalized inbound and make sure normal swap handling starts again.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, nas, NewInternalHandler(mgr), finalizedTx, updatedVoter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)
	postFinalVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(len(postFinalVoter.Actions) > 0, Equals, true,
		Commentf("confirmed inbound should repopulate actions for the real finalized swap after timeout rollback"))
	c.Check(len(outItems) > 0, Equals, true,
		Commentf("confirmed inbound should schedule real outbounds after timeout rollback"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapAggregatesSplitExternalOutboundsByAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// This test checks that two same-asset external outbounds become one reverse action.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.THORChain}.Strings(), nil)
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.Tx = ObservedTx{
		Tx: common.Tx{
			ID:    inTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		},
	}
	voter.Actions = []TxOutItem{
		{Chain: common.ETHChain, InHash: inTxID, ToAddress: GetRandomETHAddress(), Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One))},
		{Chain: common.ETHChain, InHash: inTxID, ToAddress: GetRandomETHAddress(), Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(3*common.One))},
	}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	firstHeight := ctx.BlockHeight() + 25
	secondHeight := ctx.BlockHeight() + 30
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], firstHeight), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[1], secondHeight), IsNil)
	// Check the helper result directly before running the full rollback.
	aggregated := aggregateMempoolRollbackActions([]mempoolRollbackAction{
		{item: voter.Actions[0], actionIndex: 0},
		{item: voter.Actions[1], actionIndex: 1},
	})
	c.Assert(aggregated, HasLen, 1)
	c.Check(aggregated[0].item.Coin.Equals(common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))), Equals, true,
		Commentf("split external staged outbounds should aggregate into one reverse action per asset"))

	// The real rollback should also finish inline with no queued reverse swap left behind.
	err := rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("aggregated rollback should settle inline instead of leaving a reverse swap queued"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedAdvQueueSettlementReleasesMempoolSwapCapacity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// This test checks that late settlement releases mempool capacity after the inbound finalizes.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.LPUnits = targetPool.BalanceRune
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomETHAddress()

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+destination.String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Start the mempool swap and fetch the queue item.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	queuedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, IsNil)
	c.Assert(queuedSwap.State, NotNil)

	queuedSwap.State.Quantity = 2
	queuedSwap.State.Interval = 1
	queuedSwap.State.Deposit = queuedSwap.Tx.Coins[0].Amount
	queuedSwap.State.In = cosmos.ZeroUint()
	queuedSwap.State.Out = cosmos.ZeroUint()
	queuedSwap.State.Count = 0
	queuedSwap.State.LastHeight = ctx.BlockHeight() - 1
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queuedSwap), IsNil)

	// Run one partial step before finalization.
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	partialVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(partialVoter.HasReservedMempoolSwapTOR(), Equals, true, Commentf("partial execution should still hold mempool swap capacity"))
	c.Check(partialVoter.Actions, HasLen, 0, Commentf("advanced queue should not have scheduled outbounds before settlement"))
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false, Commentf("partial execution should not create staged outbounds"))

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	finalizedVoter := partialVoter
	finalizedVoter.Tx = NewObservedTx(inboundTx, 15, activeVault.PubKey, 15)
	finalizedVoter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, finalizedVoter)

	// Run settlement again after finalization. This is where the capacity should be released.
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("settlement after finalization should release the mempool swap reservation"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("settlement after finalization should release global mempool swap usage"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false, Commentf("completed streaming swap should leave the advanced queue"))
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false, Commentf("finalized settlement should queue direct outbounds instead of staging"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("finalized settlement should queue the outbound immediately"))
	c.Check(outItems[0].InHash.Equals(inTxID), Equals, true)
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedLegacyQueueSettlementReleasesMempoolSwapCapacity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.LPUnits = targetPool.BalanceRune
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomETHAddress()

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+destination.String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)

	msg := NewMsgSwap(inboundTx, common.ETHAsset, destination, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 1, types.SwapVersion_v1, GetRandomBech32Addr())
	queue := newSwapQueue(mgr.Keeper())
	handleErr, settleErr := queue.executeAndSettleSwapQueueItemCached(ctx, mgr, swapItem{msg: *msg, index: 0})
	c.Assert(handleErr, IsNil)
	c.Assert(settleErr, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("legacy settlement after finalization should release the mempool swap reservation"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("legacy settlement after finalization should release global mempool swap usage"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("legacy finalized settlement should queue the outbound immediately"))
	c.Check(outItems[0].InHash.Equals(inTxID), Equals, true)
}
