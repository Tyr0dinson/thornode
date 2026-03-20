package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerObservedTxHelpersMempoolSuite struct{}

var _ = Suite(&HandlerObservedTxHelpersMempoolSuite{})

type failSwapQueueKeeper struct {
	keeper.Keeper
}

func (k failSwapQueueKeeper) SetSwapQueueItem(_ cosmos.Context, _ MsgSwap, _ int) error {
	return errKaboom
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

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)
	mgr.Keeper().SetMimir(ctx, constants.TxOutDelayMax.String(), 17280) // Set max delay for searching

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

	// Test 1: Successfully revert mempool swap
	err := revertMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter)
	c.Assert(err, IsNil, Commentf("revertMempoolSwap failed: %v", err))

	// Verify voter is marked as reverted
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, true, Commentf("Voter should be marked as reverted"))

	// Verify staged outbounds are deleted
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(len(stagedItems), Equals, 0, Commentf("Staged outbounds should be deleted after revert"))

	// Verify a reverse swap was queued
	swapIter := mgr.Keeper().GetSwapQueueIterator(ctx)
	defer func() { _ = swapIter.Close() }()
	c.Check(swapIter.Valid(), Equals, true, Commentf("Reverse swap should be queued"))

	reverseTxID := CreateMempoolRevertTxID(inTxID, 0)
	reverseSwap, err := mgr.Keeper().GetSwapQueueItem(ctx, reverseTxID, 0)
	c.Assert(err, IsNil)

	destAddress, err := vault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(reverseSwap.Tx.Memo, Equals, fmt.Sprintf("=:%s:%s", common.BTCAsset, destAddress))
	c.Check(reverseSwap.PostSwapMemo, Equals, fmt.Sprintf("DONATE:%s", common.BTCAsset.GetLayer1Asset()))

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

	err = revertMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter2)
	c.Assert(err, IsNil, Commentf("Should succeed with no staged outbounds"))

	// Verify voter2 is marked as reverted
	updatedVoter2, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter2TxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter2.Reverted, Equals, true, Commentf("Voter2 should be marked as reverted"))

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

	err = revertMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, runeVoter)
	c.Assert(err, IsNil, Commentf("Should handle native (RUNE) output"))

	// Verify voter is marked as reverted
	updatedRuneVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, runeTxID)
	c.Assert(err, IsNil)
	c.Check(updatedRuneVoter.Reverted, Equals, true, Commentf("RUNE voter should be marked as reverted"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestRevertMempoolSwapRollbackOnError(c *C) {
	ctx, mgr := setupManagerForTest(c)
	failKeeper := failSwapQueueKeeper{Keeper: mgr.Keeper()}
	dummyMgr := NewDummyMgrWithKeeper(failKeeper)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

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

	err := revertMempoolSwapAtomic(ctx, failKeeper, dummyMgr, voter)
	c.Assert(err, NotNil)

	usage := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)
	c.Check(usage.Equal(cosmos.NewUint(700*common.One)), Equals, true, Commentf("mempool usage should roll back on revert failure"))

	stagedItems, stagedErr := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(stagedErr, IsNil)
	c.Check(stagedItems, HasLen, 2, Commentf("staged outbounds should remain after revert failure"))

	updatedVoter, voterErr := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(voterErr, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("voter should not be marked reverted when revert fails"))

	updatedPool, poolErr := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(poolErr, IsNil)
	c.Check(updatedPool.BalanceRune.Equal(sourcePool.BalanceRune), Equals, true, Commentf("pool rune balance should roll back on revert failure"))
	c.Check(updatedPool.BalanceAsset.Equal(sourcePool.BalanceAsset), Equals, true, Commentf("pool asset balance should roll back on revert failure"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirEnabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Enable mempool swaps
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// Create pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Check that mempool swap cap is enabled
	mempoolSwapCap := cosmos.NewUint(uint64(mgr.Keeper().GetConfigInt64(ctx, constants.MempoolSwapCapTOR)))
	c.Check(mempoolSwapCap.IsZero(), Equals, false, Commentf("Mempool swaps should be enabled with non-zero cap"))

	// Verify swap memo is recognized as processable before finalization
	memo, _ := ParseMemo(common.LatestVersion, "=:ETH.ETH:"+GetRandomETHAddress().String())
	canProcessMempoolSwap := !mempoolSwapCap.IsZero() && memo.IsType(TxSwap)
	c.Check(canProcessMempoolSwap, Equals, true, Commentf("Should be able to process mempool swap"))
}

func (s *HandlerObservedTxHelpersMempoolSuite) TestMempoolSwapWithMimirDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Disable mempool swaps (cap = 0)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 0)

	// Check that mempool swap cap is disabled
	mempoolSwapCap := cosmos.NewUint(uint64(mgr.Keeper().GetConfigInt64(ctx, constants.MempoolSwapCapTOR)))
	c.Check(mempoolSwapCap.IsZero(), Equals, true, Commentf("Mempool swaps should be disabled with zero cap"))

	// Verify swap memo is NOT processable before finalization
	memo, _ := ParseMemo(common.LatestVersion, "=:ETH.ETH:"+GetRandomETHAddress().String())
	canProcessMempoolSwap := !mempoolSwapCap.IsZero() && memo.IsType(TxSwap)
	c.Check(canProcessMempoolSwap, Equals, false, Commentf("Should NOT be able to process mempool swap when disabled"))
}
