package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerIntentCancelSuite struct{}

var _ = Suite(&HandlerIntentCancelSuite{})

type HandlerIntentCancelTestHelper struct {
	keeper.Keeper
	failToGetIntent         bool
	failToGetIntentInHandle bool
}

func NewHandlerIntentCancelTestHelper(k keeper.Keeper) *HandlerIntentCancelTestHelper {
	return &HandlerIntentCancelTestHelper{
		Keeper: k,
	}
}

func (h *HandlerIntentCancelTestHelper) GetIntent(ctx cosmos.Context, txID common.TxID) (Intent, error) {
	if h.failToGetIntent {
		return Intent{}, errKaboom
	}
	if h.failToGetIntentInHandle {
		// Only fail on the second call (in handle, not validate)
		h.failToGetIntentInHandle = false
		h.failToGetIntent = true
	}
	return h.Keeper.GetIntent(ctx, txID)
}

func (HandlerIntentCancelSuite) TestIntentCancel_RefundPoolAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for DOGE
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active intent with pool asset
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
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
	handler := NewIntentCancelHandler(mgr)

	// Cancel the intent - tx FromAddress must match intent's SourceAddress
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status updated
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)
}

func (HandlerIntentCancelSuite) TestIntentCancel_RefundNativeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active intent with RUNE (native asset)
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentCancelHandler(mgr)

	// Cancel the intent - tx FromAddress must match intent's SourceAddress
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status updated
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)
}

func (HandlerIntentCancelSuite) TestIntentCancel_ValidationErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active intent
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),

		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	testCases := []struct {
		name        string
		setupMsg    func() *MsgIntentCancel
		expectedErr error
	}{
		{
			name: "non-existent intent should fail",
			setupMsg: func() *MsgIntentCancel {
				fakeTxID := GetRandomTxHash()
				return NewMsgIntentCancel(GetRandomTx(), fakeTxID, cosmos.AccAddress(userAddr))
			},
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name: "inactive intent should fail",
			setupMsg: func() *MsgIntentCancel {
				inactiveIntent := intent
				inactiveIntent.Status = IntentCompleted
				w.keeper.SetIntent(w.ctx, inactiveIntent)
				return NewMsgIntentCancel(GetRandomTx(), intentTx.ID, cosmos.AccAddress(userAddr))
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "wrong signer should fail",
			setupMsg: func() *MsgIntentCancel {
				// Reset to active
				intent.Status = IntentActive
				w.keeper.SetIntent(w.ctx, intent)

				wrongAddr := GetRandomTHORAddress()
				return NewMsgIntentCancel(GetRandomTx(), intentTx.ID, cosmos.AccAddress(wrongAddr))
			},
			expectedErr: se.ErrUnauthorized,
		},
	}

	handler := NewIntentCancelHandler(NewDummyMgrWithKeeper(w.keeper))
	for _, tc := range testCases {
		msg := tc.setupMsg()
		_, err := handler.Run(w.ctx, msg)
		c.Check(errors.Is(err, tc.expectedErr), Equals, true, Commentf("name:%s, got err: %v", tc.name, err))
	}
}

func (HandlerIntentCancelSuite) TestIntentCancel_KeeperErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),

		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	msg := NewMsgIntentCancel(GetRandomTx(), intentTx.ID, cosmos.AccAddress(userAddr))

	// Test GetIntent failure
	testKeeper := NewHandlerIntentCancelTestHelper(w.keeper)
	testKeeper.failToGetIntent = true
	handler := NewIntentCancelHandler(NewDummyMgrWithKeeper(testKeeper))
	result, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true)
	c.Check(result, IsNil)
}

func (HandlerIntentCancelSuite) TestIntentCancel_InvalidMessage(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentCancelHandler(mgr)

	// Pass wrong message type
	msgBan := NewMsgBan(GetRandomBech32Addr(), w.activeNodeAccount.NodeAddress)
	result, err := handler.Run(w.ctx, msgBan)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	c.Check(result, IsNil)
}

func (HandlerIntentCancelSuite) TestIntentCancel_NonPoolAssetRoutesToAuction(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active vault with BTC (no pool will be created for BTC)
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// Create an active intent with BTC (non-pool, non-native asset)
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intentAmount := cosmos.NewUint(100 * common.One)
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.BTCAsset, intentAmount),
		TargetAsset:   common.DOGEAsset,
		TargetAddress: GetRandomDOGEAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentCancelHandler(mgr)

	// Cancel the intent
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify vault balance is NOT reduced (no longer abandoning)
	updatedVault, err := w.keeper.GetVault(w.ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Check(updatedVault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(200*common.One)), Equals, true,
		Commentf("vault balance should be unchanged, got %s", updatedVault.GetCoin(common.BTCAsset).Amount))
}

func (HandlerIntentCancelSuite) TestIntentCancel_StagedPoolRoutesToAuction(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a STAGED pool for DOGE (not Available, so not refundable)
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active intent with DOGE
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
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
	handler := NewIntentCancelHandler(mgr)

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil, Commentf("cancel of staged-pool asset should route to auction, not fail"))
}

func (HandlerIntentCancelSuite) TestIntentCancel_RefundWithExtraCoins(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for DOGE
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active intent with pool asset
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intentAmount := cosmos.NewUint(100 * common.One)
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.DOGEAsset, intentAmount),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentCancelHandler(mgr)

	// Cancel the intent with extra coins of the same asset
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	extraAmount := cosmos.NewUint(25 * common.One)
	cancelTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, extraAmount)}

	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status updated
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)

	// Note: The actual refund amount (intent + extra) is handled by TxOutStore
	// We've verified the handler runs without error when extra coins are included
}

func (HandlerIntentCancelSuite) TestIntentCancel_AvailablePoolRefunds(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an Available pool for DOGE
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active intent with DOGE (pool asset with Available status)
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
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
	handler := NewIntentCancelHandler(mgr)

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status is cancelled (direct refund path)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)
}
