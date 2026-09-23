package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerSecuredAssetDeposit struct{}

var _ = Suite(&HandlerSecuredAssetDeposit{})

func (HandlerSecuredAssetDeposit) TestSecuredAssetDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	pool, err := mgr.K.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	pool.Asset = asset
	err = mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil)

	bal := mgr.SecuredAssetManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "350")

	bankBals := mgr.coinKeeper.GetAllBalances(ctx, addr)
	expected := cosmos.NewCoins(cosmos.NewCoin(asset.GetSecuredAsset().Native(), cosmos.NewInt(350)))
	c.Check(bankBals.String(), Equals, expected.String())
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)

	// Pass wrong message type
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositHaltDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool
	pool := NewPool()
	pool.Asset = asset
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Set mimir to halt secured asset deposits for BTC
	// The template is "HaltSecuredDeposit-%s" with chain string
	haltKey := fmt.Sprintf("HaltSecuredDeposit-%s", asset.Chain)
	mgr.Keeper().SetMimir(ctx, haltKey, 1) // Set to block height 1, current is 18

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*secured asset deposits are disabled.*")
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositPoolNotAvailable(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set pool to Staged status (not Available)
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolStaged
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*pool.*unavailable.*")
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositTVLCap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool with balances
	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Enable strict bond liquidity ratio to trigger TVL cap
	mgr.constAccessor = constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.MaximumLiquidityRune: 1, // Very low cap
	}, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, map[constants.ConstantName]string{})

	// Set up an asgard vault with assets to push TVL over the cap
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(asset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(mgr.K.SetVault(ctx, vault), IsNil)

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*secured deposits more than bond.*")
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositTVLCapOraclePrice(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(150 * common.One)
	c.Assert(mgr.K.SetNodeAccount(ctx, node), IsNil)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, nil)

	amount := cosmos.NewUint(100 * common.One)
	coins := common.NewCoins(common.NewCoin(asset, amount))
	// The pool values this deposit at 100 RUNE, below the 150 RUNE bond.
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
	c.Check(validateSecuredAssetDeposit(ctx, mgr, asset, amount), IsNil)

	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "4"}), IsNil)
	// With neither oracle nor TOR RUNE pricing, use the direct pool ratio.
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)

	// A TOR anchor provides $2.50 per RUNE even when the RUNE feed is absent.
	anchor := NewPool()
	anchor.Asset = common.USDCAsset
	anchor.BalanceRune = cosmos.NewUint(100 * common.One)
	anchor.BalanceAsset = cosmos.NewUint(250 * common.One)
	c.Assert(mgr.K.SetPool(ctx, anchor), IsNil)
	mgr.K.SetMimir(ctx, fmt.Sprintf("TorAnchor-%s-%s", anchor.Asset.Chain, anchor.Asset.Symbol), 1)
	c.Assert(mgr.K.DollarsPerRune(ctx).String(), Equals, "250000000")
	// BTC's oracle price and RUNE's TOR price value the deposit at 160 RUNE.
	c.Check(atTVLCap(ctx, coins, mgr), Equals, true)
	c.Check(validateSecuredAssetDeposit(ctx, mgr, asset, amount), NotNil)

	// An available RUNE oracle takes priority over TOR: 100 * $4 / $4 = 100 RUNE.
	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "RUNE", Price: "4"}), IsNil)
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "RUNE", Price: "2"}), IsNil)
	// The oracle values the same deposit at 200 RUNE.
	c.Check(atTVLCap(ctx, coins, mgr), Equals, true)
	c.Check(validateSecuredAssetDeposit(ctx, mgr, asset, amount), NotNil)

	mgr.K.DelPrice(ctx, "BTC")
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "invalid"}), IsNil)
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "0"}), IsNil)
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
	c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "4"}), IsNil)
	for _, invalidPrice := range []string{"0", "-1", "invalid"} {
		c.Assert(mgr.K.SetPrice(ctx, OraclePrice{Symbol: "RUNE", Price: invalidPrice}), IsNil)
		c.Check(atTVLCap(ctx, coins, mgr), Equals, true, Commentf("RUNE price=%s", invalidPrice))
	}

	// When both feeds are missing, the pool ratio still values the deposit.
	mgr.K.DelPrice(ctx, "BTC")
	mgr.K.DelPrice(ctx, "RUNE")
	c.Check(atTVLCap(ctx, coins, mgr), Equals, false)
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositValidateBasicFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool so we get past pool checks
	pool := NewPool()
	pool.Asset = common.BTCAsset
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Empty asset should fail ValidateBasic
	msg := NewMsgSecuredAssetDeposit(common.EmptyAsset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Zero amount should fail
	msg = NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.ZeroUint(), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Native asset should fail
	msg = NewMsgSecuredAssetDeposit(common.RuneNative, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty signer should fail
	msg = NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(350), addr, cosmos.AccAddress{}, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty tx ID should fail
	msg = NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(350), addr, addr, common.Tx{})
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositHaltNotActive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool
	pool := NewPool()
	pool.Asset = asset
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Set mimir halt value to a future block height (should NOT halt)
	haltKey := fmt.Sprintf("HaltSecuredDeposit-%s", asset.Chain)
	mgr.Keeper().SetMimir(ctx, haltKey, 100) // block height 100 > current 18, so not active

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil) // Should succeed since halt is for future
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositHaltNegative(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool
	pool := NewPool()
	pool.Asset = asset
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Set mimir halt value to -1 (disabled)
	haltKey := fmt.Sprintf("HaltSecuredDeposit-%s", asset.Chain)
	mgr.Keeper().SetMimir(ctx, haltKey, -1) // Negative value should not halt

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil) // Should succeed since halt value is negative
}

func (HandlerSecuredAssetDeposit) TestSecuredAssetDepositHandleError(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Set up a valid pool so validation passes
	pool := NewPool()
	pool.Asset = asset
	c.Assert(mgr.K.SetPool(ctx, pool), IsNil)

	// Set global secured asset halt to trigger error in Deposit (handle path)
	// This is a different halt from the per-chain halt checked in validate
	mgr.Keeper().SetMimir(ctx, "HaltSecuredGlobal", 1) // block height 1 <= current 18

	msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*secured assets are disabled.*")
}
