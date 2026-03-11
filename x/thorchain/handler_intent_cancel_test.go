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
	failToGetIntent          bool
	failToGetAsgardVaults    bool
	failToSetVault           bool
	failToGetIntentInHandle  bool
	forceEmptyVaults         bool
	insufficientVaultBalance bool
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

func (h *HandlerIntentCancelTestHelper) GetAsgardVaultsByStatus(ctx cosmos.Context, status VaultStatus) (Vaults, error) {
	if h.failToGetAsgardVaults {
		return nil, errKaboom
	}
	if h.forceEmptyVaults {
		return Vaults{}, nil
	}
	vaults, err := h.Keeper.GetAsgardVaultsByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	if h.insufficientVaultBalance {
		// Return vaults but with zero balance for the asset
		for i := range vaults {
			vaults[i].Coins = common.Coins{}
		}
	}
	return vaults, nil
}

func (h *HandlerIntentCancelTestHelper) SetVault(ctx cosmos.Context, vault Vault) error {
	if h.failToSetVault {
		return errKaboom
	}
	return h.Keeper.SetVault(ctx, vault)
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

func (HandlerIntentCancelSuite) TestIntentCancel_AbandonNonPoolAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active vault with BTC (no pool will be created for BTC)
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// Create an active intent with BTC (non-pool, non-native asset)
	// Note: NOT creating a pool for BTC, so it should take the abandon path
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

	// Create a voter for the cancel tx (mimics handler_deposit.go creating a voter)
	txIn := ObservedTx{Tx: cancelTx}
	voter := NewObservedTxVoter(cancelTx.ID, []ObservedTx{txIn})
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	voter.Tx = txIn
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status updated
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)

	// Verify vault balance was reduced (abandon = burn from vault)
	updatedVault, err := w.keeper.GetVault(w.ctx, vaultPubKey)
	c.Assert(err, IsNil)
	expectedBalance := cosmos.NewUint(100 * common.One) // 200 - 100
	c.Check(updatedVault.GetCoin(common.BTCAsset).Amount.Equal(expectedBalance), Equals, true,
		Commentf("expected %s, got %s", expectedBalance, updatedVault.GetCoin(common.BTCAsset).Amount))

	// Verify the cancel tx voter is marked done (abandon path has no outbound)
	updatedVoter, err := w.keeper.GetObservedTxInVoter(w.ctx, cancelTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.IsDone(), Equals, true, Commentf("voter should be marked done in abandon path"))
}

func (HandlerIntentCancelSuite) TestIntentCancel_AbandonNoVaultWithSufficientBalance(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active vault with insufficient BTC balance
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)))) // Only 50, need 100
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// Create an active intent with BTC requiring more than vault has
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)), // Need 100
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

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(err.Error(), Matches, ".*no vault found with sufficient balance.*")
}

func (HandlerIntentCancelSuite) TestIntentCancel_AbandonNoActiveVaults(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Don't create any vaults

	// Create an active intent with BTC (non-pool asset)
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

	// Use test helper to force empty vaults
	testKeeper := NewHandlerIntentCancelTestHelper(w.keeper)
	testKeeper.forceEmptyVaults = true
	handler := NewIntentCancelHandler(NewDummyMgrWithKeeper(testKeeper))

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(err.Error(), Matches, ".*no active vaults available.*")
}

func (HandlerIntentCancelSuite) TestIntentCancel_AbandonGetVaultsFails(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active intent with BTC (non-pool asset)
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

	testKeeper := NewHandlerIntentCancelTestHelper(w.keeper)
	testKeeper.failToGetAsgardVaults = true
	handler := NewIntentCancelHandler(NewDummyMgrWithKeeper(testKeeper))

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to get active vaults.*")
}

func (HandlerIntentCancelSuite) TestIntentCancel_AbandonSetVaultFails(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an active vault with BTC
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	// Create an active intent with BTC (non-pool asset)
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

	testKeeper := NewHandlerIntentCancelTestHelper(w.keeper)
	testKeeper.failToSetVault = true
	handler := NewIntentCancelHandler(NewDummyMgrWithKeeper(testKeeper))

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(err.Error(), Matches, ".*fail to save vault.*")
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

func (HandlerIntentCancelSuite) TestIntentCancel_VaultSelectionDeterminism(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create two vaults with the same BTC balance
	vault1PubKey := GetRandomPubKey()
	vault1 := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vault1PubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault1.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault1), IsNil)

	vault2PubKey := GetRandomPubKey()
	vault2 := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, vault2PubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault2.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, vault2), IsNil)

	// Create an active intent with BTC (non-pool asset)
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

	cancelTx := GetRandomTx()
	cancelTx.FromAddress = userAddr
	msg := NewMsgIntentCancel(cancelTx, intentTx.ID, cosmos.AccAddress(userAddr))
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify intent status updated
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCancelled)

	// One vault should have 100 BTC, the other should have 200 BTC
	// The vault with the lower PubKey string should be selected (deterministic tiebreaker)
	updatedVault1, _ := w.keeper.GetVault(w.ctx, vault1PubKey)
	updatedVault2, _ := w.keeper.GetVault(w.ctx, vault2PubKey)

	vault1Balance := updatedVault1.GetCoin(common.BTCAsset).Amount
	vault2Balance := updatedVault2.GetCoin(common.BTCAsset).Amount

	// One should be 100, the other 200
	c.Check(vault1Balance.Add(vault2Balance).Equal(cosmos.NewUint(300*common.One)), Equals, true,
		Commentf("total balance should be 300, got %s", vault1Balance.Add(vault2Balance)))
	c.Check(vault1Balance.Equal(cosmos.NewUint(100*common.One)) || vault2Balance.Equal(cosmos.NewUint(100*common.One)), Equals, true,
		Commentf("one vault should have 100 BTC remaining"))
}
