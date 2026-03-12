package thorchain

import (
	"errors"

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
}

func NewHandlerIntentOpenTestHelper(k keeper.Keeper) *HandlerIntentOpenTestHelper {
	return &HandlerIntentOpenTestHelper{
		Keeper: k,
	}
}

func (h *HandlerIntentOpenTestHelper) GetVaultIterator(ctx cosmos.Context) cosmos.Iterator {
	if h.failToGetVault {
		return nil
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

func (h *HandlerIntentOpenTestHelper) GetAsgardVaultsByStatus(ctx cosmos.Context, status VaultStatus) (Vaults, error) {
	if h.failToGetVault {
		return nil, errKaboom
	}
	return h.Keeper.GetAsgardVaultsByStatus(ctx, status)
}

func (HandlerIntentOpenSuite) TestIntentOpen_HappyPath(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000)
	w.keeper.SetMimir(w.ctx, constants.IntentPriceSlideBlocks.String(), 500)
	w.keeper.SetMimir(w.ctx, constants.IntentMinFeeBasisPoints.String(), 10)

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
		GetRandomBTCAddress(),         // target address
		cosmos.NewUint(15*common.One), // high target (15 BTC)
		cosmos.NewUint(10*common.One), // low target (10 BTC)
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
	c.Check(intent.Source.Asset.Equals(common.DOGEAsset), Equals, true)
	c.Check(intent.TargetAsset.Equals(common.BTCAsset), Equals, true)
	c.Check(intent.HighTarget.Equal(cosmos.NewUint(15*common.One)), Equals, true)
	c.Check(intent.LowTarget.Equal(cosmos.NewUint(10*common.One)), Equals, true)
}

func (HandlerIntentOpenSuite) TestIntentOpen_WithAffiliateFee(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with affiliate fee
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	affAddr := GetRandomTHORAddress()
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
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
	c.Check(intent.AffiliateBasisPoints, Equals, int64(100))
}

func (HandlerIntentOpenSuite) TestIntentOpen_NativeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with RUNE as source (native asset)
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.Source.Asset.IsRune(), Equals, true)
}

func (HandlerIntentOpenSuite) TestIntentOpen_ValidationErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create a pool for BTC
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}

	testCases := []struct {
		name           string
		setupMsg       func() *MsgIntentOpen
		expectedErrMsg string
	}{
		{
			name: "feature disabled should fail",
			setupMsg: func() *MsgIntentOpen {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 0)
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*intents feature is disabled.*",
		},
		{
			name: "high target < low target should fail",
			setupMsg: func() *MsgIntentOpen {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.NewUint(5*common.One), // high < low
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*high target must be greater than or equal to low target.*",
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
					GetRandomBTCAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*intent can only be created with one coin type.*",
		},
		{
			name: "zero high target should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.ZeroUint(), // zero high
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*high target cannot be zero.*",
		},
		{
			name: "zero low target should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.ZeroUint(), // zero low
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*low target cannot be zero.*",
		},
		{
			name: "amount below dust threshold should fail",
			setupMsg: func() *MsgIntentOpen {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				dustTx := GetRandomTx()
				// DOGE dust threshold is 100_000_000 (1 DOGE), use amount at threshold
				dustTx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100_000_000))}
				return NewMsgIntentOpen(
					dustTx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*below dust threshold.*",
		},
		{
			name: "unsupported THOR-native source asset should fail",
			setupMsg: func() *MsgIntentOpen {
				synthTx := GetRandomTx()
				synthTx.Coins = common.Coins{common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(100*common.One))}
				return NewMsgIntentOpen(
					synthTx,
					common.BTCAsset,
					GetRandomBTCAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*THOR-native asset BTC/BTC is not supported by intents.*",
		},
		{
			name: "unsupported THOR-native target asset should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset.GetSyntheticAsset(),
					GetRandomTHORAddress(),
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*THOR-native asset BTC/BTC is not supported by intents.*",
		},
		{
			name: "empty target address should fail",
			setupMsg: func() *MsgIntentOpen {
				return NewMsgIntentOpen(
					tx,
					common.BTCAsset,
					common.NoAddress, // empty address
					cosmos.NewUint(15*common.One),
					cosmos.NewUint(10*common.One),
					common.NoAddress,
					cosmos.ZeroUint(),
					w.activeNodeAccount.NodeAddress,
				)
			},
			expectedErrMsg: ".*target address cannot be empty.*",
		},
	}

	handler := NewIntentOpenHandler(NewDummyMgrWithKeeper(w.keeper))
	for _, tc := range testCases {
		msg := tc.setupMsg()
		_, err := handler.Run(w.ctx, msg)
		c.Assert(err, NotNil, Commentf("name:%s should have failed", tc.name))
		c.Check(err.Error(), Matches, tc.expectedErrMsg, Commentf("name:%s, got err: %v", tc.name, err))
	}
}

func (HandlerIntentOpenSuite) TestIntentOpen_KeeperErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	// Test SetIntent failure
	testKeeper := NewHandlerIntentOpenTestHelper(w.keeper)
	testKeeper.failToSetIntent = true
	handler := NewIntentOpenHandler(NewDummyMgrWithKeeper(testKeeper))
	// Note: SetIntent doesn't return error, so this won't fail the handler
	// but we test that the code path is covered
	_, err := handler.Run(w.ctx, msg)
	c.Check(err, IsNil) // Should succeed even if SetIntent is noop
}

func (HandlerIntentOpenSuite) TestIntentOpen_ExpiryCalculation(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with specific TTL
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 500)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
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

// FailingTradeAccountManager returns errors for testing
type FailingTradeAccountManager struct {
	DummyTradeAccountManager
}

func (f FailingTradeAccountManager) Withdrawal(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, txID common.TxID) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), errors.New("fail to withdraw trade asset")
}

// FailingSecuredAssetManager returns errors for testing
type FailingSecuredAssetManager struct {
	DummySecuredAssetManager
}

func (f FailingSecuredAssetManager) Withdraw(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (common.Coin, error) {
	return common.NoCoin, errors.New("fail to withdraw secured asset")
}

func (HandlerIntentOpenSuite) TestIntentOpen_SecuredAssetEscrow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with secured asset as source
	securedAsset := common.BTCAsset.GetSecuredAsset()
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(securedAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.NewUint(1000*common.One),
		cosmos.NewUint(900*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created with secured asset
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.Source.Asset.IsSecuredAsset(), Equals, true)
	c.Check(intent.Status, Equals, IntentActive)
}

func (HandlerIntentOpenSuite) TestIntentOpen_SecuredAssetEscrowError(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	// Use the failing secured asset manager
	mgr.securedMgr = FailingSecuredAssetManager{}
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with secured asset as source
	securedAsset := common.BTCAsset.GetSecuredAsset()
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(securedAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.NewUint(1000*common.One),
		cosmos.NewUint(900*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to escrow secured asset.*")

	// Verify intent was NOT created
	c.Check(w.keeper.IntentExists(w.ctx, tx.ID), Equals, false)
}

func (HandlerIntentOpenSuite) TestIntentOpen_TradeAssetEscrow(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentTTLBlocks.String(), 1000)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with trade asset as source
	tradeAsset := common.BTCAsset.GetTradeAsset()
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(tradeAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.NewUint(1000*common.One),
		cosmos.NewUint(900*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent was created with trade asset
	intent, err := w.keeper.GetIntent(w.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(intent.Source.Asset.IsTradeAsset(), Equals, true)
	c.Check(intent.Status, Equals, IntentActive)
}

func (HandlerIntentOpenSuite) TestIntentOpen_TradeAssetEscrowError(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create a BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	// Use the failing trade account manager
	mgr.tradeMgr = FailingTradeAccountManager{}
	handler := NewIntentOpenHandler(mgr)

	// Create an intent with trade asset as source
	tradeAsset := common.BTCAsset.GetTradeAsset()
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(tradeAsset, cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.NewUint(1000*common.One),
		cosmos.NewUint(900*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to escrow trade asset.*")

	// Verify intent was NOT created
	c.Check(w.keeper.IntentExists(w.ctx, tx.ID), Equals, false)
}

func (HandlerIntentOpenSuite) TestIntentOpen_RejectsNonChargeableAssets(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)

	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentMinFeeBasisPoints.String(), 50)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	securedAsset := common.BTCAsset.GetSecuredAsset()
	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(securedAsset, cosmos.NewUint(100*common.One))}

	msg := NewMsgIntentOpen(
		tx,
		common.DOGEAsset,
		GetRandomDOGEAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*at least one of source or target asset must be a fee-chargeable asset \\(active L1 pool asset or RUNE\\).*")
	c.Check(w.keeper.IntentExists(w.ctx, tx.ID), Equals, false)
}

func (HandlerIntentOpenSuite) TestIntentOpen_RagnarokBlocked(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Set ragnarok in progress using the keeper method
	w.keeper.SetRagnarokBlockHeight(w.ctx, 1)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*cannot create intents during ragnarok.*")
}

func (HandlerIntentOpenSuite) TestIntentOpen_CombinedFeesTooHigh(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with protocol fee that, combined with max affiliate fee, exceeds MaxBasisPts
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentMinFeeBasisPoints.String(), 9500) // 95% protocol fee

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	tx := GetRandomTx()
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		GetRandomTHORAddress(), // affiliate
		cosmos.NewUint(500),    // 5% affiliate fee → combined 100%
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*combined fee basis points.*")
}

func (HandlerIntentOpenSuite) TestIntentOpen_EmptyCoins(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentOpenHandler(mgr)

	// Create a tx with empty coins
	tx := GetRandomTx()
	tx.Coins = common.Coins{} // empty

	msg := NewMsgIntentOpen(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(15*common.One),
		cosmos.NewUint(10*common.One),
		common.NoAddress,
		cosmos.ZeroUint(),
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	// Error comes from ValidateBasic: "must have at least 1 coin"
	c.Check(err.Error(), Matches, ".*must have at least 1 coin.*")
}
