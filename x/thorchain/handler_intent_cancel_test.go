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

func (HandlerIntentCancelSuite) TestIntentCancel_NonPoolAssetRefundsDirect(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for BTC so gas estimation works for the refund txout
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, btcPool), IsNil)

	// Create an active vault with BTC and gas asset
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// Create an active intent with BTC (non-pool for DOGE target, but BTC source has a pool)
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

	// Verify intent status is cancelled (direct refund, not auction)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)
}

func (HandlerIntentCancelSuite) TestIntentCancel_StagedPoolRefundsDirect(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an Available pool for DOGE so gas estimation works for refund
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active vault with DOGE
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.DOGEChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.DOGEAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

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
	c.Assert(err, IsNil)

	// Verify intent status is cancelled (direct refund, not auction)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)
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

func (HandlerIntentCancelSuite) TestIntentCancel_NonPoolAssetFailsWithoutGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Use an ERC20 token on ETH chain (no pool, not a gas asset)
	erc20Asset := common.Asset{Chain: common.ETHChain, Symbol: "TKN-0XDEADBEEF", Ticker: "TKN"}

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(erc20Asset, cosmos.NewUint(100*common.One)),
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

	// Cancel with the ERC20 token but no ETH gas tokens
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	cancelTx.Coins = common.Coins{common.NewCoin(erc20Asset, cosmos.NewUint(1))}
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, se.ErrInsufficientFunds), Equals, true, Commentf("expected ErrInsufficientFunds, got: %v", err))

	// Verify intent is still active
	stillActive, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(stillActive.Status, Equals, IntentActive)
}

func (HandlerIntentCancelSuite) TestIntentCancel_NonPoolAssetSucceedsWithGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Use an ERC20 token on ETH chain (no pool, not a gas asset)
	erc20Asset := common.Asset{Chain: common.ETHChain, Symbol: "TKN-0XDEADBEEF", Ticker: "TKN"}

	// Set up ETH pool (needed for gas estimation in TryAddTxOutItem)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, ethPool), IsNil)

	// Create an active vault with ETH and the ERC20
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
		common.NewCoin(erc20Asset, cosmos.NewUint(200*common.One)),
	))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(erc20Asset, cosmos.NewUint(100*common.One)),
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

	// Cancel with enough ETH gas (DummyGasManager returns 37500 for ETH)
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	cancelTx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(37500))}
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent is cancelled
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)

	// Verify the gas was booked into the ETH pool (pool balance increased)
	updatedPool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.GT(ethPool.BalanceAsset), Equals, true,
		Commentf("ETH pool asset balance should have increased after booking cancel gas"))
	c.Check(updatedPool.BalanceRune.LT(ethPool.BalanceRune), Equals, true,
		Commentf("ETH pool RUNE balance should have decreased after booking cancel gas"))
}

func (HandlerIntentCancelSuite) TestIntentCancel_ExcessGasRefundFailureRollsBack(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	erc20Asset := common.Asset{Chain: common.ETHChain, Symbol: "TKN-0XDEADBEEF", Ticker: "TKN"}

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, ethPool), IsNil)

	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
		common.NewCoin(erc20Asset, cosmos.NewUint(200*common.One)),
	))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(erc20Asset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	mgr.txOutStore = NewTxOutStoreFailOnCallDummy(1) // excess gas refund fails
	handler := NewIntentCancelHandler(mgr)

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	cancelTx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(150000))} // > 3x MaxGas
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*excess cancel gas refund.*")

	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentActive)

	updatedPool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.Equal(ethPool.BalanceAsset), Equals, true)
	c.Check(updatedPool.BalanceRune.Equal(ethPool.BalanceRune), Equals, true)
}

func (HandlerIntentCancelSuite) TestIntentCancel_LaterRefundFailureRollsBackBookedGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	erc20Asset := common.Asset{Chain: common.ETHChain, Symbol: "TKN-0XDEADBEEF", Ticker: "TKN"}

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, ethPool), IsNil)

	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
		common.NewCoin(erc20Asset, cosmos.NewUint(200*common.One)),
	))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(erc20Asset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 1000,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	mgr.txOutStore = NewTxOutStoreFailOnCallDummy(2) // excess refund succeeds, source refund fails
	handler := NewIntentCancelHandler(mgr)

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	cancelTx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(150000))} // > 3x MaxGas
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)

	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentActive)

	updatedPool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.Equal(ethPool.BalanceAsset), Equals, true)
	c.Check(updatedPool.BalanceRune.Equal(ethPool.BalanceRune), Equals, true)
}

func (HandlerIntentCancelSuite) TestIntentCancel_PoolAssetNoGasBooking(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for DOGE (Available pool = gas booking not needed)
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an active vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.DOGEChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.DOGEAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

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

	// Pool-backed assets should NOT have gas booked (pool balance unchanged)
	updatedPool, err := w.keeper.GetPool(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true,
		Commentf("pool-backed cancel should not book gas into pool"))
}

func (HandlerIntentCancelSuite) TestIntentCancel_GasAssetNoPoolSkipsGasCheck(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// BTC is the gas asset for BTC chain — no BTC pool exists
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
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

	// Cancel with BTC (the source asset) but no extra gas tokens
	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	cancelTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))

	// The gas check should be skipped (BTC is the gas asset).
	// May fail for other reasons (no pool/vault for TryAddTxOutItem), but NOT ErrInsufficientFunds.
	_, err := handler.Run(w.ctx, msg)
	if err != nil {
		c.Check(errors.Is(err, se.ErrInsufficientFunds), Equals, false,
			Commentf("gas asset source should skip gas check, but got: %v", err))
	}
}
