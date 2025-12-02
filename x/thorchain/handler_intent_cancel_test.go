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
	failToGetIntent bool
	failToGetPool   bool
	failToGetVault  bool
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
	return h.Keeper.GetIntent(ctx, txID)
}

func (h *HandlerIntentCancelTestHelper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if h.failToGetPool {
		return NewPool(), errKaboom
	}
	return h.Keeper.GetPool(ctx, asset)
}

func (h *HandlerIntentCancelTestHelper) GetVaultIterator(ctx cosmos.Context) cosmos.Iterator {
	if h.failToGetVault {
		return nil
	}
	return h.Keeper.GetVaultIterator(ctx)
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
