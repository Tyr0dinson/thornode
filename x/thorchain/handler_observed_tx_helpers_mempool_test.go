package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type HandlerObservedTxHelpersMempoolSuite struct{}

var _ = Suite(&HandlerObservedTxHelpersMempoolSuite{})

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

	// Create unfinalized TxOutItem
	txOutItem := TxOutItem{
		Chain:            common.BTCChain,
		InHash:           inTxID,
		VaultPubKey:      vault.PubKey,
		ToAddress:        GetRandomBTCAddress(),
		Coin:             common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:             "OUT:" + inTxID.String(),
		InboundFinalized: false,
	}

	// Add to voter actions
	voter.Actions = []TxOutItem{txOutItem}

	// Stage the TxOutItem (instead of adding directly to TxOut)
	outboundHeight := ctx.BlockHeight() + 10
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, txOutItem, outboundHeight), IsNil)

	// Save voter
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Test 1: Successfully finalize outbounds
	err := finalizeOutbounds(ctx, mgr, voter)
	c.Assert(err, IsNil, Commentf("finalizeOutbounds failed: %v", err))

	// Verify action was updated
	c.Check(voter.Actions[0].InboundFinalized, Equals, true, Commentf("Action should be finalized"))

	// Verify staged outbound was moved to real TxOut
	updatedTxOut, err := mgr.Keeper().GetTxOut(ctx, outboundHeight)
	c.Assert(err, IsNil)
	c.Assert(updatedTxOut.TxArray, HasLen, 1)
	c.Check(updatedTxOut.TxArray[0].InboundFinalized, Equals, true, Commentf("Queued TxOut should be finalized"))

	// Verify staged outbounds are deleted
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(len(stagedItems), Equals, 0, Commentf("Staged outbounds should be deleted after finalization"))

	// Test 2: Multiple actions for single inbound
	voter2 := NewObservedTxVoter(GetRandomTxHash(), ObservedTxs{})
	voter2.UnfinalizedHeight = ctx.BlockHeight()
	voter2.Actions = []TxOutItem{
		{Chain: common.BTCChain, InHash: voter2.TxID, ToAddress: GetRandomBTCAddress(), VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)), InboundFinalized: false},
		{Chain: common.BTCChain, InHash: voter2.TxID, ToAddress: GetRandomBTCAddress(), VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)), InboundFinalized: false},
	}

	// Stage both items
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter2.Actions[0], ctx.BlockHeight()+20), IsNil)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, voter2.Actions[1], ctx.BlockHeight()+20), IsNil)

	err = finalizeOutbounds(ctx, mgr, voter2)
	c.Assert(err, IsNil)

	// Verify both actions finalized
	c.Check(voter2.Actions[0].InboundFinalized, Equals, true)
	c.Check(voter2.Actions[1].InboundFinalized, Equals, true)

	// Test 3: Skip already finalized actions
	voter3 := NewObservedTxVoter(GetRandomTxHash(), ObservedTxs{})
	voter3.Actions = []TxOutItem{
		{Chain: common.BTCChain, InHash: voter3.TxID, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)), InboundFinalized: true},
	}

	err = finalizeOutbounds(ctx, mgr, voter3)
	c.Assert(err, IsNil, Commentf("Should succeed even with already finalized actions"))
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
		Chain:            common.ETHChain,
		InHash:           inTxID,
		VaultPubKey:      vault.PubKey,
		ToAddress:        GetRandomETHAddress(),
		Coin:             common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
		Memo:             "OUT:" + inTxID.String(),
		InboundFinalized: false,
		// OutHash defaults to zero value (empty)
	}

	voter.Actions = []TxOutItem{txOutItem}

	// Stage the TxOutItem (instead of adding directly to TxOut)
	outboundHeight := ctx.BlockHeight() + 10
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, txOutItem, outboundHeight), IsNil)

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Test 1: Successfully revert mempool swap
	err := revertMempoolSwap(ctx, mgr.Keeper(), mgr, voter)
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
			Chain:            common.ETHChain,
			InHash:           voter2.TxID,
			Coin:             common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
			InboundFinalized: false,
			OutHash:          GetRandomTxHash(), // Already signed
		},
	}

	err = revertMempoolSwap(ctx, mgr.Keeper(), mgr, voter2)
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
		Chain:            common.THORChain,
		InHash:           runeTxID,
		ToAddress:        GetRandomRUNEAddress(),
		Coin:             common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
		InboundFinalized: false,
	}
	runeVoter.Actions = []TxOutItem{nativeTxOutItem}

	// Stage the native outbound
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeTxOutItem, ctx.BlockHeight()+15), IsNil)

	err = revertMempoolSwap(ctx, mgr.Keeper(), mgr, runeVoter)
	c.Assert(err, IsNil, Commentf("Should handle native (RUNE) output"))

	// Verify voter is marked as reverted
	updatedRuneVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, runeTxID)
	c.Assert(err, IsNil)
	c.Check(updatedRuneVoter.Reverted, Equals, true, Commentf("RUNE voter should be marked as reverted"))
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
