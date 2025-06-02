package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerModifyLimitSwapSuite struct{}

var _ = Suite(&HandlerModifyLimitSwapSuite{})

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create a valid MsgModifyLimitSwap
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	signer := GetRandomBech32Addr()

	msg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, signer)

	// Test when no matching limit swap exists
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "could not find matching limit swap")

	// Create a limit swap in the keeper
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Test successful modification
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the limit swap was modified
	modifiedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	// verify original index is no longer there
	hashes, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, *limitSwap)
	c.Assert(err, IsNil)
	c.Check(hashes, HasLen, 0)

	// verify new index IS there
	hashes, err = mgr.Keeper().GetAdvSwapQueueIndex(ctx, modifiedSwap)
	c.Assert(err, IsNil)
	c.Check(hashes, HasLen, 1)

	// Test cancellation (setting modified amount to zero)
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, common.NewCoin(targetAsset, modifiedTargetAmount), cosmos.ZeroUint(), signer)
	result, err = handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the limit swap was removed from the swap book
	hashes, err = mgr.Keeper().GetAdvSwapQueueIndex(ctx, modifiedSwap)
	c.Assert(err, IsNil)
	c.Check(hashes, HasLen, 0)

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID)
	c.Assert(err, NotNil) // Should be removed
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create a manager with our test keeper
	mgr := NewDummyMgr()
	mgr.K = k

	handler := NewModifyLimitSwapHandler(mgr)

	// Test with invalid message (empty signer)
	fromAddr := GetRandomTHORAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)

	invalidMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, cosmos.AccAddress{})
	result, err := handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapAddressCheck(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses
	fromAddr := GetRandomTHORAddress()
	differentAddr := GetRandomTHORAddress()
	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	signer := GetRandomBech32Addr()

	// Create a limit swap in the keeper
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Try to modify with a different address
	msg := types.NewMsgModifyLimitSwap(differentAddr, sourceCoin, targetCoin, modifiedTargetAmount, signer)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "could not find matching limit swap")

	// Verify the original limit swap is unchanged
	originalSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(originalSwap.TradeTarget.Equal(targetCoin.Amount), Equals, true)
}

func (s *HandlerModifyLimitSwapSuite) TestModifyMultipleLimitSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses and assets
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	signer := GetRandomBech32Addr()

	// Create multiple limit swaps with the same source/target
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Modify both limit swaps
	msg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, signer)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify only the first limit swap was modified (only one swap should be modified)
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	// The second swap should remain unchanged
	modifiedSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap2.TradeTarget.Equal(targetCoin.Amount), Equals, true) // Should still be original amount
}

func (s *HandlerModifyLimitSwapSuite) TestCancelMultipleLimitSwaps(c *C) {
	// Create a manager with our test keeper
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses and assets
	fromAddr := GetRandomTHORAddress()
	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	signer := GetRandomBech32Addr()

	// Create multiple limit swaps with the same source/target
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Cancel both limit swaps by setting modified amount to zero
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, cosmos.ZeroUint(), signer)
	result, err := handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify only the first limit swap was cancelled (removed)
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1)
	c.Assert(err, NotNil) // Should be removed

	// The second swap should remain unchanged as a limit swap
	mSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2)
	c.Assert(err, IsNil)
	c.Check(mSwap2.SwapType, Equals, LimitSwap) // Should still be a limit swap
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapErrorHandling(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test with invalid assets (same source and target)
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	invalidCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	signer := GetRandomBech32Addr()

	invalidMsg := types.NewMsgModifyLimitSwap(fromAddr, invalidCoin, invalidCoin, cosmos.NewUint(200*common.One), signer)
	result, err := handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*source asset and target asset cannot be the same.*")

	// Test with mismatched from address and source asset chain
	thorAddr := GetRandomTHORAddress()
	btcCoin := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	runeCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(500*common.One))

	invalidChainMsg := types.NewMsgModifyLimitSwap(thorAddr, btcCoin, runeCoin, cosmos.NewUint(600*common.One), signer)
	result, err = handler.Run(ctx, invalidChainMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*from address and source asset do not match.*")
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapCancellationLogic(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Create a limit swap with a very large target amount
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	largeAmount := cosmos.NewUint(1 << 62) // Very large amount
	targetCoin := common.NewCoin(targetAsset, largeAmount)
	signer := GetRandomBech32Addr()

	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Test cancellation with zero amount
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, cosmos.ZeroUint(), signer)
	result, err := handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the swap was removed
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID)
	c.Assert(err, NotNil) // Should be removed
}
