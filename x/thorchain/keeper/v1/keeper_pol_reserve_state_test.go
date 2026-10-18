package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gopkg.in/check.v1"
)

func (s *KeeperPOLReserveSuite) TestPOLCycleLiquidityFeeAccruesAndResets(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)
	c.Assert(k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(50)), IsNil)
	c.Assert(k.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(7)), IsNil)

	fee, err := k.GetPOLCycleLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(fee, Equals, uint64(150))
	fee, err = k.GetPOLCycleLiquidityFee(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(fee, Equals, uint64(7))

	k.ResetPOLCycleLiquidityFees(ctx)

	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		fee, err = k.GetPOLCycleLiquidityFee(ctx, asset)
		c.Assert(err, IsNil)
		c.Check(fee, Equals, uint64(0), Commentf("%s", asset))
	}

	// the PoolCycle rolling fee is tracked separately and is not reset
	rolling, err := k.GetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolling, Equals, uint64(150))
}

func (s *KeeperPOLReserveSuite) TestPOLReserveTarget(c *C) {
	ctx, k := setupKeeperForTest(c)

	_, found, err := k.GetPOLReserveTarget(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	k.SetPOLReserveTarget(ctx, common.ETHAsset)
	asset, found, err := k.GetPOLReserveTarget(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(asset.Equals(common.ETHAsset), Equals, true)

	k.ClearPOLReserveTarget(ctx)
	_, found, err = k.GetPOLReserveTarget(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
}

func (s *KeeperPOLReserveSuite) TestPOLReserveDrainState(c *C) {
	ctx, k := setupKeeperForTest(c)

	_, _, found, err := k.GetPOLReserveDrainState(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	k.SetPOLReserveDrainState(ctx, common.BTCAsset, cosmos.NewUint(500))
	asset, owed, found, err := k.GetPOLReserveDrainState(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(asset.Equals(common.BTCAsset), Equals, true)
	c.Check(owed.Uint64(), Equals, uint64(500))

	k.SetPOLReserveDrainState(ctx, common.BTCAsset, cosmos.ZeroUint())
	asset, owed, found, err = k.GetPOLReserveDrainState(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(asset.Equals(common.BTCAsset), Equals, true)
	c.Check(owed.IsZero(), Equals, true)

	k.SetPOLReserveDrainState(ctx, common.ETHAsset, cosmos.NewUint(7))
	asset, owed, found, err = k.GetPOLReserveDrainState(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(asset.Equals(common.ETHAsset), Equals, true)
	c.Check(owed.Uint64(), Equals, uint64(7))

	k.ClearPOLReserveDrainState(ctx)
	_, owed, found, err = k.GetPOLReserveDrainState(ctx)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
	c.Check(owed.IsZero(), Equals, true)
}

func (s *KeeperMimirSuite) TestListMimirsWithPrefix(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.SetMimir(ctx, "SystemIncomePOLPool-ETH-ETH", 0)
	k.SetMimir(ctx, "systemincomepolpool-btc-btc", 1)
	k.SetMimir(ctx, "SystemIncomePOLPoolX", 5)
	k.SetMimir(ctx, "DrainPOLPool-BTC-BTC", 1)

	mimirs, err := k.ListMimirsWithPrefix(ctx, "SystemIncomePOLPool-")
	c.Assert(err, IsNil)
	c.Assert(mimirs, HasLen, 2)
	// store order, upper-case keys
	c.Check(mimirs[0].Key, Equals, "SYSTEMINCOMEPOLPOOL-BTC-BTC")
	c.Check(mimirs[0].Value, Equals, int64(1))
	c.Check(mimirs[1].Key, Equals, "SYSTEMINCOMEPOLPOOL-ETH-ETH")
	c.Check(mimirs[1].Value, Equals, int64(0))

	mimirs, err = k.ListMimirsWithPrefix(ctx, "DRAINPOLPOOL-")
	c.Assert(err, IsNil)
	c.Assert(mimirs, HasLen, 1)
	c.Check(mimirs[0].Key, Equals, "DRAINPOLPOOL-BTC-BTC")

	mimirs, err = k.ListMimirsWithPrefix(ctx, "NOSUCHPREFIX-")
	c.Assert(err, IsNil)
	c.Check(mimirs, HasLen, 0)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveSelection(c *C) {
	_, k := setupKeeperForTest(c)

	for _, key := range []string{
		constants.POLSelectAlgo.String(),
		constants.POLCycle.String(),
		constants.DrainPOLOn.String(),
		constants.DrainRunePerBlock.String(),
		"POLSELECTALGO",
		"SystemIncomePOLPool-SOL-SOL",
		"systemincomepolpool-btc-btc",
		"DrainPOLPool-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	} {
		c.Check(k.IsOperationalMimir(key), Equals, true, Commentf("%s", key))
	}
	// POLReserveMaxDeployment stays an economic mimir
	c.Check(k.IsOperationalMimir(constants.POLReserveMaxDeployment.String()), Equals, false)
}
