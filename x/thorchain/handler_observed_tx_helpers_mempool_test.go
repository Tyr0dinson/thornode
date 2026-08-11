package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxHelpersMempoolSuite struct{}

var _ = Suite(&HandlerObservedTxHelpersMempoolSuite{})

type failGetStagedOutboundsKeeper struct {
	keeper.Keeper
}

func (k failGetStagedOutboundsKeeper) GetStagedOutbounds(_ cosmos.Context, _ common.TxID) ([]keeper.StagedOutbound, error) {
	return nil, fmt.Errorf("fail to get staged outbounds")
}

type failSecondSetSwapperCloutKeeper struct {
	keeper.Keeper
	setCalls int
}

func (k *failSecondSetSwapperCloutKeeper) SetSwapperClout(ctx cosmos.Context, record SwapperClout) error {
	k.setCalls++
	if k.setCalls == 2 {
		return fmt.Errorf("fail second swapper clout write")
	}
	return k.Keeper.SetSwapperClout(ctx, record)
}

func runSwapQueues(c *C, ctx cosmos.Context, mgr *Mgrs) {
	c.Assert(mgr.SwapQ().EndBlock(ctx, mgr), IsNil)
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	}
}

func setupAdvancedMempoolManagerForTest(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	return ctx, mgr
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestReclaimStagedCloutRollbackOnPartialWriteError(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	sender := GetRandomBTCAddress()
	recipient := GetRandomBTCAddress()

	senderClout := NewSwapperClout(sender)
	senderClout.Spent = cosmos.NewUint(100)
	recipientClout := NewSwapperClout(recipient)
	recipientClout.Spent = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetSwapperClout(ctx, senderClout), IsNil)
	c.Assert(mgr.Keeper().SetSwapperClout(ctx, recipientClout), IsNil)

	spent := cosmos.NewUint(80)
	voter := NewObservedTxVoter(GetRandomTxHash(), ObservedTxs{})
	voter.Tx = ObservedTx{Tx: common.Tx{FromAddress: sender}}
	stagedItems := []keeper.StagedOutbound{{
		TxOutItem: TxOutItem{
			ToAddress:  recipient,
			CloutSpent: &spent,
		},
	}}
	failKeeper := &failSecondSetSwapperCloutKeeper{Keeper: mgr.Keeper()}

	err := reclaimStagedClout(ctx, failKeeper, voter, stagedItems)
	c.Assert(err, ErrorMatches, "fail to save recipient clout: fail second swapper clout write")
	c.Check(failKeeper.setCalls, Equals, 2)

	// The sender write happened inside the nested cache before the recipient
	// write failed. Neither side may escape to the parent context.
	updatedSender, err := mgr.Keeper().GetSwapperClout(ctx, sender)
	c.Assert(err, IsNil)
	updatedRecipient, err := mgr.Keeper().GetSwapperClout(ctx, recipient)
	c.Assert(err, IsNil)
	c.Check(updatedSender.Reclaimed.IsZero(), Equals, true)
	c.Check(updatedRecipient.Reclaimed.IsZero(), Equals, true)
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRestoreFinalNativeMempoolTxOutItemValidatesPosition(c *C) {
	inHash := GetRandomTxHash()
	securedAsset := common.BTCAsset.GetSecuredAsset()
	voter := NewObservedTxVoter(inHash, ObservedTxs{})
	voter.Actions = []TxOutItem{
		{
			Chain:  common.THORChain,
			InHash: inHash,
			Coin:   common.NewCoin(securedAsset, cosmos.NewUint(3*common.One)),
		},
		{
			Chain:  common.THORChain,
			InHash: inHash,
			Coin:   common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
		},
	}

	staged := []keeper.StagedOutbound{
		{
			TxOutItem: TxOutItem{
				Chain:  common.THORChain,
				InHash: inHash,
				Coin:   common.NewCoin(common.BTCAsset, voter.Actions[0].Coin.Amount),
			},
		},
		{
			TxOutItem: voter.Actions[1],
		},
	}

	c.Assert(validateStagedOutboundActionAlignment(voter, staged), IsNil)
	err := validateStagedOutboundActionAlignment(voter, staged[:1])
	c.Assert(err, ErrorMatches, "staged outbound/action count mismatch for .*: 1 staged, 2 actions")

	securedItem, err := restoreFinalNativeMempoolTxOutItem(voter, 0, staged[0])
	c.Assert(err, IsNil)
	c.Check(securedItem.Coin.Asset.Equals(securedAsset), Equals, true)
	runeItem, err := restoreFinalNativeMempoolTxOutItem(voter, 1, staged[1])
	c.Assert(err, IsNil)
	c.Check(runeItem.Coin.Asset.Equals(common.RuneAsset()), Equals, true)

	_, err = restoreFinalNativeMempoolTxOutItem(voter, 0, staged[1])
	c.Assert(err, ErrorMatches, "staged escrow asset mismatch at index 0: staged .* action .*")
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutbounds(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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

// TestFinalizeOutboundsRollbackOnError verifies that a finalize failure leaves
// staged outbounds and mempool-capacity accounting untouched.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsRollbackOnError(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	dummyMgr := NewDummyMgrWithKeeper(mgr.Keeper())

	// Seed one staged outbound plus some reserved mempool capacity.
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.MempoolSwapTOR = cosmos.NewUint(123 * common.One)

	stagedItem := TxOutItem{
		Chain:  common.THORChain,
		InHash: inTxID,
		Coin:   common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
	}

	// Align the voter action with the staged outbound so finalization gets past
	// validateStagedOutboundActionAlignment and actually reaches the cached
	// mutations this test asserts are rolled back.
	voter.Actions = []TxOutItem{stagedItem}

	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedItem, ctx.BlockHeight()+5), IsNil)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(999*common.One))

	// Run finalize through a manager that cannot complete the cached flow.
	err := finalizeOutboundsAtomic(ctx, dummyMgr, voter)
	c.Assert(err, NotNil)

	// The failed CacheContext commit must leave the outer store unchanged.
	usage := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)
	c.Check(usage.Equal(cosmos.NewUint(999*common.One)), Equals, true, Commentf("mempool usage should roll back on finalize failure"))

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 1, Commentf("staged outbounds should remain after finalize failure"))
}

// TestFinalizedHandlerKeepsPendingRevShareOnFinalizeError verifies that the
// finalized observation path does not commit speculative REVSHARE outside the
// same cache as action-backed outbound finalization. The handler deliberately
// swallows a finalize error so voter/vault observation state can persist; the
// transaction journal must nevertheless remain available for retry or rollback.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedHandlerKeepsPendingRevShareOnFinalizeError(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.BTCChain, common.ETHChain}.Strings(),
		nil,
	)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	vaultAddress, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		vaultAddress,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	finalizedTx := NewObservedTx(inboundTx, ctx.BlockHeight(), activeVault.PubKey, ctx.BlockHeight())

	stagedItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		Memo:        NewOutboundMemo(inTxID).String(),
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(123 * common.One)
	voter.Actions = []TxOutItem{stagedItem}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	usageBefore := cosmos.NewUint(999 * common.One)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, usageBefore)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedItem, ctx.BlockHeight()+5), IsNil)

	pendingFee := cosmos.NewUint(77)
	c.Assert(mgr.Keeper().AddPendingAffiliateLiquidityFee(ctx, inTxID, 0, "AFFX", pendingFee), IsNil)
	baselineFee := cosmos.NewUint(11)
	c.Assert(mgr.Keeper().AddAffiliateLiquidityFee(ctx, "BASE", baselineFee), IsNil)

	// The dummy manager's TxOutStore is intentionally not *TxOutStorage, so the
	// cached finalizer fails after committing pending REVSHARE in its child cache.
	// A direct probe confirms the fixture reaches that late failure without
	// changing the parent state.
	dummyMgr := NewDummyMgrWithKeeper(mgr.Keeper())
	err = finalizeOutboundsAtomic(ctx, dummyMgr, voter)
	c.Assert(err, ErrorMatches, "fail to cast TxOutStore to TxOutStorage")
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, true)

	err = handleObservedTxInQuorum(
		ctx,
		dummyMgr,
		na.NodeAddress,
		NodeAccounts{na},
		NewInternalHandler(dummyMgr),
		finalizedTx,
		voter,
		[]cosmos.AccAddress{na.NodeAddress},
		true,
	)
	c.Assert(err, IsNil)

	pending, err := mgr.Keeper().GetPendingAffiliateLiquidityFees(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1, Commentf("failed finalization must retain pending REVSHARE for retry or rollback"))
	c.Check(pending[0].Thorname, Equals, "AFFX")
	c.Check(pending[0].Fee.Equal(pendingFee), Equals, true)

	accrued := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(accrued, HasLen, 1, Commentf("failed finalization must not leak pending REVSHARE into the payout accumulator"))
	c.Check(accrued[0].Thorname, Equals, "BASE")
	c.Check(accrued[0].Fee.Equal(baselineFee), Equals, true)

	staged, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(staged, HasLen, 1, Commentf("failed finalization must retain staged outbound ownership"))
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(updatedVoter.Actions, HasLen, 1)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(usageBefore), Equals, true)

	// A corrupted snapshot with staged ownership but no matching Actions cannot
	// be action-finalized. It must likewise retain the transaction-scoped journal
	// rather than promote REVSHARE that a later forced rollback cannot identify.
	updatedVoter.Actions = nil
	mgr.Keeper().SetObservedTxInVoter(ctx, updatedVoter)
	err = handleObservedTxInQuorum(
		ctx,
		dummyMgr,
		na.NodeAddress,
		NodeAccounts{na},
		NewInternalHandler(dummyMgr),
		finalizedTx,
		updatedVoter,
		[]cosmos.AccAddress{na.NodeAddress},
		true,
	)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, true)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), DeepEquals, accrued)
	staged, err = mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(staged, HasLen, 1)
	updatedVoter, err = mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(updatedVoter.Actions, HasLen, 0)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(usageBefore), Equals, true)
}

// TestFinalizeOutboundsRollbackOnDeferredAffiliateFeeError verifies that a
// deferred affiliate-fee failure aborts finalization instead of silently
// materializing the gross outbound amount.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsRollbackOnDeferredAffiliateFeeError(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

	// Provide the normal pool and vault fixtures so the affiliate-fee failure is
	// the only reason finalization aborts.
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
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	ownerAddr := GetRandomETHAddress()
	affiliateAddr := GetRandomTHORAddress()
	inTxID := GetRandomTxHash()
	outboundAmount := cosmos.NewUint(1000 * common.One)
	stagedHeight := ctx.BlockHeight() + 5
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
	}
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	voter.Actions = []TxOutItem{{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   ownerAddr,
		Coin:        common.NewCoin(common.ETHAsset, outboundAmount),
		Memo:        NewOutboundMemo(inTxID).String(),
	}}

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(999*common.One))
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], stagedHeight), IsNil)

	// No active validators are registered in this fixture, so deferred
	// affiliate-fee adjustment should fail before any finalization side effects
	// are committed.
	err := finalizeOutboundsAtomic(ctx, mgr, voter)
	c.Assert(err, ErrorMatches, ".*no active node accounts")

	usage := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)
	c.Check(usage.Equal(cosmos.NewUint(999*common.One)), Equals, true, Commentf("mempool usage should roll back on deferred affiliate failure"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, true, Commentf("deferred affiliate failure should keep the voter mempool reservation"))
	c.Assert(updatedVoter.Actions, HasLen, 1, Commentf("deferred affiliate failure should keep the speculative action intact"))
	c.Check(updatedVoter.Actions[0].Coin.Amount.Equal(outboundAmount), Equals, true, Commentf("deferred affiliate failure should not rewrite the gross outbound amount"))

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Assert(stagedItems, HasLen, 1, Commentf("staged outbounds should remain after deferred affiliate failure"))
	c.Check(stagedItems[0].TxOutItem.Coin.Amount.Equal(outboundAmount), Equals, true, Commentf("staged outbounds should remain gross after deferred affiliate failure"))

	txOut, err := mgr.Keeper().GetTxOut(ctx, stagedHeight)
	c.Assert(err, IsNil)
	c.Check(txOut.TxArray, HasLen, 0, Commentf("deferred affiliate failure should not queue any finalized outbound"))
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, inTxID, 2), Equals, false, Commentf("deferred affiliate failure should not queue affiliate sidecars"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 2), Equals, false, Commentf("deferred affiliate failure should not queue advanced affiliate sidecars"))
}

// TestFinalizeOutboundsAppliesDeferredAffiliateFeesToNativeNonRuneTargets
// checks that native synth, trade, and secured targets only apply deferred
// affiliate work once the staged outbound finalizes.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToNativeNonRuneTargets(c *C) {
	affiliateBps := cosmos.NewUint(500)
	outboundAmount := cosmos.NewUint(1000 * common.One)
	affiliateAmount := cosmos.NewUint(50 * common.One)
	netSynthAmount := outboundAmount.Sub(affiliateAmount)

	setupCase := func() (cosmos.Context, *Mgrs, NodeAccount) {
		ctx, mgr := setupAdvancedMempoolManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

		// Give each sub-case an isolated validator, vault, and pool fixture.
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

		return ctx, mgr, na
	}

	runCase := func(name string, finalAsset, stagedAsset common.Asset, moduleName string, checkBalance func(cosmos.Context, *Mgrs, cosmos.AccAddress)) {
		ctx, mgr, na := setupCase()
		// Build a finalized inbound and matching staged outbound for this asset flavor.
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

		_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
		c.Assert(err, NotNil, Commentf("case %s should not queue affiliate sidecars before finalization", name))

		// Finalization should apply the deferred affiliate work exactly once.
		err = finalizeOutboundsAtomic(ctx, mgr, voter)
		c.Assert(err, IsNil, Commentf("case %s", name))

		stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
		c.Assert(stagedErr, IsNil, Commentf("case %s", name))
		c.Check(stagedItems, HasLen, 0, Commentf("case %s should clear staged outbounds on finalization", name))

		affiliateSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
		c.Assert(err, IsNil, Commentf("case %s should queue affiliate sidecar on finalization", name))
		c.Check(affiliateSwap.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(finalAsset, affiliateAmount)}, Commentf("case %s should queue the deferred affiliate input", name))
		c.Check(affiliateSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true, Commentf("case %s should swap the affiliate fee to RUNE", name))

		// Check the asset-specific end state after the staged outbound becomes real.
		checkBalance(ctx, mgr, ownerAcc)
	}

	runCase("synth target", common.ETHAsset.GetSyntheticAsset(), common.ETHAsset.GetSyntheticAsset(), ModuleName, func(ctx cosmos.Context, mgr *Mgrs, ownerAcc cosmos.AccAddress) {
		balance := mgr.Keeper().GetBalance(ctx, ownerAcc)
		c.Check(balance.AmountOf(common.ETHAsset.GetSyntheticAsset().Native()).Equal(cosmos.NewIntFromBigInt(netSynthAmount.BigInt())), Equals, true, Commentf("synth target should finalize net of affiliate fee"))
	})

	runCase("trade target", common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetTradeAsset(), "", func(ctx cosmos.Context, mgr *Mgrs, ownerAcc cosmos.AccAddress) {
		balance := mgr.TradeAccountManager().BalanceOf(ctx, common.BTCAsset.GetTradeAsset(), ownerAcc)
		c.Check(balance.Equal(outboundAmount), Equals, true, Commentf("trade target should keep the full user balance on finalization"))
	})

	runCase("secured target", common.ETHAsset.GetSecuredAsset(), common.ETHAsset, "", func(ctx cosmos.Context, mgr *Mgrs, ownerAcc cosmos.AccAddress) {
		balance := mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, ownerAcc)
		c.Check(balance.Equal(outboundAmount), Equals, true, Commentf("secured target should keep the full user balance on finalization"))
	})
}

// TestFinalizeOutboundsAppliesDeferredAffiliateFeesToRuneTarget verifies that
// a native RUNE target pays the affiliate directly at finalization time rather
// than spawning a deferred sidecar swap.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToRuneTarget(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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

	_, queueErr := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
	c.Assert(queueErr, NotNil, Commentf("RUNE target should not queue an affiliate sidecar on finalization"))

	// The owner should get net RUNE and the affiliate should get the deferred fee.
	ownerBalance := mgr.Keeper().GetBalance(ctx, ownerAcc)
	affiliateBalance := mgr.Keeper().GetBalance(ctx, affiliateAcc)
	c.Check(ownerBalance.AmountOf(common.RuneNative.Native()).Equal(cosmos.NewIntFromBigInt(netAmount.BigInt())), Equals, true,
		Commentf("RUNE target should finalize net of affiliate fee"))
	c.Check(affiliateBalance.AmountOf(common.RuneNative.Native()).Equal(cosmos.NewIntFromBigInt(affiliateAmount.BigInt())), Equals, true,
		Commentf("RUNE target should transfer the deferred affiliate fee on finalization"))
}

// TestFinalizeOutboundsAppliesDeferredAffiliateFeesToExternalTarget verifies
// that split external outbounds are regrouped at finalization, rewritten to
// their net queued amounts, and paired with one deferred affiliate sidecar.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsAppliesDeferredAffiliateFeesToExternalTarget(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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
	outboundFee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.ETHAsset, false)
	c.Assert(err, IsNil)
	expectedQueuedAmount := netAmount.Sub(outboundFee).Sub(outboundFee)
	// The two queued pieces together should equal the deferred-affiliate net
	// amount less the real outbound fee on each finalized split piece.
	totalQueued := cosmos.ZeroUint()
	for _, item := range txOut.TxArray {
		totalQueued = totalQueued.Add(item.Coin.Amount)
	}
	c.Check(totalQueued.Equal(expectedQueuedAmount), Equals, true,
		Commentf("split external targets should enter TxOut net of deferred affiliate fee and per-piece outbound fees"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.Actions, HasLen, 2)
	finalActionTotal := cosmos.ZeroUint()
	for _, action := range updatedVoter.Actions {
		finalActionTotal = finalActionTotal.Add(action.Coin.Amount)
	}
	c.Check(finalActionTotal.Equal(expectedQueuedAmount), Equals, true,
		Commentf("finalized actions should be rewritten to the net queued outputs"))

	affiliateSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, IsNil)
	c.Check(affiliateSwap.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(common.ETHAsset, affiliateAmount)},
		Commentf("external target should queue the deferred affiliate sidecar on finalization"))
	c.Check(affiliateSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true)
}

// TestFinalizeOutboundsCommitsOutboundFeeAccountingAtFinalization verifies that
// mempool staging keeps outbound-fee accounting speculative and only commits it
// when the staged outbound is finalized into a real outbound.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizeOutboundsCommitsOutboundFeeAccountingAtFinalization(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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
	inTxID := GetRandomTxHash()
	outboundAmount := cosmos.NewUint(100 * common.One)
	inboundMemo := NewSwapMemo(ctx, mgr, common.ETHAsset, ownerAddr, cosmos.ZeroUint(), "", cosmos.ZeroUint())
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
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	initialOutboundFeeWithheldRune, err := mgr.Keeper().GetOutboundFeeWithheldRune(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	initialPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)

	// Stage a gross outbound first. The earlier mempool path must not deduct the
	// outbound fee or commit its accounting side effects yet.
	ok, err := txOutStore.TryAddTxOutItem(ctx, mgr, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   ownerAddr,
		Coin:        common.NewCoin(common.ETHAsset, outboundAmount),
		Memo:        NewOutboundMemo(inTxID).String(),
	}, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(stagedItems, HasLen, 1)
	c.Check(stagedItems[0].TxOutItem.Coin.Amount.Equal(outboundAmount), Equals, true,
		Commentf("mempool staging should keep the gross external outbound amount"))

	// Before finalization, the withheld-fee accounting should still be untouched.
	outboundFeeWithheldRune, err := mgr.Keeper().GetOutboundFeeWithheldRune(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(outboundFeeWithheldRune.Equal(initialOutboundFeeWithheldRune), Equals, true,
		Commentf("mempool staging should not add outbound fee withheld rune early"))

	stagedHeight := stagedItems[0].ScheduledHeight
	voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Finalization is the point where the staged gross outbound becomes a real
	// outbound and must therefore pick up the normal outbound fee accounting.
	err = finalizeOutboundsAtomic(ctx, mgr, voter)
	c.Assert(err, IsNil)

	outboundFee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.ETHAsset, false)
	c.Assert(err, IsNil)
	expectedNetAmount := outboundAmount.Sub(outboundFee)
	expectedRuneFee := initialPool.RuneDisbursementForAssetAdd(outboundFee)
	if expectedRuneFee.GT(initialPool.BalanceRune) {
		expectedRuneFee = initialPool.BalanceRune
	}

	// The queued outbound is now net of the outbound fee, proving the deduction
	// happened at finalization rather than during staging.
	txOut, err := mgr.Keeper().GetTxOut(ctx, stagedHeight)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 1)
	c.Check(txOut.TxArray[0].Coin.Amount.Equal(expectedNetAmount), Equals, true,
		Commentf("finalization should apply the outbound fee when queueing the staged outbound"))

	// Fee-side accounting should also land exactly once at finalization.
	outboundFeeWithheldRune, err = mgr.Keeper().GetOutboundFeeWithheldRune(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(outboundFeeWithheldRune.Sub(initialOutboundFeeWithheldRune).Equal(expectedRuneFee), Equals, true,
		Commentf("finalization should record the outbound fee withheld rune exactly once"))

	// Pool balances should now reflect the same finalized fee that was kept out
	// of the mempool staging path.
	updatedPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.Equal(initialPool.BalanceAsset.Add(outboundFee)), Equals, true,
		Commentf("finalization should add the outbound asset fee back into the pool"))
	c.Check(updatedPool.BalanceRune.Equal(common.SafeSub(initialPool.BalanceRune, expectedRuneFee)), Equals, true,
		Commentf("finalization should deduct the corresponding rune fee from the pool"))

	// The voter actions should also be rewritten from speculative gross outputs
	// to the finalized net outputs so outbound observation still matches.
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.Actions, HasLen, 1)
	c.Check(updatedVoter.Actions[0].Coin.Amount.Equal(expectedNetAmount), Equals, true,
		Commentf("finalization should rewrite the planned action to the net outbound amount"))
}

// TestRevertMempoolSwap exercises the main rollback variants: standard external
// outbounds, already-signed/no-staged cases, native RUNE, and native synth
// outputs that must all settle inline.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwap(c *C) {
	setupCase := func() (cosmos.Context, *Mgrs, Pool, Pool, Vault) {
		ctx, mgr := setupAdvancedMempoolManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
		mgr.Keeper().SetMimir(ctx, constants.TxOutDelayMax.String(), 17280)

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

		return ctx, mgr, sourcePool, targetPool, vault
	}

	{
		ctx, mgr, _, _, vault := setupCase()

		// Test 1: Successfully roll back a mempool swap.
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
		voter.Tx = observedTx
		voter.UnfinalizedHeight = ctx.BlockHeight()
		voter.FinalisedHeight = 0

		txOutItem := TxOutItem{
			Chain:       common.ETHChain,
			InHash:      inTxID,
			VaultPubKey: vault.PubKey,
			ToAddress:   GetRandomETHAddress(),
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
			Memo:        "OUT:" + inTxID.String(),
		}
		voter.Actions = []TxOutItem{txOutItem}

		outboundHeight := ctx.BlockHeight() + 10
		c.Assert(mgr.Keeper().SetStagedOutbound(ctx, txOutItem, outboundHeight), IsNil)
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		c.Assert(mgr.Keeper().AddPendingAffiliateLiquidityFee(ctx, inTxID, 0, "PENDING", cosmos.NewUint(77)), IsNil)
		c.Assert(mgr.Keeper().AddAffiliateLiquidityFee(ctx, "EXISTING", cosmos.NewUint(99)), IsNil)

		expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, voter, common.BTCAsset, txOutItem, 0, vault)
		c.Assert(err, IsNil)

		err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
		c.Assert(err, IsNil, Commentf("rollbackMempoolSwap failed: %v", err))

		updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
		c.Assert(err, IsNil)
		c.Check(updatedVoter.Reverted, Equals, false, Commentf("rollback should normalize the voter"))
		c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("rollback should clear the reserved mempool TOR value"))
		c.Check(updatedVoter.Actions, IsNil, Commentf("rollback should discard speculative actions"))
		c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, false,
			Commentf("successful rollback must discard speculative REVSHARE"))
		committedAffiliateFees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
		c.Assert(committedAffiliateFees, HasLen, 1)
		c.Check(committedAffiliateFees[0].Thorname, Equals, "EXISTING")
		c.Check(committedAffiliateFees[0].Fee.Equal(cosmos.NewUint(99)), Equals, true,
			Commentf("rollback must not promote pending REVSHARE or disturb unrelated accumulator state"))

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
		expectedSourceBalanceAsset := cosmos.NewUint(99900000000)
		c.Check(updatedSourcePool.BalanceAsset.Equal(expectedSourceBalanceAsset), Equals, true,
			Commentf("inline rollback should set source pool asset balance to %s, got %s", expectedSourceBalanceAsset, updatedSourcePool.BalanceAsset))
	}

	{
		ctx, mgr, _, _, _ := setupCase()

		// Test 2: Skip already signed outbounds (voter with no staged outbounds should still succeed).
		voter2TxID := GetRandomTxHash()
		voter2ObservedTx := ObservedTx{
			Tx: common.Tx{
				ID:    voter2TxID,
				Chain: common.ETHChain,
				Coins: common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
			},
		}
		voter2 := NewObservedTxVoter(voter2TxID, ObservedTxs{voter2ObservedTx})
		voter2.Tx = voter2ObservedTx
		voter2.UnfinalizedHeight = ctx.BlockHeight()
		voter2.Actions = []TxOutItem{{
			Chain:   common.ETHChain,
			InHash:  voter2.TxID,
			Coin:    common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
			OutHash: GetRandomTxHash(),
		}}

		err := rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter2)
		c.Assert(err, IsNil, Commentf("Should succeed with no staged outbounds"))

		updatedVoter2, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter2TxID)
		c.Assert(err, IsNil)
		c.Check(updatedVoter2.Reverted, Equals, false, Commentf("rollback with no staged outbounds should leave voter.Reverted false"))
		c.Check(updatedVoter2.Actions, IsNil, Commentf("rollback with no staged outbounds should still clear speculative actions"))
	}

	{
		ctx, mgr, _, _, _ := setupCase()

		// Test 3: Handle RUNE source asset (native output).
		runeTxID := GetRandomTxHash()
		runeObservedTx := ObservedTx{
			Tx: common.Tx{
				ID:    runeTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
			},
		}
		runeVoter := NewObservedTxVoter(runeTxID, ObservedTxs{runeObservedTx})
		runeVoter.Tx = runeObservedTx
		runeVoter.UnfinalizedHeight = ctx.BlockHeight()

		nativeTxOutItem := TxOutItem{
			Chain:     common.THORChain,
			InHash:    runeTxID,
			ToAddress: GetRandomRUNEAddress(),
			Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
		}
		runeVoter.Actions = []TxOutItem{nativeTxOutItem}
		c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeTxOutItem, ctx.BlockHeight()+15), IsNil)

		err := rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, runeVoter)
		c.Assert(err, IsNil, Commentf("Should handle native (RUNE) output"))

		updatedRuneVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, runeTxID)
		c.Assert(err, IsNil)
		c.Check(updatedRuneVoter.Reverted, Equals, false, Commentf("RUNE rollback should normalize the voter"))
		c.Check(updatedRuneVoter.Actions, IsNil, Commentf("RUNE rollback should clear speculative actions"))
	}

	{
		ctx, mgr, _, _, _ := setupCase()

		// Test 4: Non-RUNE native output (synth) should use inline rollback and
		// consume the staged synth from Asgard instead of queueing a reverse swap.
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

		synthTxOutItem := TxOutItem{
			Chain:     common.THORChain,
			InHash:    synthTxID,
			ToAddress: GetRandomRUNEAddress(),
			Coin:      common.NewCoin(synthAsset, cosmos.NewUint(3*common.One)),
		}
		synthVoter.Actions = []TxOutItem{synthTxOutItem}

		c.Assert(mgr.Keeper().SetStagedOutbound(ctx, synthTxOutItem, ctx.BlockHeight()+20), IsNil)
		mgr.Keeper().SetObservedTxInVoter(ctx, synthVoter)
		c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthTxOutItem.Coin), IsNil)
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthTxOutItem.Coin)), IsNil)

		asgardBalance := mgr.Keeper().GetBalance(ctx, mgr.Keeper().GetModuleAccAddress(AsgardName))
		c.Check(asgardBalance.AmountOf(synthAsset.Native()).Equal(cosmos.NewIntFromBigInt(synthTxOutItem.Coin.Amount.BigInt())), Equals, true,
			Commentf("synth rollback test should stage the synth balance in Asgard before rollback"))

		err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, synthVoter)
		c.Assert(err, IsNil, Commentf("Should handle synth output via inline rollback"))

		updatedSynthVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, synthTxID)
		c.Assert(err, IsNil)
		c.Check(updatedSynthVoter.Reverted, Equals, false, Commentf("Synth rollback should normalize the voter"))
		c.Check(updatedSynthVoter.Actions, IsNil, Commentf("Synth rollback should clear speculative actions"))

		synthReverseTxID := CreateMempoolRollbackTxID(synthTxID, 0)
		_, err = mgr.Keeper().GetSwapQueueItem(ctx, synthReverseTxID, 0)
		c.Assert(err, NotNil, Commentf("Synth rollback should settle inline without leaving a queued reverse swap"))

		stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, synthTxID)
		c.Assert(err, IsNil)
		c.Check(stagedItems, HasLen, 0, Commentf("Synth rollback should delete staged outbounds"))

		asgardBalance = mgr.Keeper().GetBalance(ctx, mgr.Keeper().GetModuleAccAddress(AsgardName))
		c.Check(asgardBalance.AmountOf(synthAsset.Native()).IsZero(), Equals, true,
			Commentf("Synth rollback should consume the staged synth from Asgard"))
	}
}

// TestRevertMempoolSwapNativeRuneEmitsPoolBalanceChanged verifies native-RUNE
// rollback reports the rune-side pool mutation in the emitted pool-balance
// event, not just the source-asset deduction.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapNativeRuneEmitsPoolBalanceChanged(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.TxOutDelayMax.String(), 17280)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	runeTxID := GetRandomTxHash()
	runeObservedTx := ObservedTx{
		Tx: common.Tx{
			ID:    runeTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		},
	}
	runeVoter := NewObservedTxVoter(runeTxID, ObservedTxs{runeObservedTx})
	runeVoter.Tx = runeObservedTx
	runeVoter.UnfinalizedHeight = ctx.BlockHeight()

	nativeTxOutItem := TxOutItem{
		Chain:     common.THORChain,
		InHash:    runeTxID,
		ToAddress: GetRandomRUNEAddress(),
		Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
	}
	runeVoter.Actions = []TxOutItem{nativeTxOutItem}
	mgr.Keeper().SetObservedTxInVoter(ctx, runeVoter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeTxOutItem, ctx.BlockHeight()+15), IsNil)

	err := rollbackMempoolSwapCached(ctx, mgr.Keeper(), mgr, runeVoter)
	c.Assert(err, IsNil)

	found := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != types.PoolBalanceChangeEventType {
			continue
		}

		attrs := make(map[string]string, len(evt.Attributes))
		for _, attr := range evt.Attributes {
			attrs[attr.Key] = attr.Value
		}
		if attrs["reason"] != "mempool swap rollback" {
			continue
		}

		found = true
		c.Check(attrs["asset"], Equals, common.BTCAsset.String())
		c.Check(attrs["rune_amt"], Equals, cosmos.NewUint(5*common.One).String())
		c.Check(attrs["rune_add"], Equals, "true")
		c.Check(attrs["asset_amt"], Equals, cosmos.NewUint(1*common.One).String())
		c.Check(attrs["asset_add"], Equals, "false")
	}
	c.Assert(found, Equals, true)
}

// TestRevertMempoolSwapRollbackOnError verifies that a rollback failure keeps
// the original pools, staged outbounds, voter state, and mempool usage intact.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapRollbackOnError(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	// Use a keeper that fails during the rollback flow so we can check atomic rollback.
	failKeeper := SettleSwapPostSwapSetPoolFailKeeper{Keeper: mgr.Keeper()}
	dummyMgr := NewDummyMgrWithKeeper(failKeeper)

	// Seed real pool, vault, voter, and staged-outbound state for the rollback path.
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
	c.Assert(mgr.Keeper().AddPendingAffiliateLiquidityFee(ctx, inTxID, 0, "PENDING", cosmos.NewUint(77)), IsNil)
	c.Assert(mgr.Keeper().AddAffiliateLiquidityFee(ctx, "EXISTING", cosmos.NewUint(99)), IsNil)

	// The rollback should fail and leave all original state unchanged.
	err := rollbackMempoolSwapAtomic(ctx, failKeeper, dummyMgr, voter)
	c.Assert(err, NotNil)

	// Verify that no part of the failed rollback leaked out of the cache context.
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
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, true,
		Commentf("failed rollback must retain pending REVSHARE for retry"))
	committedAffiliateFees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(committedAffiliateFees, HasLen, 1)
	c.Check(committedAffiliateFees[0].Thorname, Equals, "EXISTING")
	c.Check(committedAffiliateFees[0].Fee.Equal(cosmos.NewUint(99)), Equals, true,
		Commentf("failed rollback must not leak pending REVSHARE into the payout accumulator"))
}

// TestMempoolSwapWithMimirEnabled verifies the runtime gate and capacity check
// when mempool swaps are enabled.
func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirEnabled(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	// Enable mempool swaps
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	// Create pools (both source and target pools must exist for pool-relative cap)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(2000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

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

	limitMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), "=<:THOR.RUNE:"+GetRandomTHORAddress().String()+":1/30/2")
	c.Assert(err, IsNil)
	canProcessMempoolSwap = canUseMempoolSwap(
		ctx,
		mgr,
		common.Tx{Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}},
		limitMemo,
	)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("streaming limit swaps should also be admitted by the runtime mempool gate"))

	torValue, hasCapacity, err := checkMempoolSwapCapacity(ctx, mgr.Keeper(), common.BTCAsset, common.ETHAsset, cosmos.NewUint(common.One))
	c.Assert(err, IsNil)
	c.Check(hasCapacity, Equals, true, Commentf("eligible swap should have capacity with a large cap"))
	c.Check(torValue.GT(cosmos.ZeroUint()), Equals, true, Commentf("capacity check should return a TOR value for the inbound"))
}

// TestMempoolSwapWithMimirDisabled verifies that policy eligibility remains the
// same while runtime capacity correctly reports mempool swaps as disabled.
func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirDisabled(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	// Disable mempool swaps (cap = 0)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 0)

	// Policy eligibility is independent of the runtime cap; only checkMempoolSwapCapacity reflects the disabled state.
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), "=:ETH.ETH:"+GetRandomETHAddress().String()+":0/1/1")
	c.Assert(err, IsNil)
	canProcessMempoolSwap := canUseMempoolSwap(
		ctx,
		mgr,
		common.Tx{Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}},
		memo,
	)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("policy eligibility should not depend on the runtime cap setting"))

	limitMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), "=<:THOR.RUNE:"+GetRandomTHORAddress().String()+":1/30/2")
	c.Assert(err, IsNil)
	canProcessMempoolSwap = canUseMempoolSwap(
		ctx,
		mgr,
		common.Tx{Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}},
		limitMemo,
	)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("streaming limit swap policy eligibility should not depend on the runtime cap setting"))

	torValue, hasCapacity, err := checkMempoolSwapCapacity(ctx, mgr.Keeper(), common.BTCAsset, common.RuneAsset(), cosmos.NewUint(common.One))
	c.Assert(err, IsNil)
	c.Check(hasCapacity, Equals, false, Commentf("disabled mempool swaps should report no runtime capacity"))
	c.Check(torValue.IsZero(), Equals, true, Commentf("disabled mempool swaps should not return a TOR value"))
}

// TestSecuredTargetMempoolSwapStagesUnderlyingAndRollsBack verifies that a
// secured target stages the layer1 asset and uses that same underlying asset
// when the speculative swap is rolled back inline.
func (s *HandlerObservedTxHelpersMempoolSuite) TestSecuredTargetMempoolSwapStagesUnderlyingAndRollsBack(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
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

// TestSecuredTargetAdvQueuePartialRollbackExecutesInlineRollbackSwap verifies
// that partial advanced-queue output is recovered from queue state and reversed
// inline before any secured outbound has been staged.
func (s *HandlerObservedTxHelpersMempoolSuite) TestSecuredTargetAdvQueuePartialRollbackExecutesInlineRollbackSwap(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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
	// Only the executed slice of the deposit has reached the pool so far, so the
	// rollback must unwind State.In rather than the full deposit.
	c.Check(partialSwap.State.In.GT(cosmos.ZeroUint()), Equals, true, Commentf("partial advanced-queue execution should credit part of the deposit"))
	c.Check(partialSwap.State.In.LT(partialSwap.State.Deposit), Equals, true, Commentf("stream must still be mid-flight for this to be a partial rollback"))

	poolBeforeRollback, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

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

	// The inline reverse swap emits source asset from the pool and the DONATE
	// post-swap memo puts exactly that back, so the only lasting asset change is
	// the removal of the phantom credit. Deducting the deposit instead of the
	// executed State.In would silently strip the pool of asset it never held.
	sourcePoolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(sourcePoolAfter.BalanceAsset.Equal(common.SafeSub(poolBeforeRollback.BalanceAsset, partialSwap.State.In)), Equals, true,
		Commentf("partial rollback should deduct only the credited State.In (%s), got balance %s from %s",
			partialSwap.State.In, sourcePoolAfter.BalanceAsset, poolBeforeRollback.BalanceAsset))
	c.Check(sourcePoolAfter.BalanceAsset.Equal(sourcePool.BalanceAsset), Equals, true,
		Commentf("partial rollback should restore the source pool asset balance to its pre-swap value"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestSameUnderlyingSecuredTargetRollbackUnwindsWithoutSwap(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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
		fmt.Sprintf("SWAP:%s:%s:0/1/1", common.BTCAsset.GetSecuredAsset(), destination),
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
	// Only the executed slice of the deposit has reached the pool so far, so the
	// rollback must unwind State.In rather than the full deposit.
	c.Check(partialSwap.State.In.GT(cosmos.ZeroUint()), Equals, true, Commentf("partial advanced-queue execution should credit part of the deposit"))
	c.Check(partialSwap.State.In.LT(partialSwap.State.Deposit), Equals, true, Commentf("stream must still be mid-flight for this to be a partial rollback"))

	poolBeforeRollback, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	partialVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("partial advanced-queue execution should not stage the secured outbound yet"))
	pendingAction, err := buildPendingMempoolRollbackAction(ctx, mgr.Keeper(), partialVoter)
	c.Assert(err, IsNil)
	c.Assert(pendingAction, NotNil)
	// Escrow denominates a secured target's output in its underlying asset, so
	// for a same-underlying swap the recovered output IS the source asset. A
	// reverse swap would be source-to-source and MsgSwap.ValidateBasic rejects
	// it, which is why this class unwinds through direct pool accounting.
	c.Check(pendingAction.Coin.Asset.Equals(common.BTCAsset), Equals, true,
		Commentf("same-underlying secured output should be recovered as the source asset"))

	// Roll back the partial mempool execution.
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, partialVoter)
	c.Assert(err, IsNil, Commentf("same-asset rollback must not abort on a source-to-source swap"))

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, NotNil, Commentf("partial rollback should remove the pending advanced-queue swap"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("partial rollback should release mempool swap capacity"))
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.BTCAsset, owner).IsZero(), Equals, true, Commentf("partial rollback should not credit the secured balance"))
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("same-asset rollback should not queue a reverse swap"))

	// The round-trip invariant: the forward flow credited State.In into the
	// pool and took State.Out back out to escrow, so returning the escrowed
	// output and removing the phantom credit must land exactly on the starting
	// balance. This is the check that catches an over- or under-refund.
	sourcePoolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(sourcePoolAfter.BalanceAsset.Equal(sourcePool.BalanceAsset), Equals, true,
		Commentf("same-asset rollback should restore the source pool asset balance to its pre-swap value: got %s want %s (before rollback %s, state_in %s, state_out %s)",
			sourcePoolAfter.BalanceAsset, sourcePool.BalanceAsset, poolBeforeRollback.BalanceAsset, partialSwap.State.In, partialSwap.State.Out))
	c.Check(sourcePoolAfter.BalanceAsset.Equal(common.SafeSub(poolBeforeRollback.BalanceAsset.Add(partialSwap.State.Out), partialSwap.State.In)), Equals, true,
		Commentf("same-asset rollback should return the escrowed output and deduct only the credited State.In"))
}

// TestCompletedPartialMempoolRollbackUsesStagedRemainder verifies the
// queue-removed partial-settlement case. The staged source refund represents
// input that never entered the pool, so rollback must neither deduct nor
// reverse-swap it.
func (s *HandlerObservedTxHelpersMempoolSuite) TestCompletedPartialMempoolRollbackUsesStagedRemainder(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	deposit := cosmos.NewUint(100 * common.One)
	credited := cosmos.NewUint(40 * common.One)
	remainder := common.SafeSub(deposit, credited)
	runeOutput := cosmos.NewUint(10 * common.One)
	reservedTOR := cosmos.NewUint(5 * common.One)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One).Sub(runeOutput)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One).Add(credited)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.Tx = ObservedTx{Tx: common.Tx{
		ID:          inTxID,
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		Coins:       common.Coins{common.NewCoin(common.BTCAsset, deposit)},
	}}
	voter.MempoolSwapTOR = reservedTOR

	swapOutput := TxOutItem{
		Chain:     common.THORChain,
		InHash:    inTxID,
		ToAddress: GetRandomTHORAddress(),
		Coin:      common.NewCoin(common.RuneAsset(), runeOutput),
	}
	sourceRefund := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inTxID,
		ToAddress:   voter.Tx.Tx.FromAddress,
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, remainder),
		Memo:        NewRefundMemo(inTxID).String(),
	}
	voter.Actions = []TxOutItem{swapOutput, sourceRefund}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, swapOutput, ctx.BlockHeight()+10), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, sourceRefund, ctx.BlockHeight()+10), IsNil)

	// Settlement has removed the advanced-queue item, leaving only staged
	// output and remainder records to describe the completed partial fill.
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false)
	c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter), IsNil)

	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceRune.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
	c.Check(poolAfter.BalanceAsset.Equal(cosmos.NewUint(1000*common.One)), Equals, true,
		Commentf("rollback should deduct credited input %s, not deposit %s", credited, deposit))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	staged, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(staged, HasLen, 0)
}

// TestRefundOnlyStagedMempoolRollbackClearsStagedRecords covers a swap that
// settled with no executed output, so the only staged record is the unconsumed
// source refund. Refunds are filtered out of the rollback action list, which
// sends this through the zero-action branch. That branch still has to delete
// the staged record, or it outlives the voter that owned it and a later
// finalization pass can pay it out a second time.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRefundOnlyStagedMempoolRollbackClearsStagedRecords(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	deposit := cosmos.NewUint(100 * common.One)
	reservedTOR := cosmos.NewUint(5 * common.One)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.Tx = ObservedTx{Tx: common.Tx{
		ID:          inTxID,
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		Coins:       common.Coins{common.NewCoin(common.BTCAsset, deposit)},
	}}
	voter.MempoolSwapTOR = reservedTOR

	// Nothing executed, so the entire deposit is staged back as a refund and
	// there is no swap output alongside it.
	sourceRefund := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inTxID,
		ToAddress:   voter.Tx.Tx.FromAddress,
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, deposit),
		Memo:        NewRefundMemo(inTxID).String(),
	}
	voter.Actions = []TxOutItem{sourceRefund}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, sourceRefund, ctx.BlockHeight()+10), IsNil)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, true)

	c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter), IsNil)

	staged, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(staged, HasLen, 0,
		Commentf("refund-only staged records must not survive rollback"))
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	voterAfter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(voterAfter.HasReservedMempoolSwapTOR(), Equals, false)
}

// TestEmptyRollbackDeductsMidFlightCreditedSource covers the fund-loss gap where
// a mid-flight streaming swap credited an executed slice into the pool (State.In)
// but its output never reached a staged outbound (State.Out == 0). That leaves
// actionsToRollback empty, so the rollback takes the early-return branch. The
// speculatively credited State.In must still be removed, or the pool is left
// overstated by State.In (SafeSub-masked). It must NOT deduct when no advanced
// queue item exists (nothing executed => nothing credited), which the
// refund-only test above locks in.
func (s *HandlerObservedTxHelpersMempoolSuite) TestEmptyRollbackDeductsMidFlightCreditedSource(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)
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

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomTHORAddress()

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

	c.Assert(handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true), IsNil)

	// Force the "executed slice credited, output never staged" state: State.In > 0
	// (source credited) but State.Out == 0 (no output => buildPendingMempoolRollbackAction
	// returns nil => empty actionsToRollback branch).
	queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, IsNil)
	c.Assert(queued.State, NotNil)
	credited := cosmos.NewUint(40000000) // 0.4 of the 1 BTC deposit
	queued.State.Deposit = queued.Tx.Coins[0].Amount
	queued.State.In = credited
	queued.State.Out = cosmos.ZeroUint()
	queued.State.Count = 1
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queued), IsNil)

	// Reflect the speculative pool credit that execution of that slice would have made.
	poolCredited, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	baseBalance := poolCredited.BalanceAsset
	poolCredited.BalanceAsset = poolCredited.BalanceAsset.Add(credited)
	c.Assert(mgr.Keeper().SetPool(ctx, poolCredited), IsNil)

	staged, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(staged, HasLen, 0, Commentf("no output staged yet, so rollback must hit the empty branch"))

	rbVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(rollbackMempoolSwapCached(ctx, mgr.Keeper(), mgr, rbVoter), IsNil)

	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.Equal(baseBalance), Equals, true,
		Commentf("empty-branch rollback must remove the mid-flight credited State.In (%s): want %s got %s", credited, baseBalance, poolAfter.BalanceAsset))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false,
		Commentf("empty-branch rollback must clear the pending advanced-queue item"))
}

// TestEmptyRollbackDeductsConsumedAfterQueueItemRemoved covers the residual of
// the same class: a stream executed (crediting the consumed portion into the
// pool) and then had its advanced-queue item removed by settlement after its
// output failed to stage, leaving only an unconsumed source refund staged. The
// queue item is gone, so the deduction must fall back to deposit minus the
// staged refund (the consumed amount) rather than deduct nothing.
func (s *HandlerObservedTxHelpersMempoolSuite) TestEmptyRollbackDeductsConsumedAfterQueueItemRemoved(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

	deposit := cosmos.NewUint(100 * common.One)
	refund := cosmos.NewUint(60 * common.One)   // unconsumed input staged back to the user
	consumed := common.SafeSub(deposit, refund) // 40, credited into the pool by executed slices
	baseBalance := cosmos.NewUint(1000 * common.One)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = baseBalance.Add(consumed) // executed slices already credited `consumed`
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, ObservedTxs{})
	voter.Tx = ObservedTx{Tx: common.Tx{
		ID:          inTxID,
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		Coins:       common.Coins{common.NewCoin(common.BTCAsset, deposit)},
	}}
	voter.MempoolSwapTOR = cosmos.NewUint(5 * common.One)

	sourceRefund := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inTxID,
		ToAddress:   voter.Tx.Tx.FromAddress,
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, refund),
		Memo:        NewRefundMemo(inTxID).String(),
	}
	voter.Actions = []TxOutItem{sourceRefund}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, sourceRefund, ctx.BlockHeight()+10), IsNil)
	c.Assert(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false)

	c.Assert(rollbackMempoolSwapCached(ctx, mgr.Keeper(), mgr, voter), IsNil)

	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.Equal(baseBalance), Equals, true,
		Commentf("empty-branch rollback must remove the consumed credit (%s) even when the queue item is already gone: want %s got %s", consumed, baseBalance, poolAfter.BalanceAsset))
}

// TestFinalizedOrphanedMempoolSwapReleasesReservation covers the settled-but-
// orphaned finalized mempool swap: settlement ran while the inbound was
// unfinalized (removing the advanced-queue item and, for a failed output,
// donating it back to its pool + refunding the remainder) but could not release
// the TOR reservation because FinalisedHeight was still 0. Once the inbound
// finalizes with no queue item and no staged outbound, no index-driven sweep can
// find it. handleObservedTxInQuorum must release the reservation — the pool is
// already settled and the finalized deposit backs the credited source, so this
// is release-only (nothing to roll back or refund).
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedOrphanedMempoolSwapReleasesReservation(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

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
	finalizedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 12)

	// Orphan state: finalized + reservation held, but Actions empty, no advanced-
	// queue item, and no staged outbound (settlement removed the queue item while
	// the inbound was unfinalized).
	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(50*common.One))
	c.Assert(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false)
	c.Assert(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false)

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), finalizedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true,
		Commentf("orphaned finalized mempool swap must release the held TOR reservation"))
	updated, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updated.HasReservedMempoolSwapTOR(), Equals, false,
		Commentf("orphan recovery must clear the voter's reservation marker"))
}

// TestInactiveVaultSkipsMempoolSwapBeforeFinalization verifies that speculative
// mempool processing never starts when the source vault is inactive.
func (s *HandlerObservedTxHelpersMempoolSuite) TestInactiveVaultSkipsMempoolSwapBeforeFinalization(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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

// TestTradingHaltSkipsMempoolProcessingUntilFinalization verifies that a
// trading halt suppresses all speculative queueing, staging, and capacity use.
func (s *HandlerObservedTxHelpersMempoolSuite) TestTradingHaltSkipsMempoolProcessingUntilFinalization(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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

// TestInactiveVaultFinalizedMempoolSwapRevertsBeforeRefund verifies that once
// the inbound is finalized, the handler first unwinds speculative state and
// only then schedules the refund path.
func (s *HandlerObservedTxHelpersMempoolSuite) TestInactiveVaultFinalizedMempoolSwapRevertsBeforeRefund(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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

func (s *HandlerObservedTxHelpersMempoolSuite) TestObservedTxOutQuorumInactiveVaultTransitionRollsBackUnfinalizedMempoolSwaps(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

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

	retiringVault := NewVault(ctx.BlockHeight(), RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	retiringVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	retiringBTCAddress, err := retiringVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		retiringBTCAddress,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	observedInbound := NewObservedTx(inboundTx, ctx.BlockHeight(), retiringVault.PubKey, 0)
	observedInbound.Status = common.Status_done

	inVoter := NewObservedTxVoter(inTxID, ObservedTxs{observedInbound})
	inVoter.Tx = observedInbound
	inVoter.UnfinalizedHeight = ctx.BlockHeight()
	inVoter.FinalisedHeight = 0
	inVoter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)

	stagedOutbound := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(3*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}
	inVoter.Actions = []TxOutItem{stagedOutbound}

	mgr.Keeper().SetObservedTxInVoter(ctx, inVoter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, inVoter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedOutbound, ctx.BlockHeight()+10), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	retiringETHAddress, err := retiringVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	outTxID := GetRandomTxHash()
	outboundTx := common.NewTx(
		outTxID,
		retiringETHAddress,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{},
		NewOutboundMemo(GetRandomTxHash()).String(),
	)
	observedOutbound := NewObservedTx(outboundTx, ctx.BlockHeight(), retiringVault.PubKey, ctx.BlockHeight())
	observedOutbound.Status = common.Status_done

	outVoter := NewObservedTxVoter(outTxID, ObservedTxs{observedOutbound})
	outVoter.Tx = observedOutbound
	outVoter.Height = ctx.BlockHeight()
	outVoter.FinalisedHeight = ctx.BlockHeight()

	err = handleObservedTxOutQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), observedOutbound, outVoter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	updatedVault, err := mgr.Keeper().GetVault(ctx, retiringVault.PubKey)
	c.Assert(err, IsNil)
	c.Check(updatedVault.Status, Equals, InactiveVault)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("outbound-driven inactive-vault transition should clear staged mempool outbounds"))

	updatedInVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedInVoter.FinalisedHeight, Equals, int64(0), Commentf("rollback should not mark an unfinalized inbound as finalized"))
	c.Check(updatedInVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("outbound-driven inactive-vault rollback should clear the voter mempool TOR value"))
	c.Check(updatedInVoter.Actions, IsNil, Commentf("outbound-driven inactive-vault rollback should discard speculative actions"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("outbound-driven inactive-vault rollback should release mempool swap capacity"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 0, Commentf("unfinalized inactive-vault rollback should not schedule a refund"))
}

// TestInactiveVaultFinalizedNonMempoolStillRefundsWhenRollbackInspectionFails
// verifies that plain finalized inactive-vault inbounds keep the historical
// direct-refund behavior even if mempool rollback-state inspection is broken.
func (s *HandlerObservedTxHelpersMempoolSuite) TestInactiveVaultFinalizedNonMempoolStillRefundsWhenRollbackInspectionFails(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	na := GetRandomValidatorNode(NodeActive)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
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
	finalizedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 12)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	failKeeper := failGetStagedOutboundsKeeper{Keeper: mgr.Keeper()}
	mgr.K = failKeeper
	mgr.txOutStore = newTxOutStorage(failKeeper, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), finalizedTx, voter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("plain inactive-vault finalization should still schedule a refund even if rollback inspection fails"))
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
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(10_000_000 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

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

	activeVault := NewVault(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.BTCChain, common.THORChain}.Strings(),
		nil,
	)
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	destination := GetRandomTHORAddress()
	inboundMemo := fmt.Sprintf("=:%s:%s", common.ETHAsset.GetSyntheticAsset(), destination)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(200_000))},
		inboundMemo,
	)
	tx.Chain = common.BTCChain

	observedTx := ObservedTx{
		Tx:             tx,
		Status:         common.Status_done,
		BlockHeight:    ctx.BlockHeight(),
		ObservedPubKey: activeVault.PubKey,
	}
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)

	// Reproduce the real advanced-queue mempool path: execute the swap with
	// direct outbound scheduling suppressed, then materialize the emitted synth
	// through the shared staging logic. This keeps the minted synth in Asgard
	// while the outbound remains speculative and staged.
	msg := MsgSwap{
		Tx:                    tx,
		TargetAsset:           common.ETHAsset.GetSyntheticAsset(),
		Destination:           destination,
		TradeTarget:           cosmos.ZeroUint(),
		AffiliateBasisPoints:  cosmos.ZeroUint(),
		AggregatorTargetLimit: nil,
		Signer:                mgr.Keeper().GetModuleAccAddress(AsgardName),
		Version:               types.SwapVersion_v2,
	}
	err := executeSwapWithoutOutboundsCached(ctx, mgr, &msg)
	c.Assert(err, IsNil)
	c.Assert(msg.State, NotNil)
	synthAmount := msg.State.Out
	c.Assert(synthAmount.GT(cosmos.ZeroUint()), Equals, true)

	err = settleSwapResultCached(ctx, mgr, msg, "swap has been completed")
	c.Assert(err, IsNil)

	postSwapSourcePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	postSwapTargetPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	postSwapSynthSupply := mgr.Keeper().GetTotalSupply(ctx, common.ETHAsset.GetSyntheticAsset())
	c.Assert(postSwapSynthSupply.GT(cosmos.ZeroUint()), Equals, true)
	c.Assert(postSwapSourcePool.BalanceRune.Equal(sourcePool.BalanceRune), Equals, false)
	c.Assert(postSwapTargetPool.BalanceRune.Equal(targetPool.BalanceRune), Equals, false)
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(stagedItems, HasLen, 1)
	c.Check(stagedItems[0].TxOutItem.Coin.Equals(common.NewCoin(common.ETHAsset.GetSyntheticAsset(), synthAmount)), Equals, true)

	asgardBalance := mgr.Keeper().GetBalance(ctx, mgr.Keeper().GetModuleAccAddress(AsgardName))
	c.Check(asgardBalance.AmountOf(common.ETHAsset.GetSyntheticAsset().Native()).Equal(cosmos.NewIntFromBigInt(synthAmount.BigInt())), Equals, true,
		Commentf("real mempool staging should leave the minted synth in Asgard until finalization or rollback"))

	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("synth mempool rollback should normalize the voter"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("synth mempool rollback should clear speculative actions"))

	stagedItems, err = mgr.Keeper().GetStagedOutbounds(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("synth mempool rollback should delete staged outbounds"))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(tx.ID, 0), 0)
	c.Assert(err, NotNil, Commentf("synth mempool rollback should settle the reverse swap inline"))

	asgardBalance = mgr.Keeper().GetBalance(ctx, mgr.Keeper().GetModuleAccAddress(AsgardName))
	c.Check(asgardBalance.AmountOf(common.ETHAsset.GetSyntheticAsset().Native()).IsZero(), Equals, true,
		Commentf("rollback should consume the staged synth from Asgard"))

	updatedSourcePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	updatedTargetPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	expectedSourceBalanceRune := cosmos.NewUint(57571412921)
	expectedSourceBalanceAsset := cosmos.NewUint(3400000000)
	expectedTargetBalanceRune := cosmos.NewUint(111096736680)
	expectedTargetBalanceAsset := cosmos.NewUint(3400000000)
	c.Check(updatedSourcePool.BalanceRune.Equal(expectedSourceBalanceRune), Equals, true,
		Commentf("synth mempool rollback should set source pool rune balance to %s, got %s", expectedSourceBalanceRune, updatedSourcePool.BalanceRune))
	c.Check(updatedSourcePool.BalanceAsset.Equal(expectedSourceBalanceAsset), Equals, true,
		Commentf("synth mempool rollback should set source pool asset balance to %s, got %s", expectedSourceBalanceAsset, updatedSourcePool.BalanceAsset))
	c.Check(updatedTargetPool.BalanceRune.Equal(expectedTargetBalanceRune), Equals, true,
		Commentf("synth mempool rollback should set target pool rune balance to %s, got %s", expectedTargetBalanceRune, updatedTargetPool.BalanceRune))
	c.Check(updatedTargetPool.BalanceAsset.Equal(expectedTargetBalanceAsset), Equals, true,
		Commentf("synth mempool rollback should set target pool asset balance to %s, got %s", expectedTargetBalanceAsset, updatedTargetPool.BalanceAsset))
}

// TestRevertMempoolSwapSynthAffiliateTargetBuildsGrossRollbackSwap verifies
// that deferred affiliate handling does not shrink a staged synth amount before
// rollback, so the reverse swap still uses the full gross output.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapSynthAffiliateTargetBuildsGrossRollbackSwap(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, NotNil, Commentf("mempool staging should not create affiliate sidecars before finalization"))

	// Mirror the forward swap's synth mint path so rollback can consume the
	// staged synth from Asgard, just like it would in the real mempool flow.
	synthCoin := common.NewCoin(targetAsset, outboundAmount)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)

	// Snapshot the balances the rollback has to actually move. Asserting only
	// against expectedReverse would pass even if the executed rollback consumed
	// a smaller amount and stranded synths in Asgard.
	asgardSynthBefore := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, targetAsset.Native())
	synthSupplyBefore := mgr.Keeper().GetTotalSupply(ctx, targetAsset)
	c.Assert(asgardSynthBefore.Equal(outboundAmount), Equals, true)

	btcPoolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	// Roll back and make sure the gross amount is used.
	err = rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	// The executed rollback must have consumed the whole staged synth amount.
	asgardSynthAfter := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, targetAsset.Native())
	c.Check(asgardSynthAfter.IsZero(), Equals, true,
		Commentf("rollback should consume the full staged synth from Asgard, %s left behind", asgardSynthAfter))
	synthSupplyAfter := mgr.Keeper().GetTotalSupply(ctx, targetAsset)
	c.Check(common.SafeSub(synthSupplyBefore, synthSupplyAfter).Equal(outboundAmount), Equals, true,
		Commentf("rollback should burn the full gross synth amount: before %s after %s", synthSupplyBefore, synthSupplyAfter))

	// The recovered source asset is donated back into the BTC pool, and the
	// speculatively credited input is removed from it.
	btcPoolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(common.SafeSub(btcPoolBefore.BalanceAsset, btcPoolAfter.BalanceAsset).Equal(cosmos.NewUint(100*common.One)), Equals, true,
		Commentf("rollback should remove the speculatively credited BTC input: before %s after %s",
			btcPoolBefore.BalanceAsset, btcPoolAfter.BalanceAsset))
	c.Check(btcPoolAfter.BalanceRune.GT(btcPoolBefore.BalanceRune), Equals, true,
		Commentf("rollback should return the recovered synth value to the BTC pool: before %s after %s",
			btcPoolBefore.BalanceRune, btcPoolAfter.BalanceRune))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("synth affiliate rollback should normalize the voter"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("synth affiliate rollback should clear speculative actions"))

	c.Check(expectedReverse.Tx.Coins, DeepEquals, common.Coins{common.NewCoin(targetAsset, outboundAmount)},
		Commentf("rollback should reverse the full staged synth amount, not a fee-reduced amount"))
	c.Check(expectedReverse.TargetAsset.Equals(common.BTCAsset), Equals, true)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("rollback should settle the synth reverse swap inline"))

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 2)
	c.Assert(err, NotNil, Commentf("rollback should not leave deferred affiliate sidecars queued"))
}

// TestSelectActiveVaultForChainPrefersUsableSourceChainVault verifies that the
// rollback helper chooses an active vault that both advertises and can derive a
// usable address for the inbound's source chain.
func (s *HandlerObservedTxHelpersMempoolSuite) TestSelectActiveVaultForChainPrefersUsableSourceChainVault(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)

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
	selectedVault, err := selectActiveVaultForSourceChain(ctx, mgr.Keeper(), common.SOLChain)
	c.Assert(err, IsNil)
	c.Check(selectedVault.PubKey.Equals(supportedVault.PubKey), Equals, true)
	// The rollback swap should use that same vault address in the memo and destination.
	expectedReverse, err := buildMempoolRollbackSwapMsg(ctx, mgr, voter, common.SOLAsset, voter.Actions[0], 0, selectedVault)
	c.Assert(err, IsNil)

	c.Check(expectedReverse.Destination.Equals(supportedSOLAddress), Equals, true,
		Commentf("reverse swap should target the active vault that can receive the SOL source asset"))
	c.Check(expectedReverse.Tx.Memo, Equals, fmt.Sprintf("=:%s:%s", common.SOLAsset, supportedSOLAddress))
}

// TestTimeoutRevertedMempoolSwapFinalizesNormallyWithoutResume verifies that a
// timeout-style rollback only clears speculative state; a later confirmed
// inbound should proceed through ordinary finalized swap handling.
func (s *HandlerObservedTxHelpersMempoolSuite) TestTimeoutRevertedMempoolSwapFinalizesNormallyWithoutResume(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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
			Gas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))},
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

	// Then process the real finalized inbound and make sure normal finalized
	// handling re-queues the swap toward the original ETH destination.
	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, nas, NewInternalHandler(mgr), finalizedTx, updatedVoter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)
	queuedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, IsNil)
	c.Check(queuedSwap.TargetAsset.Equals(common.ETHAsset), Equals, true,
		Commentf("confirmed inbound should re-queue a BTC->ETH swap after timeout rollback"))
	c.Check(queuedSwap.Destination.Equals(ethDestination), Equals, true,
		Commentf("confirmed inbound should preserve the memo destination when it re-queues after timeout rollback"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestErrataRevertedMempoolSwapRollbackPreservesReverted(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))

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

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings(), nil)
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

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
			Gas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))},
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
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	voter.Actions = []TxOutItem{{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
	}}
	voter.SetReverted()

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter.Actions[0], ctx.BlockHeight()+5), IsNil)

	err := rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, true, Commentf("rollback must preserve errata-reverted state"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("rollback should still clear mempool TOR bookkeeping"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("rollback should still discard speculative actions"))

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("rollback should clear staged outbounds for errata-reverted mempool swaps"))

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

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, nas, NewInternalHandler(mgr), finalizedTx, updatedVoter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, NotNil, Commentf("errata-reverted inbound must not be re-queued as a fresh finalized swap"))

	finalVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(finalVoter.Reverted, Equals, true, Commentf("finalized observation should leave errata-reverted state intact"))
}

// TestRevertMempoolSwapAggregatesSplitExternalOutboundsByAsset verifies that
// rollback collapses same-asset external pieces into one reverse action before
// settling inline.
func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapAggregatesSplitExternalOutboundsByAsset(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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

// TestFinalizedAdvQueueSettlementReleasesMempoolSwapCapacity verifies that
// advanced-queue settlement keeps the reservation during partial execution and
// releases it only once the inbound is finalized and the swap completes.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedAdvQueueSettlementReleasesMempoolSwapCapacity(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
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
	pendingAffiliateFee := cosmos.NewUint(77)
	c.Assert(mgr.Keeper().AddPendingAffiliateLiquidityFee(ctx, inTxID, 0, "AFFX", pendingAffiliateFee), IsNil)

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
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, false,
		Commentf("finalized stream settlement must not release capacity with pending REVSHARE"))
	committedAffiliateFees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(committedAffiliateFees, HasLen, 1)
	c.Check(committedAffiliateFees[0].Thorname, Equals, "AFFX")
	c.Check(committedAffiliateFees[0].Fee.Equal(pendingAffiliateFee), Equals, true)

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("finalized settlement should queue the outbound immediately"))
	c.Check(outItems[0].InHash.Equals(inTxID), Equals, true)
}

// TestUnfinalizedDustSettlementKeepsRollbackRecord verifies that a completed
// mempool swap whose gross output is entirely consumed by the outbound fee
// leaves a staged owner until finality. Before this guard, settlement donated
// the output, deleted the advanced-queue item, and left the TOR reservation
// with no index that timeout recovery could discover.
func (s *HandlerObservedTxHelpersMempoolSuite) TestUnfinalizedDustSettlementKeepsRollbackRecord(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.LPUnits = pool.BalanceRune
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	outboundFee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.ETHAsset, false)
	c.Assert(err, IsNil)
	c.Assert(outboundFee.IsZero(), Equals, false)

	toAddr, err := activeVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	destination := GetRandomETHAddress()
	inTxID := GetRandomTxHash()
	inboundAmount := cosmos.NewUint(1 * common.One)
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, inboundAmount)},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+destination.String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, activeVault.PubKey, 15)

	reservedTOR := cosmos.NewUint(50 * common.One)
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = reservedTOR
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)

	msg := NewMsgSwap(
		inboundTx,
		common.ETHAsset,
		destination,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types.SwapVersion_v2,
		na.NodeAddress,
	)
	msg.State.In = inboundAmount
	msg.State.Out = outboundFee
	msg.State.Count = 1
	msg.State.LastHeight = ctx.BlockHeight()
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *msg), IsNil)

	targetPoolBeforeSettlement, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(settleSwap(ctx, mgr, *msg, "completed"), IsNil)

	// Terminal settlement may remove its queue item only because the gross dust
	// output now provides the durable rollback/finalization record.
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false)
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(stagedItems, HasLen, 1)
	c.Check(stagedItems[0].TxOutItem.Coin.Amount.Equal(outboundFee), Equals, true)
	c.Check(stagedItems[0].StagedHeight, Equals, ctx.BlockHeight(),
		Commentf("dust output must carry the timeout-recovery clock"))

	settledVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(settledVoter.Actions, HasLen, 1)
	c.Check(settledVoter.Actions[0].Coin.Amount.Equal(outboundFee), Equals, true)
	c.Check(settledVoter.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(reservedTOR), Equals, true)

	cleanupReason := "failed_outbound"
	cleanupKey := stagedRefundCleanupKey(stagedItems[0].TxOutItem, cleanupReason, 0)
	c.Check(mgr.Keeper().HasRefundCleanup(ctx, cleanupKey), Equals, false,
		Commentf("unrefundable cleanup must wait for inbound finality"))
	targetPoolAfterSettlement, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(targetPoolAfterSettlement, DeepEquals, targetPoolBeforeSettlement,
		Commentf("speculative dust settlement must not donate the output early"))
	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 0)

	// Once the inbound finalizes, the gross staged coin is cleaned up exactly
	// once, the staged owner is deleted, and TOR capacity is released.
	settledVoter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, settledVoter)

	// Cleanup is part of finalization's atomic transition. If its pool write
	// fails, the staged owner and both TOR markers must remain available for a
	// later retry.
	baseKeeper := mgr.Keeper()
	mgr.K = SettleSwapPostSwapSetPoolFailKeeper{Keeper: baseKeeper}
	finalizeErr := finalizeOutboundsAtomic(ctx, mgr, settledVoter)
	c.Assert(finalizeErr, ErrorMatches, ".*fail to clean up unrefundable finalized staged outbound.*")
	mgr.K = baseKeeper

	failedPool, err := baseKeeper.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(failedPool, DeepEquals, targetPoolBeforeSettlement)
	failedStaged, err := baseKeeper.GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(failedStaged, HasLen, 1)
	failedVoter, err := baseKeeper.GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(failedVoter.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(failedVoter.Actions, HasLen, 1)
	c.Check(baseKeeper.GetMempoolSwapUsageTOR(ctx).Equal(reservedTOR), Equals, true)
	c.Check(baseKeeper.HasRefundCleanup(ctx, cleanupKey), Equals, false)

	c.Assert(finalizeOutboundsAtomic(ctx, mgr, settledVoter), IsNil)

	finalPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.BalanceAsset.Equal(targetPoolBeforeSettlement.BalanceAsset.Add(outboundFee)), Equals, true,
		Commentf("finalized dust output must be donated back to its pool exactly once"))
	c.Check(finalPool.BalanceRune.Equal(targetPoolBeforeSettlement.BalanceRune), Equals, true)
	c.Check(mgr.Keeper().HasRefundCleanup(ctx, cleanupKey), Equals, true)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	finalVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(finalVoter.HasReservedMempoolSwapTOR(), Equals, false)
	c.Check(finalVoter.Actions, HasLen, 0)
	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 0)

	// A replay after successful finalization is a no-op.
	c.Assert(finalizeOutboundsAtomic(ctx, mgr, finalVoter), IsNil)
	replayedPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(replayedPool, DeepEquals, finalPool)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

// TestMarkerlessSettlementFallsBackToAtomicRollback covers terminal settlement
// paths that cannot create even a gross staged output. The settlement cache is
// discarded and rollback runs against the still-live advanced-queue state; if
// rollback fails, both ownership records and all speculative state remain.
func (s *HandlerObservedTxHelpersMempoolSuite) TestMarkerlessSettlementFallsBackToAtomicRollback(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

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

	inTxID := GetRandomTxHash()
	inboundAmount := cosmos.NewUint(common.One)
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, inboundAmount)},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	observedTx := NewObservedTx(inboundTx, 12, GetRandomPubKey(), 15)
	reservedTOR := cosmos.NewUint(50 * common.One)
	voter := NewObservedTxVoter(inTxID, ObservedTxs{observedTx})
	voter.Tx = observedTx
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = reservedTOR
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)

	msg := NewMsgSwap(
		inboundTx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types.SwapVersion_v2,
		na.NodeAddress,
	)
	msg.State.In = inboundAmount
	msg.State.Out = cosmos.ZeroUint()
	msg.State.Count = 1
	msg.State.LastHeight = ctx.BlockHeight()
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *msg), IsNil)

	// Model the source-asset credit already applied by speculative swap math.
	creditedPool := sourcePool
	creditedPool.BalanceAsset = creditedPool.BalanceAsset.Add(inboundAmount)
	c.Assert(mgr.Keeper().SetPool(ctx, creditedPool), IsNil)

	baseKeeper := mgr.Keeper()
	mgr.K = SettleSwapPostSwapSetPoolFailKeeper{Keeper: baseKeeper}
	err := settleSwap(ctx, mgr, *msg, "completed")
	c.Assert(err, ErrorMatches, ".*fail to roll back markerless unfinalized mempool settlement.*")
	mgr.K = baseKeeper

	// Neither the abandoned settlement cache nor the failed rollback cache may
	// leak. The original queue and reservation still own the speculative credit.
	c.Check(baseKeeper.HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, true)
	c.Check(baseKeeper.HasStagedOutbound(ctx, inTxID), Equals, false)
	failedVoter, err := baseKeeper.GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(failedVoter.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(baseKeeper.GetMempoolSwapUsageTOR(ctx).Equal(reservedTOR), Equals, true)
	failedPool, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(failedPool, DeepEquals, creditedPool)

	// A healthy retry atomically removes the phantom source credit and clears
	// every ownership/capacity marker.
	c.Assert(settleSwap(ctx, mgr, *msg, "completed"), IsNil)
	c.Check(baseKeeper.HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false)
	c.Check(baseKeeper.HasStagedOutbound(ctx, inTxID), Equals, false)
	rolledBackVoter, err := baseKeeper.GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(rolledBackVoter.HasReservedMempoolSwapTOR(), Equals, false)
	c.Check(rolledBackVoter.Actions, HasLen, 0)
	c.Check(baseKeeper.GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	rolledBackPool, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolledBackPool, DeepEquals, sourcePool)
}

// TestFinalizedPendingQueueMempoolSwapReleasesCapacityAndSkipsRefundChecks
// verifies that a finalized mempool swap with only queue-backed work releases
// its speculative capacity without running the legacy finalized refund path or
// removing the advanced-queue item.
func (s *HandlerObservedTxHelpersMempoolSuite) TestFinalizedPendingQueueMempoolSwapReleasesCapacityAndSkipsRefundChecks(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)
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
	mgr.Keeper().SetTHORName(ctx, NewTHORName(
		"affx",
		ctx.BlockHeight()+1000,
		[]THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}},
	))

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+destination.String()+"::affx:100",
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

	// Run one partial step so the finalized inbound still has only queue-backed
	// mempool state and no staged Actions yet.
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	partialVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(partialVoter.HasReservedMempoolSwapTOR(), Equals, true, Commentf("partial execution should still hold mempool swap capacity"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(partialVoter.MempoolSwapTOR), Equals, true,
		Commentf("the global usage precondition should contain this swap's reservation"))
	c.Check(partialVoter.Actions, HasLen, 0, Commentf("partial execution should not have scheduled finalized actions yet"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, true, Commentf("partial execution should leave the advanced queue item live"))
	pendingAffiliateFees, err := mgr.Keeper().GetPendingAffiliateLiquidityFees(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(pendingAffiliateFees, HasLen, 1)
	pendingAffiliateFee := pendingAffiliateFees[0].Fee
	c.Check(pendingAffiliateFee.IsZero(), Equals, false,
		Commentf("the speculative stream slice should journal its realized REVSHARE basis"))
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0,
		Commentf("speculative REVSHARE must stay outside the payout accumulator before finality"))

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	mgr.Keeper().SetMimir(ctx, "HaltTrading", 1)

	finalizedTx := NewObservedTx(inboundTx, 15, activeVault.PubKey, 15)
	partialVoter.Tx = finalizedTx
	partialVoter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, partialVoter)

	err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, NewInternalHandler(mgr), finalizedTx, partialVoter, []cosmos.AccAddress{na.NodeAddress}, true)
	c.Assert(err, IsNil)

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false,
		Commentf("inbound finality should release the queue-backed mempool reservation"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true,
		Commentf("inbound finality should release global mempool swap usage"))
	c.Check(updatedVoter.Actions, HasLen, 0, Commentf("finalized queue-backed mempool swap should not fabricate finalized actions"))
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, true, Commentf("handler should leave the advanced queue item for later settlement"))
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, false,
		Commentf("inbound finality must commit pending REVSHARE even while the stream remains queue-backed"))
	committedAffiliateFees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(committedAffiliateFees, HasLen, 1)
	c.Check(committedAffiliateFees[0].Thorname, Equals, "AFFX")
	c.Check(committedAffiliateFees[0].Fee.Equal(pendingAffiliateFee), Equals, true)

	// Replaying the finality-side drain cannot credit the already-consumed
	// pending record a second time.
	c.Assert(commitPendingAffiliateLiquidityFeesAtomic(ctx, mgr.Keeper(), inTxID), IsNil)
	replayedAffiliateFees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(replayedAffiliateFees, HasLen, 1)
	c.Check(replayedAffiliateFees[0].Fee.Equal(pendingAffiliateFee), Equals, true)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("queue-backed finalized mempool swap should not create staged outbounds"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 0, Commentf("queue-backed finalized mempool swap should not schedule a refund while queue state is still live"))

	// Clearing the capacity marker must not disturb ordinary post-finality queue
	// settlement. Resume trading and finish the remaining stream slice.
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	mgr.Keeper().SetMimir(ctx, "HaltTrading", 0)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, inTxID, 0), Equals, false,
		Commentf("the finalized queue item should settle normally after capacity release"))
	settledVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(settledVoter.HasReservedMempoolSwapTOR(), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().HasPendingAffiliateLiquidityFees(ctx, inTxID), Equals, false)
	outItems, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outItems, HasLen, 1,
		Commentf("post-finality settlement should materialize its outbound normally"))
}
