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

type HandlerIntentOpenSuite struct{}

var _ = Suite(&HandlerIntentOpenSuite{})

type HandlerIntentOpenTestHelper struct {
	keeper.Keeper
	failToGetVault    bool
	failToSetIntent   bool
	failToGetConstant bool
	failToEmitEvent   bool
}

func NewHandlerIntentOpenTestHelper(k keeper.Keeper) *HandlerIntentOpenTestHelper {
	return &HandlerIntentOpenTestHelper{
		Keeper: k,
	}
}

func (h *HandlerIntentOpenTestHelper) GetVaultIterator(ctx cosmos.Context) cosmos.Iterator {
	if h.failToGetVault {
		return keeper.NilIterator()
	}
	return h.Keeper.GetVaultIterator(ctx)
}

func (h *HandlerIntentOpenTestHelper) SetIntent(ctx cosmos.Context, intent Intent) {
	if h.failToSetIntent {
		return
	}
	h.Keeper.SetIntent(ctx, intent)
}

func (h *HandlerIntentOpenTestHelper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if h.failToGetConstant {
		return 0, errKaboom
	}
	return h.Keeper.GetMimir(ctx, key)
}

func (HandlerIntentOpenSuite) TestIntentOpen_HappyPath(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 500), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentMinFeeBasisPoints.String(), 10), IsNil)

	// Create a pool for BTC
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent to swap DOGE for BTC at a specific price
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,               // target asset
		cosmos.NewUint(15*common.One), // high target (15 BTC)
		cosmos.NewUint(10*common.One), // low target (10 BTC)
		GetRandomBTCAddress(),         // target address
		common.NoAddress,              // no affiliate
		cosmos.ZeroUint(),             // no affiliate fee
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.TxID, Equals, tx.ID)
	c.Check(intent.Status, Equals, IntentActive)
	c.Check(intent.SourceAsset.Equals(common.DOGEAsset), Equals, true)
	c.Check(intent.TargetAsset.Equals(common.BTCAsset), Equals, true)
	c.Check(intent.HighTarget.Equal(cosmos.NewUint(15*common.One)), Equals, true)
	c.Check(intent.LowTarget.Equal(cosmos.NewUint(10*common.One)), Equals, true)
}

func (HandlerIntentOpenSuite) TestIntentOpen_WithAffiliateFee(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with affiliate fee
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	affAddr := GetRandomTHORAddress()
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		GetRandomBTCAddress(),
		affAddr,             // affiliate address
		cosmos.NewUint(100), // 1% affiliate fee
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created with affiliate info
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.AffiliateAddress.Equals(affAddr), Equals, true)
	c.Check(intent.AffiliateBasisPoints.Equal(cosmos.NewUint(100)), Equals, true)
}

func (HandlerIntentOpenSuite) TestIntentOpen_NativeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with RUNE as source (native asset)
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		GetRandomBTCAddress(),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.SourceAsset.IsRune(), Equals, true)
}

func (HandlerIntentOpenSuite) TestIntentOpen_ValidationErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)

	// Create a pool for BTC
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}

	testCases := []struct {
		name        string
		setupMsg    func() *MsgIntentOpen
		expectedErr error
	}{
		{
			name: "feature disabled should fail",
			setupMsg: func() *MsgIntentOpen {
				w.keeper.DeleteMimir(w.ctx, constants.IntentsEnabled.String())
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					GetRandomBTCAddress(),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "high target < low target should fail",
			setupMsg: func() *MsgIntentOpen {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					cosmos.NewUint(5*common.One), // high < low
					cosmos.NewUint(10*common.One),
					GetRandomBTCAddress(),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "multiple coins should fail",
			setupMsg: func() *MsgIntentOpen {
				multiTx := GetRandomTx()
				multiTx.Coins = common.Coins{
					common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
				}
				return NewMsgIntentOpen(
					multiTx,
					common.BTCAsset,
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					GetRandomBTCAddress(),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "zero high target should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					cosmos.ZeroUint(), // zero high
					cosmos.NewUint(10*common.One),
					GetRandomBTCAddress(),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "zero low target should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					cosmos.NewUint(15*common.One),
					cosmos.ZeroUint(), // zero low
					GetRandomBTCAddress(),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "empty target address should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress, // empty address
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErr: se.ErrInvalidRequest,
		},
	}

	handler := NewIntentOpenHandler(NewDummyMgrWithKeeper(w.keeper))
	for _, tc := range testCases {
		msg := tc.setupMsg()
		_, err := handler.Run(w.ctx, msg)
		c.Check(errors.Is(err, tc.expectedErr), Equals, true, Commentf("name:%s, got err: %v", tc.name, err))
	}
}

func (HandlerIntentOpenSuite) TestIntentOpen_KeeperErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		GetRandomBTCAddress(),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	// Test vault iterator failure
	testKeeper := NewHandlerIntentOpenTestHelper(w.keeper)
	testKeeper.failToGetVault = true
	handler := NewIntentOpenHandler(NewDummyMgrWithKeeper(testKeeper))
	result, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true)
	c.Check(result, IsNil)

	// Test SetIntent failure
	testKeeper2 := NewHandlerIntentOpenTestHelper(w.keeper)
	testKeeper2.failToSetIntent = true
	handler2 := NewIntentOpenHandler(NewDummyMgrWithKeeper(testKeeper2))
	// Note: SetIntent doesn't return error, so this won't fail the handler
	// but we test that the code path is covered
	_, err = handler2.Run(w.ctx, msg)
	c.Check(err, IsNil) // Should succeed even if SetIntent is noop
}

func (HandlerIntentOpenSuite) TestIntentOpen_ExpiryCalculation(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with specific TTL
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1), IsNil)
	c.Assert(w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 500), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		GetRandomBTCAddress(),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	currentHeight := w.ctx.BlockHeight()
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify expiry height is calculated correctly
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	expectedExpiry := currentHeight + 500
	c.Check(intent.ExpiryHeight, Equals, expectedExpiry)
}

func (HandlerIntentOpenSuite) TestIntentOpen_InvalidMessage(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Pass wrong message type
	msgBan := NewMsgBan(GetRandomBech32Addr(), w.activeNodeAccount.NodeAddress)
	result, err := handler.Run(w.ctx, msgBan)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	c.Check(result, IsNil)
}
