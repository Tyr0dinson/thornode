package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerTradeAccountDeposit struct{}

var _ = Suite(&HandlerTradeAccountDeposit{})

func (HandlerTradeAccountDeposit) TestTradeAccountDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)

	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)

	bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "350")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)

	// Pass wrong message type
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Disable trade accounts
	mgr.Keeper().SetMimir(ctx, "TradeAccountsEnabled", 0)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade accounts are disabled.*")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositDepositDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Keep trade accounts enabled but disable deposit specifically
	mgr.Keeper().SetMimir(ctx, "TradeAccountsDepositEnabled", 0)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade accounts are disabled.*")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositChainLiquidityCap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Create 6 active nodes so BTC with 3 validators is below 2/3 threshold
	nodes := make(NodeAccounts, 6)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	FundModule(c, ctx, mgr.K, BondName, 600*common.One)

	// Create an active vault for BTC chain with 3 members (below 2/3 of 6)
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Type = AsgardVault
	vault.Chains = []string{common.BTCChain.String()}
	vault.Membership = []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(asset, cosmos.NewUint(1_00000000)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set up BTC pool: 1 BTC = 10,000 RUNE
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100_000_00000000)
	pool.BalanceAsset = cosmos.NewUint(10_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Configure mimirs: cap = 3 * 10,000 = 30,000 RUNE
	mgr.Keeper().SetMimir(ctx, constants.ChainSubBFTRatioNum.String(), 2)
	mgr.Keeper().SetMimir(ctx, constants.ChainSubBFTRatioDenom.String(), 3)
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 10_000_00000000)

	// Deposit exceeding cap: 2.5 BTC = 25,000 RUNE; current vault = 10,000 RUNE → total 35,000 > 30,000
	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(2_50000000), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil, Commentf("should reject trade deposit exceeding chain liquidity cap"))
	c.Check(err.Error(), Matches, ".*liquidity cap exceeded.*")

	// Deposit within cap: 0.5 BTC = 5,000 RUNE; current vault = 10,000 RUNE → total 15,000 < 30,000
	msg = NewMsgTradeAccountDeposit(asset, cosmos.NewUint(50000000), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("should allow trade deposit within chain liquidity cap"))
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositValidateBasicFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Empty asset should fail ValidateBasic
	msg := NewMsgTradeAccountDeposit(common.EmptyAsset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Zero amount should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.ZeroUint(), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// THORChain asset should fail
	msg = NewMsgTradeAccountDeposit(common.RuneNative, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty signer should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(350), addr, cosmos.AccAddress{}, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty tx ID should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(350), addr, addr, common.Tx{})
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
}
