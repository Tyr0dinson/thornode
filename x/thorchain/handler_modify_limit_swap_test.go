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

	modifiedSwap, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(modifiedSwap.SwapType, Equals, MarketSwap)
	c.Check(modifiedSwap.TradeTarget.Uint64() > targetCoin.Amount.Uint64(), Equals, true)
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

	// Verify both limit swaps were modified
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	modifiedSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap2.TradeTarget.Equal(modifiedTargetAmount), Equals, true)
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

	// Verify both limit swaps were removed
	mSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1)
	c.Assert(err, IsNil)
	c.Check(mSwap1.SwapType, Equals, MarketSwap)

	mSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2)
	c.Assert(err, IsNil)
	c.Check(mSwap2.SwapType, Equals, MarketSwap)
}
