package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerIntentSolveSuite struct{}

var _ = Suite(&HandlerIntentSolveSuite{})

type HandlerIntentSolveTestHelper struct {
	keeper.Keeper
	failToGetIntent      bool
	failToSetIntentSolve bool
}

func NewHandlerIntentSolveTestHelper(k keeper.Keeper) *HandlerIntentSolveTestHelper {
	return &HandlerIntentSolveTestHelper{
		Keeper: k,
	}
}

func (h *HandlerIntentSolveTestHelper) GetIntent(ctx cosmos.Context, txID common.TxID) (Intent, error) {
	if h.failToGetIntent {
		return Intent{}, errKaboom
	}
	return h.Keeper.GetIntent(ctx, txID)
}

func (h *HandlerIntentSolveTestHelper) SetIntentSolve(ctx cosmos.Context, solve IntentSolve) {
	if h.failToSetIntentSolve {
		return
	}
	h.Keeper.SetIntentSolve(ctx, solve)
}

func (HandlerIntentSolveSuite) TestIntentSolve_HappyPath(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000), IsNil)

	// Create and save an intent
	intentTx := GetRandomTx()
	intentTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(15*common.One),
		LowTarget:    cosmos.NewUint(10*common.One),
		ExpiryHeight: w.ctx.BlockHeight() + 2000,
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve the intent with mid-range offer
	solveTx := GetRandomTx()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))}
	msg := NewMsgIntentSolve(
		solveTx,
		intentTx.ID,
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify solve was created
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.SolveTxID.Equals(solveTx.ID), Equals, true)
	c.Check(solve.IntentTxID.Equals(intentTx.ID), Equals, true)
	c.Check(solve.TargetCoin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(solve.TargetCoin.Amount.Equal(cosmos.NewUint(12*common.One)), Equals, true)
}

func (HandlerIntentSolveSuite) TestIntentSolve_MatchHeightCalculation(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000), IsNil)

	currentHeight := w.ctx.BlockHeight()

	// Create intent
	intentTx := GetRandomTx()
	intentTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(20*common.One),
		LowTarget:    cosmos.NewUint(10*common.One),
		ExpiryHeight: currentHeight + 5000,
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Test Case 1: Offer at high target (immediate match)
	solveTx1 := GetRandomTx()
	solveTx1.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One))}
	msg1 := NewMsgIntentSolve(solveTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	solve1, err := w.keeper.GetIntentSolve(w.ctx, solveTx1.ID)
	c.Assert(err, IsNil)
	c.Check(solve1.MatchHeight, Equals, currentHeight, Commentf("High target should match immediately"))

	// Test Case 2: Offer at low target (delayed match at slide blocks)
	solveTx2 := GetRandomTx()
	solveTx2.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg2 := NewMsgIntentSolve(solveTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, IsNil)

	solve2, err := w.keeper.GetIntentSolve(w.ctx, solveTx2.ID)
	c.Assert(err, IsNil)
	expectedMatch2 := currentHeight + 1000 // full slide
	c.Check(solve2.MatchHeight, Equals, expectedMatch2, Commentf("Low target should match after full slide"))

	// Test Case 3: Offer in middle (proportional delay)
	// Offer 15 BTC (halfway between 10 and 20)
	solveTx3 := GetRandomTx()
	solveTx3.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg3 := NewMsgIntentSolve(solveTx3, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg3)
	c.Assert(err, IsNil)

	solve3, err := w.keeper.GetIntentSolve(w.ctx, solveTx3.ID)
	c.Assert(err, IsNil)
	// Formula: match_height = current + ((high - offered) * slide / (high - low))
	// = current + ((20 - 15) * 1000 / (20 - 10))
	// = current + (5 * 1000 / 10)
	// = current + 500
	expectedMatch3 := currentHeight + 500
	c.Check(solve3.MatchHeight, Equals, expectedMatch3, Commentf("Mid-range offer should match proportionally"))
}

func (HandlerIntentSolveSuite) TestIntentSolve_MatchHeightCappedAtExpiry(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with short slide period
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 10000), IsNil)

	currentHeight := w.ctx.BlockHeight()

	// Create intent with short expiry
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(20*common.One),
		LowTarget:    cosmos.NewUint(10*common.One),
		ExpiryHeight: currentHeight + 100, // Short expiry
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve at low target (would normally delay by 10000 blocks)
	solveTx := GetRandomTx()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify match height is capped at expiry
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.MatchHeight, Equals, intent.ExpiryHeight, Commentf("Match height should be capped at expiry"))
}

func (HandlerIntentSolveSuite) TestIntentSolve_ValidationErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(20*common.One),
		LowTarget:    cosmos.NewUint(10*common.One),
		ExpiryHeight: w.ctx.BlockHeight() + 1000,
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	solveTx := GetRandomTx()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}

	testCases := []struct {
		name        string
		setupMsg    func() *MsgIntentSolve
		expectedErr error
	}{
		{
			name: "feature disabled should fail",
			setupMsg: func() *MsgIntentSolve {
				w.keeper.DeleteMimir(w.ctx, constants.IntentsEnabled.String())
				return NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "non-existent intent should fail",
			setupMsg: func() *MsgIntentSolve {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				fakeTxID := GetRandomTxHash()
				return NewMsgIntentSolve(solveTx, fakeTxID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name: "inactive intent should fail",
			setupMsg: func() *MsgIntentSolve {
				inactiveIntent := intent
				inactiveIntent.Status = IntentCompleted
				w.keeper.SetIntent(w.ctx, inactiveIntent)
				return NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "expired intent should fail",
			setupMsg: func() *MsgIntentSolve {
				activeIntent := intent
				activeIntent.Status = IntentActive
				activeIntent.ExpiryHeight = w.ctx.BlockHeight() - 1 // Expired
				w.keeper.SetIntent(w.ctx, activeIntent)
				return NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "multiple coins should fail",
			setupMsg: func() *MsgIntentSolve {
				// Reset to valid intent
				intent.ExpiryHeight = w.ctx.BlockHeight() + 1000
				w.keeper.SetIntent(w.ctx, intent)

				multiTx := GetRandomTx()
				multiTx.Coins = common.Coins{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One)),
					common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
				}
				return NewMsgIntentSolve(multiTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "wrong asset should fail",
			setupMsg: func() *MsgIntentSolve {
				wrongAssetTx := GetRandomTx()
				wrongAssetTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(15*common.One))}
				return NewMsgIntentSolve(wrongAssetTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "amount below low target should fail",
			setupMsg: func() *MsgIntentSolve {
				lowTx := GetRandomTx()
				lowTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One))} // Below low target of 10
				return NewMsgIntentSolve(lowTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
	}

	for _, tc := range testCases {
		msg := tc.setupMsg()
		handler := NewIntentSolveHandler(NewDummyMgrWithKeeper(w.keeper))
		_, err := handler.Run(w.ctx, msg)
		c.Check(errors.Is(err, tc.expectedErr), Equals, true, Commentf("name:%s, got err: %v", tc.name, err))
	}
}

func (HandlerIntentSolveSuite) TestIntentSolve_KeeperErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(20*common.One),
		LowTarget:    cosmos.NewUint(10*common.One),
		ExpiryHeight: w.ctx.BlockHeight() + 1000,
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	solveTx := GetRandomTx()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)

	// Test GetIntent failure
	testKeeper := NewHandlerIntentSolveTestHelper(w.keeper)
	testKeeper.failToGetIntent = true
	handler := NewIntentSolveHandler(NewDummyMgrWithKeeper(testKeeper))
	result, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true)
	c.Check(result, IsNil)
}

func (HandlerIntentSolveSuite) TestIntentSolve_InvalidMessage(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Pass wrong message type
	msgBan := NewMsgBan(GetRandomBech32Addr(), w.activeNodeAccount.NodeAddress)
	result, err := handler.Run(w.ctx, msgBan)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	c.Check(result, IsNil)
}

func (HandlerIntentSolveSuite) TestIntentSolve_EdgeCase_HighEqualsLow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000), IsNil)

	currentHeight := w.ctx.BlockHeight()

	// Create intent with high == low (degenerate case)
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:         intentTx.ID,
		Sender:       GetRandomTHORAddress(),
		SourceAsset:  common.DOGEAsset,
		SourceCoin:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:  common.BTCAsset,
		TargetAddr:   GetRandomBTCAddress(),
		HighTarget:   cosmos.NewUint(10*common.One),
		LowTarget:    cosmos.NewUint(10*common.One), // Same as high
		ExpiryHeight: currentHeight + 5000,
		Status:       IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve at the exact price
	solveTx := GetRandomTx()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Should match immediately since there's no range
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.MatchHeight, Equals, currentHeight, Commentf("High == Low should match immediately"))
}
