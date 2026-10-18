package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gopkg.in/check.v1"
)

type HelpersPOLReserveSuite struct{}

var _ = Suite(&HelpersPOLReserveSuite{})

const polReserveTestUSDT = "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7"

func polReserveTestUSDTAsset(c *C) common.Asset {
	asset, err := common.NewAsset(polReserveTestUSDT)
	c.Assert(err, IsNil)
	return asset
}

func (s *HelpersPOLReserveSuite) TestResolvePOLPoolMimir(c *C) {
	ctx, mgr := setupManagerForTest(c)
	usdt := polReserveTestUSDTAsset(c)
	polReserveSetPool(c, ctx, mgr, common.BTCAsset, 1000, 10)
	polReserveSetPool(c, ctx, mgr, common.ETHAsset, 1000, 10)
	polReserveSetPool(c, ctx, mgr, usdt, 1000, 1000)

	_, ok := resolvePOLPoolMimir(ctx, mgr, systemIncomePOLPoolMimirPrefix)
	c.Check(ok, Equals, false, Commentf("nothing set"))

	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-BTC-BTC", 1)
	pool, ok := resolvePOLPoolMimir(ctx, mgr, systemIncomePOLPoolMimirPrefix)
	c.Assert(ok, Equals, true)
	c.Check(pool.Asset.Equals(common.BTCAsset), Equals, true)

	// token assets keep the hyphen between ticker and contract
	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-BTC-BTC", 0)
	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", 1)
	pool, ok = resolvePOLPoolMimir(ctx, mgr, systemIncomePOLPoolMimirPrefix)
	c.Assert(ok, Equals, true)
	c.Check(pool.Asset.Equals(usdt), Equals, true)

	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-BTC-BTC", 1)
	_, ok = resolvePOLPoolMimir(ctx, mgr, systemIncomePOLPoolMimirPrefix)
	c.Check(ok, Equals, false, Commentf("two pools set"))

	// a different prefix is resolved independently, and an unknown pool resolves to none
	mgr.Keeper().SetMimir(ctx, "DrainPOLPool-SOL-SOL", 1)
	_, ok = resolvePOLPoolMimir(ctx, mgr, drainPOLPoolMimirPrefix)
	c.Check(ok, Equals, false, Commentf("unknown pool"))
}

func (s *HelpersPOLReserveSuite) TestActivePOLDrain(c *C) {
	ctx, mgr := setupManagerForTest(c)
	polReserveSetPool(c, ctx, mgr, common.BTCAsset, 1000, 10)

	_, _, active := activePOLDrain(ctx, mgr)
	c.Check(active, Equals, false)

	mgr.Keeper().SetMimir(ctx, "DrainPOLPool-BTC-BTC", 1)
	mgr.Keeper().SetMimir(ctx, constants.DrainPOLOn.String(), 1)
	_, _, active = activePOLDrain(ctx, mgr)
	c.Check(active, Equals, false, Commentf("DrainRunePerBlock unset"))

	mgr.Keeper().SetMimir(ctx, constants.DrainRunePerBlock.String(), 250)
	pool, rate, active := activePOLDrain(ctx, mgr)
	c.Assert(active, Equals, true)
	c.Check(pool.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(rate.Uint64(), Equals, uint64(250))

	// a stored rate above the cap (e.g. set before it existed) is clamped to 100 RUNE
	mgr.Keeper().SetMimir(ctx, constants.DrainRunePerBlock.String(), 1_000*common.One)
	_, rate, active = activePOLDrain(ctx, mgr)
	c.Assert(active, Equals, true)
	c.Check(rate.Uint64(), Equals, uint64(100*common.One))

	mgr.Keeper().SetMimir(ctx, constants.DrainPOLOn.String(), 0)
	_, _, active = activePOLDrain(ctx, mgr)
	c.Check(active, Equals, false)
}

func (s *HelpersPOLReserveSuite) TestValidatePOLSelectAlgoVote(c *C) {
	ctx, mgr := setupManagerForTest(c)
	polReserveSetPool(c, ctx, mgr, common.BTCAsset, 1000, 10)
	key := "POLSELECTALGO"

	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 1), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, -1), IsNil, Commentf("vote withdrawal"))
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 0), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 3), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 2), NotNil, Commentf("no SystemIncomePOLPool"))

	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-BTC-BTC", 1)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 2), IsNil)

	// the selected pool has to be eligible when algo 2 is switched on
	mgr.Keeper().SetMimir(ctx, "PauseLPBTC", ctx.BlockHeight())
	c.Check(validatePOLReserveMimirVote(ctx, mgr, key, 2), NotNil)
}

func (s *HelpersPOLReserveSuite) TestValidatePOLCycleAndRateVotes(c *C) {
	ctx, mgr := setupManagerForTest(c)

	c.Check(validatePOLReserveMimirVote(ctx, mgr, "POLCYCLE", 0), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "POLCYCLE", 1), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "POLCYCLE", 14400), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINRUNEPERBLOCK", 0), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINRUNEPERBLOCK", 100*common.One), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINRUNEPERBLOCK", 100*common.One+1), NotNil, Commentf("above the 100 RUNE cap"))
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINRUNEPERBLOCK", -1), IsNil, Commentf("vote withdrawal"))
}

func (s *HelpersPOLReserveSuite) TestValidateSystemIncomePOLPoolVote(c *C) {
	ctx, mgr := setupManagerForTest(c)
	usdt := polReserveTestUSDTAsset(c)
	polReserveSetPool(c, ctx, mgr, common.BTCAsset, 1000, 10)
	polReserveSetPool(c, ctx, mgr, common.ETHAsset, 1000, 10)
	polReserveSetPool(c, ctx, mgr, usdt, 1000, 1000)
	btcKey := "SYSTEMINCOMEPOLPOOL-BTC-BTC"

	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 2), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 1), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "SYSTEMINCOMEPOLPOOL-SOL-SOL", 1), NotNil, Commentf("no SOL pool"))
	// USDT is neither a gas asset, a TOR anchor nor whitelisted
	usdtKey := "SYSTEMINCOMEPOLPOOL-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7"
	c.Check(validatePOLReserveMimirVote(ctx, mgr, usdtKey, 1), NotNil)
	mgr.Keeper().SetMimir(ctx, "POLReserveWhitelist-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", 1)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, usdtKey, 1), IsNil)

	// one pool at a time: another set pool must be unset first
	mgr.Keeper().SetMimir(ctx, "SystemIncomePOLPool-ETH-ETH", 1)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 1), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "SYSTEMINCOMEPOLPOOL-ETH-ETH", 1), IsNil, Commentf("re-voting the set pool"))
	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 0), IsNil)
}

func (s *HelpersPOLReserveSuite) TestValidateDrainVotes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	polReserveSetPool(c, ctx, mgr, common.BTCAsset, 1000, 10)
	polReserveSetPool(c, ctx, mgr, common.ETHAsset, 1000, 10)
	btcKey := "DRAINPOLPOOL-BTC-BTC"

	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 1), NotNil, Commentf("no POL Reserve liquidity"))

	polAddress, err := mgr.Keeper().GetModuleAddress(POLReserveName)
	c.Assert(err, IsNil)
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       polAddress,
		Units:             cosmos.NewUint(100 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
	})
	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 1), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, btcKey, 2), NotNil)

	// DrainPOLOn needs both the pool and the rate already set
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLON", 2), NotNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLON", 0), IsNil)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLON", 1), NotNil)
	mgr.Keeper().SetMimir(ctx, btcKey, 1)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLON", 1), NotNil, Commentf("DrainRunePerBlock unset"))
	mgr.Keeper().SetMimir(ctx, constants.DrainRunePerBlock.String(), common.One)
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLON", 1), IsNil)

	// one drain pool at a time
	c.Check(validatePOLReserveMimirVote(ctx, mgr, "DRAINPOLPOOL-ETH-ETH", 1), NotNil)
}

func (s *HelpersPOLReserveSuite) TestMimirHandlerRejectsPOLReserveVote(c *C) {
	ctx, mgr := setupManagerForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	handler := NewMimirHandler(mgr)

	err := handler.validate(ctx, *NewMsgMimir("POLSelectAlgo", 2, na.NodeAddress))
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*SystemIncomePOLPool.*")

	c.Check(handler.validate(ctx, *NewMsgMimir("POLSelectAlgo", 1, na.NodeAddress)), IsNil)
}
