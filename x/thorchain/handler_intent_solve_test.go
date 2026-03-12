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
	failToGetIntent             bool
	failToSetIntentSolve        bool
	failSendFromAccountToModule bool
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

func (h *HandlerIntentSolveTestHelper) SendFromAccountToModule(ctx cosmos.Context, from cosmos.AccAddress, to string, coins common.Coins) error {
	if h.failSendFromAccountToModule {
		return errKaboom
	}
	return h.Keeper.SendFromAccountToModule(ctx, from, to, coins)
}

// SolveFailingSecuredAssetManager returns errors for testing
type SolveFailingSecuredAssetManager struct {
	DummySecuredAssetManager
}

func (f SolveFailingSecuredAssetManager) Withdraw(_ cosmos.Context, _ common.Asset, _ cosmos.Uint, _ cosmos.AccAddress, _ common.Address, _ common.TxID) (common.Coin, error) {
	return common.NoCoin, errKaboom
}

// SolveFailingTradeAccountManager returns errors for testing
type SolveFailingTradeAccountManager struct {
	DummyTradeAccountManager
}

func (f SolveFailingTradeAccountManager) Withdrawal(_ cosmos.Context, _ common.Asset, _ cosmos.Uint, _ cosmos.AccAddress, _ common.Address, _ common.TxID) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), errKaboom
}

func (HandlerIntentSolveSuite) TestIntentSolve_HappyPath(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create and save an intent
	intentTx := GetRandomTx()
	intentTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve the intent with mid-range offer
	// Use DOGE address for solver since they need to receive DOGE (the source asset)
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))}
	// Provide destination address on DOGE chain (same as FromAddress in this case)
	msg := NewMsgIntentSolve(
		solveTx,
		intentTx.ID,
		solveTx.FromAddress, // Destination address on source asset chain
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
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	currentHeight := w.ctx.BlockHeight()

	// Create intent
	intentTx := GetRandomTx()
	intentTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 5000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Test Case 1: Offer at high target (immediate match)
	solveTx1 := GetRandomTx()
	solveTx1.FromAddress = GetRandomDOGEAddress()
	solveTx1.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One))}
	msg1 := NewMsgIntentSolve(solveTx1, intentTx.ID, solveTx1.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	solve1, err := w.keeper.GetIntentSolve(w.ctx, solveTx1.ID)
	c.Assert(err, IsNil)
	c.Check(solve1.MatchHeight, Equals, currentHeight, Commentf("High target should match immediately"))

	// Test Case 2: Offer at low target (delayed match at slide blocks)
	solveTx2 := GetRandomTx()
	solveTx2.FromAddress = GetRandomDOGEAddress()
	solveTx2.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg2 := NewMsgIntentSolve(solveTx2, intentTx.ID, solveTx2.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, IsNil)

	solve2, err := w.keeper.GetIntentSolve(w.ctx, solveTx2.ID)
	c.Assert(err, IsNil)
	expectedMatch2 := currentHeight + 1000 // full slide
	c.Check(solve2.MatchHeight, Equals, expectedMatch2, Commentf("Low target should match after full slide"))

	// Test Case 3: Offer in middle (proportional delay)
	// Offer 15 BTC (halfway between 10 and 20)
	solveTx3 := GetRandomTx()
	solveTx3.FromAddress = GetRandomDOGEAddress()
	solveTx3.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg3 := NewMsgIntentSolve(solveTx3, intentTx.ID, solveTx3.FromAddress, w.activeNodeAccount.NodeAddress)
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
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 10000)

	currentHeight := w.ctx.BlockHeight()

	// Create intent with short expiry
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 100, // Short expiry
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve at low target (would normally delay by 10000 blocks)
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
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
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress() // Source asset is DOGE, so solver needs DOGE address
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}

	testCases := []struct {
		name        string
		setupMsg    func() *MsgIntentSolve
		expectedErr error
	}{
		{
			name: "feature disabled should fail",
			setupMsg: func() *MsgIntentSolve {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 0)
				return NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "non-existent intent should fail",
			setupMsg: func() *MsgIntentSolve {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				fakeTxID := GetRandomTxHash()
				return NewMsgIntentSolve(solveTx, fakeTxID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name: "inactive intent should fail",
			setupMsg: func() *MsgIntentSolve {
				inactiveIntent := intent
				inactiveIntent.Status = IntentCompleted
				w.keeper.SetIntent(w.ctx, inactiveIntent)
				return NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
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
				return NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
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
				multiTx.FromAddress = GetRandomDOGEAddress()
				multiTx.Coins = common.Coins{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One)),
					common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
				}
				return NewMsgIntentSolve(multiTx, intentTx.ID, multiTx.FromAddress, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest, // caught by ValidateBasic
		},
		{
			name: "wrong asset should fail",
			setupMsg: func() *MsgIntentSolve {
				wrongAssetTx := GetRandomTx()
				wrongAssetTx.FromAddress = GetRandomDOGEAddress()
				wrongAssetTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(15*common.One))}
				return NewMsgIntentSolve(wrongAssetTx, intentTx.ID, wrongAssetTx.FromAddress, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "amount below low target should fail",
			setupMsg: func() *MsgIntentSolve {
				lowTx := GetRandomTx()
				lowTx.FromAddress = GetRandomDOGEAddress()
				lowTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One))} // Below low target of 10
				return NewMsgIntentSolve(lowTx, intentTx.ID, lowTx.FromAddress, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "unsupported THOR-native target asset should fail",
			setupMsg: func() *MsgIntentSolve {
				unsupportedIntent := intent
				unsupportedIntent.TargetAsset = common.BTCAsset.GetSyntheticAsset()
				w.keeper.SetIntent(w.ctx, unsupportedIntent)

				synthTx := GetRandomTx()
				synthTx.FromAddress = GetRandomTHORAddress()
				synthTx.Coins = common.Coins{common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(15*common.One))}
				return NewMsgIntentSolve(synthTx, intentTx.ID, GetRandomDOGEAddress(), w.activeNodeAccount.NodeAddress)
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
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

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
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	currentHeight := w.ctx.BlockHeight()

	// Create intent with high == low (degenerate case)
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(10 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One), // Same as high
		ExpiryHeight:  currentHeight + 5000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve at the exact price
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Should match immediately since there's no range
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.MatchHeight, Equals, currentHeight, Commentf("High == Low should match immediately"))
}

func (HandlerIntentSolveSuite) TestIntentSolve_SecuredAssetEscrow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create intent requesting secured BTC
	securedBTC := common.BTCAsset.GetSecuredAsset()
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
		TargetAsset:   securedBTC,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(10 * common.One),
		LowTarget:     cosmos.NewUint(5 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve with secured asset - use THOR address for FromAddress
	// Source asset is RUNE (on THOR chain), so destination needs to be THOR address
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(securedBTC, cosmos.NewUint(7*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify solve was created
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.TargetCoin.Asset.IsSecuredAsset(), Equals, true)
}

func (HandlerIntentSolveSuite) TestIntentSolve_SecuredAssetEscrowError(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent requesting secured BTC
	securedBTC := common.BTCAsset.GetSecuredAsset()
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
		TargetAsset:   securedBTC,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(10 * common.One),
		LowTarget:     cosmos.NewUint(5 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	mgr.securedMgr = SolveFailingSecuredAssetManager{}
	handler := NewIntentSolveHandler(mgr)

	// Solve with secured asset - use THOR address for FromAddress
	// Source asset is RUNE (on THOR chain), so destination needs to be THOR address
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(securedBTC, cosmos.NewUint(7*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to escrow secured asset.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_TradeAssetEscrow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create intent requesting trade BTC
	tradeBTC := common.BTCAsset.GetTradeAsset()
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
		TargetAsset:   tradeBTC,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(10 * common.One),
		LowTarget:     cosmos.NewUint(5 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve with trade asset - use THOR address for FromAddress
	// Source asset is RUNE (on THOR chain), so destination needs to be THOR address
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(tradeBTC, cosmos.NewUint(7*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify solve was created
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.TargetCoin.Asset.IsTradeAsset(), Equals, true)
}

func (HandlerIntentSolveSuite) TestIntentSolve_TradeAssetEscrowError(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent requesting trade BTC
	tradeBTC := common.BTCAsset.GetTradeAsset()
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
		TargetAsset:   tradeBTC,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(10 * common.One),
		LowTarget:     cosmos.NewUint(5 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	mgr.tradeMgr = SolveFailingTradeAccountManager{}
	handler := NewIntentSolveHandler(mgr)

	// Solve with trade asset - use THOR address for FromAddress
	// Source asset is RUNE (on THOR chain), so destination needs to be THOR address
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(tradeBTC, cosmos.NewUint(7*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to escrow trade asset.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_RUNEEscrow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent requesting RUNE with reasonable targets
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		TargetAsset:   common.RuneAsset(),
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(200 * common.One),
		LowTarget:     cosmos.NewUint(100 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Fund the solver account so RUNE escrow deterministically succeeds
	FundAccount(c, w.ctx, w.keeper, w.activeNodeAccount.NodeAddress, 200*common.One)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Source asset is BTC, so destination needs to be BTC address
	solveTx := GetRandomTx()
	solveTx.FromAddress, _ = common.NewAddress(w.activeNodeAccount.NodeAddress.String())
	solveTx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(150*common.One))}
	destAddr := GetRandomBTCAddress()
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, destAddr, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify solve was created
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.TargetCoin.Asset.IsRune(), Equals, true)
}

func (HandlerIntentSolveSuite) TestIntentSolve_RUNEEscrowError(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent requesting RUNE
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		TargetAsset:   common.RuneAsset(),
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(1000 * common.One),
		LowTarget:     cosmos.NewUint(500 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	testKeeper := NewHandlerIntentSolveTestHelper(w.keeper)
	testKeeper.failSendFromAccountToModule = true
	mgr := NewDummyMgrWithKeeper(testKeeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve with RUNE - use THOR address for FromAddress
	// Source asset is BTC, so destination needs to be BTC address
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(700*common.One))}
	destAddr := GetRandomBTCAddress()
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, destAddr, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to escrow RUNE.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_EmptyCoins(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Create solve with empty coins
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{} // empty
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	// Error comes from ValidateBasic: "must have at least 1 coin"
	c.Check(err.Error(), Matches, ".*must have at least 1 coin.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_OfferExceedsHighTarget(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	currentHeight := w.ctx.BlockHeight()

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 5000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve with offer EXCEEDING high target (25 > 20)
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(25*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Should match immediately since offer exceeds high target
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
	c.Check(solve.MatchHeight, Equals, currentHeight, Commentf("Offer exceeding high target should match immediately"))
}

func (HandlerIntentSolveSuite) TestIntentSolve_IntentExpiresAtCurrentBlock(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	currentHeight := w.ctx.BlockHeight()

	// Create intent that expires at current block (edge case)
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight, // Expires at exactly current block
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Try to solve - should fail because intent has expired
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*intent has expired.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_CrossChainRequiresDestinationAddress(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent: user wants to swap ETH for BTC
	// Using ETH as source because ETH addresses (0x prefix) are easily distinguishable from BTC
	intentTx := GetRandomTx()
	intentTx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))}
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomETHAddress(),
		Source:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Test 1: Cross-chain solve WITHOUT destination address should FAIL
	// Solver sends from BTC chain, but intent source is ETH
	solveTx1 := GetRandomTx()
	solveTx1.FromAddress = GetRandomBTCAddress() // Sending from BTC
	solveTx1.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))}
	msg1 := NewMsgIntentSolve(solveTx1, intentTx.ID, common.NoAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*destination address is required when solving cross-chain.*")

	// Test 2: Cross-chain solve WITH wrong chain destination address should FAIL
	// BTC address is not valid for ETH chain (ETH needs 0x prefix)
	solveTx2 := GetRandomTx()
	solveTx2.FromAddress = GetRandomBTCAddress() // Sending from BTC
	solveTx2.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))}
	wrongDestAddr := GetRandomBTCAddress() // BTC address, but source is ETH
	msg2 := NewMsgIntentSolve(solveTx2, intentTx.ID, wrongDestAddr, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*destination address .* is not valid for chain.*")

	// Test 3: Cross-chain solve WITH correct chain destination address should SUCCEED
	solveTx3 := GetRandomTx()
	solveTx3.FromAddress = GetRandomBTCAddress() // Sending from BTC
	correctDestAddr := GetRandomETHAddress()     // ETH address for receiving ETH (source asset)
	solveTx3.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))}
	msg3 := NewMsgIntentSolve(solveTx3, intentTx.ID, correctDestAddr, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg3)
	c.Assert(err, IsNil)

	// Verify solve was created with correct destination address
	solve, err := w.keeper.GetIntentSolve(w.ctx, solveTx3.ID)
	c.Assert(err, IsNil)
	c.Check(solve.DestinationAddress.Equals(correctDestAddr), Equals, true)
}

func (HandlerIntentSolveSuite) TestIntentSolve_DuplicateSolveTxID(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Place first solve
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Try to place solve with same TxID (should fail)
	msg2 := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*already exists.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_SelfSolving(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Create intent with a specific source address
	intentTx := GetRandomTx()
	sourceAddr := GetRandomDOGEAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: sourceAddr,
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Try to solve own intent (FromAddress matches intent SourceAddress)
	solveTx := GetRandomTx()
	solveTx.FromAddress = sourceAddr // Same as intent creator
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*cannot solve their own intent.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_MaxSolvesPerIntent(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with very low max solves
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)
	w.keeper.SetMimir(w.ctx, constants.IntentMaxSolvesPerIntent.String(), 2) // Only 2 solves allowed

	// Create intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Place 2 solves successfully
	for i := 0; i < 2; i++ {
		solveTx := GetRandomTx()
		solveTx.FromAddress = GetRandomDOGEAddress()
		solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
		msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
		_, err := handler.Run(w.ctx, msg)
		c.Assert(err, IsNil, Commentf("Solve %d should succeed", i+1))
	}

	// 3rd solve should fail
	solveTx := GetRandomTx()
	solveTx.FromAddress = GetRandomDOGEAddress()
	solveTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One))}
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, solveTx.FromAddress, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*maximum number of solves.*")
}

func (HandlerIntentSolveSuite) TestIntentSolve_UnsupportedNativeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 1000)

	// Use a hypothetical native non-RUNE asset (e.g. IBC token)
	ibcAsset, err := common.NewAsset("THOR.IBC-TOKEN")
	c.Assert(err, IsNil)

	// Create intent targeting this asset
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		TargetAsset:   ibcAsset,
		TargetAddress: GetRandomTHORAddress(),
		HighTarget:    cosmos.NewUint(200 * common.One),
		LowTarget:     cosmos.NewUint(100 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentSolveHandler(mgr)

	// Solve with the IBC token via a native (THORChain) tx. Validation should
	// reject unsupported THOR-native assets before escrow handling.
	solveTx := GetRandomTx()
	solveTx.Chain = common.THORChain
	solveTx.FromAddress = GetRandomTHORAddress()
	solveTx.Coins = common.Coins{common.NewCoin(ibcAsset, cosmos.NewUint(150*common.One))}
	destAddr := GetRandomBTCAddress()
	msg := NewMsgIntentSolve(solveTx, intentTx.ID, destAddr, w.activeNodeAccount.NodeAddress)

	_, err = handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*THOR-native asset THOR.IBC-TOKEN is not supported by intents.*")
}
