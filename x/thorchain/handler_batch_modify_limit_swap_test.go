package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerBatchModifyLimitSwapSuite struct{}

var _ = Suite(&HandlerBatchModifyLimitSwapSuite{})

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	// Create addresses and assets (RUNE source, signer must match from)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	sourceAsset := common.RuneAsset()
	targetAsset1 := common.BTCAsset
	targetAsset2 := common.ETHAsset
	sourceCoin1 := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	sourceCoin2 := common.NewCoin(sourceAsset, cosmos.NewUint(200*common.One))
	targetCoin1 := common.NewCoin(targetAsset1, cosmos.NewUint(500*common.One))
	targetCoin2 := common.NewCoin(targetAsset2, cosmos.NewUint(1000*common.One))
	modifiedTargetAmount1 := cosmos.NewUint(600 * common.One)
	modifiedTargetAmount2 := cosmos.NewUint(1200 * common.One)

	// Create limit swaps in the keeper
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin1},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset1, fromAddr, targetCoin1.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin2},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset2, fromAddr, targetCoin2.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Create batch modify message with two operations
	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin1,
			Target:               targetCoin1,
			ModifiedTargetAmount: modifiedTargetAmount1,
		},
		{
			From:                 fromAddr,
			Source:               sourceCoin2,
			Target:               targetCoin2,
			ModifiedTargetAmount: modifiedTargetAmount2,
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	// Test successful batch modification
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify both limit swaps were modified
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(modifiedTargetAmount1), Equals, true)

	modifiedSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap2.TradeTarget.Equal(modifiedTargetAmount2), Equals, true)
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapTooManyOperations(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Create more operations than allowed
	operations := make([]ModifyLimitSwapOperation, MaxBatchModifyOperations+1)
	for i := range operations {
		operations[i] = ModifyLimitSwapOperation{
			From:                 fromAddr,
			Source:               common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
			Target:               common.NewCoin(common.BTCAsset, cosmos.NewUint(500*common.One)),
			ModifiedTargetAmount: cosmos.NewUint(600 * common.One),
		}
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*too many operations.*")
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapEmptyOperations(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)
	signer := GetRandomBech32Addr()

	// Create batch modify message with no operations
	msg := NewMsgBatchModifyLimitSwap([]ModifyLimitSwapOperation{}, signer)

	// Should fail validation
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*no operations provided.*")
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapPartialFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	// Create only one limit swap (RUNE source)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin1 := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin1 := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount1 := cosmos.NewUint(600 * common.One)

	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin1},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin1.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	// Create batch with one valid and one invalid operation
	// Second operation references a non-existent limit swap
	sourceCoin2 := common.NewCoin(sourceAsset, cosmos.NewUint(200*common.One))
	targetCoin2 := common.NewCoin(targetAsset, cosmos.NewUint(1000*common.One))
	modifiedTargetAmount2 := cosmos.NewUint(1200 * common.One)

	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin1,
			Target:               targetCoin1,
			ModifiedTargetAmount: modifiedTargetAmount1,
		},
		{
			From:                 fromAddr, // No swap exists for this source/target combo
			Source:               sourceCoin2,
			Target:               targetCoin2,
			ModifiedTargetAmount: modifiedTargetAmount2,
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	// Should fail because second operation fails
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*operation 1 failed.*")
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapWithCancellation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	// Create two limit swaps (RUNE source)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	sourceAsset := common.RuneAsset()
	targetAsset1 := common.BTCAsset
	targetAsset2 := common.ETHAsset
	sourceCoin1 := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	sourceCoin2 := common.NewCoin(sourceAsset, cosmos.NewUint(200*common.One))
	targetCoin1 := common.NewCoin(targetAsset1, cosmos.NewUint(500*common.One))
	targetCoin2 := common.NewCoin(targetAsset2, cosmos.NewUint(1000*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)

	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin1},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset1, fromAddr, targetCoin1.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin2},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset2, fromAddr, targetCoin2.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Batch: modify first, cancel second (by setting modified amount to zero)
	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin1,
			Target:               targetCoin1,
			ModifiedTargetAmount: modifiedTargetAmount,
		},
		{
			From:                 fromAddr,
			Source:               sourceCoin2,
			Target:               targetCoin2,
			ModifiedTargetAmount: cosmos.ZeroUint(), // Cancellation
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify first swap was modified
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	// Verify second swap was cancelled (removed)
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
	c.Assert(err, NotNil) // Should be removed
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	fromAddr := GetRandomTHORAddress()
	sourceCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(common.BTCAsset, cosmos.NewUint(500*common.One))

	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin,
			Target:               targetCoin,
			ModifiedTargetAmount: cosmos.NewUint(600 * common.One),
		},
	}

	// Empty signer
	invalidMsg := NewMsgBatchModifyLimitSwap(operations, cosmos.AccAddress{})
	result, err := handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapInvalidOperation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Test with operation having same source and target
	sourceCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin,
			Target:               sourceCoin, // Same as source - invalid
			ModifiedTargetAmount: cosmos.NewUint(600 * common.One),
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapMultipleSameFrom(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	// Create multiple limit swaps from same address (RUNE source)
	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin1 := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	targetCoin2 := common.NewCoin(targetAsset, cosmos.NewUint(600*common.One))

	// First swap with target 500
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin1.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	// Second swap with target 600
	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset, fromAddr, targetCoin2.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Batch modify both - each targets a specific swap by its current target amount
	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin,
			Target:               targetCoin1, // Targets first swap
			ModifiedTargetAmount: cosmos.NewUint(550 * common.One),
		},
		{
			From:                 fromAddr,
			Source:               sourceCoin,
			Target:               targetCoin2, // Targets second swap
			ModifiedTargetAmount: cosmos.NewUint(650 * common.One),
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify first swap was modified
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(cosmos.NewUint(550*common.One)), Equals, true)

	// Verify second swap was modified
	modifiedSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap2.TradeTarget.Equal(cosmos.NewUint(650*common.One)), Equals, true)
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapRUNESource(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	// Create a RUNE-based limit swap
	fromAddr := GetRandomTHORAddress()
	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(1*common.One))

	// For RUNE source, signer must match fromAddr
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Batch modify with one operation
	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               sourceCoin,
			Target:               targetCoin,
			ModifiedTargetAmount: cosmos.NewUint(2 * common.One),
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify modification
	modifiedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap.TradeTarget.Equal(cosmos.NewUint(2*common.One)), Equals, true)
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapRejectsExternalChainSource(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)
	signer := GetRandomBech32Addr()

	// Attempt batch modify with BTC source asset (should be rejected)
	fromAddr := GetRandomBTCAddress()
	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			Target:               common.NewCoin(common.RuneAsset(), cosmos.NewUint(500*common.One)),
			ModifiedTargetAmount: cosmos.NewUint(600 * common.One),
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, signer)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*batch modify only supports THORChain-native source assets.*")
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapRejectsSignerMismatch(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewBatchModifyLimitSwapHandler(mgr)

	fromAddr := GetRandomTHORAddress()
	differentSigner := GetRandomBech32Addr() // Different from fromAddr

	operations := []ModifyLimitSwapOperation{
		{
			From:                 fromAddr,
			Source:               common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
			Target:               common.NewCoin(common.BTCAsset, cosmos.NewUint(500*common.One)),
			ModifiedTargetAmount: cosmos.NewUint(600 * common.One),
		},
	}

	msg := NewMsgBatchModifyLimitSwap(operations, differentSigner)

	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*from and signer address must match.*")
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapFeeDeduction(c *C) {
	ctx, mgr := setupManagerForTest(c)

	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Fund the account
	FundAccount(c, ctx, mgr.Keeper(), signer, 200*common.One)
	denom := common.RuneNative.Native()
	initialRune := mgr.Keeper().GetBalance(ctx, signer).AmountOf(denom)

	// First batch modify should get discounted fee
	discountedFee := mgr.Keeper().GetModifyLimitSwapFee(ctx, signer)
	c.Assert(discountedFee.LT(mgr.Keeper().GetNativeTxFee(ctx)), Equals, true, Commentf("discounted fee should be less than standard fee"))

	// Deduct first fee (discounted)
	err = mgr.Keeper().DeductModifyLimitSwapFee(ctx, signer)
	c.Assert(err, IsNil)

	afterFirstRune := mgr.Keeper().GetBalance(ctx, signer).AmountOf(denom)
	firstDeduction := initialRune.Sub(afterFirstRune)
	c.Assert(firstDeduction.String(), Equals, discountedFee.String())

	// Second fee in same block should be full fee
	fullFee := mgr.Keeper().GetModifyLimitSwapFee(ctx, signer)
	c.Assert(fullFee.Equal(mgr.Keeper().GetNativeTxFee(ctx)), Equals, true, Commentf("second fee should be the standard tx fee"))

	err = mgr.Keeper().DeductModifyLimitSwapFee(ctx, signer)
	c.Assert(err, IsNil)

	afterSecondRune := mgr.Keeper().GetBalance(ctx, signer).AmountOf(denom)
	secondDeduction := afterFirstRune.Sub(afterSecondRune)
	c.Assert(secondDeduction.String(), Equals, fullFee.String())
}

func (s *HandlerBatchModifyLimitSwapSuite) TestBatchModifyLimitSwapInsufficientFunds(c *C) {
	ctx, mgr := setupManagerForTest(c)

	fromAddr := GetRandomTHORAddress()
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Don't fund the account - should fail fee deduction
	err = mgr.Keeper().DeductModifyLimitSwapFee(ctx, signer)
	c.Assert(err, NotNil)
}
