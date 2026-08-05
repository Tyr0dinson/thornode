package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type KeeperLiquidityProviderSuite struct{}

var _ = Suite(&KeeperLiquidityProviderSuite{})

func (mas *KeeperLiquidityProviderSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *KeeperLiquidityProviderSuite) TestLiquidityProvider(c *C) {
	ctx, k := setupKeeperForTest(c)
	asset := common.ETHAsset

	lp, err := k.GetLiquidityProvider(ctx, asset, GetRandomRUNEAddress())
	c.Assert(err, IsNil)
	c.Check(lp.PendingRune, NotNil)
	c.Check(lp.Units, NotNil)

	lp = LiquidityProvider{
		Asset:        asset,
		Units:        cosmos.NewUint(12),
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomBTCAddress(),
	}
	k.SetLiquidityProvider(ctx, lp)
	lp, err = k.GetLiquidityProvider(ctx, asset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Asset.Equals(asset), Equals, true)
	c.Check(lp.Units.Equal(cosmos.NewUint(12)), Equals, true)
	iter := k.GetLiquidityProviderIterator(ctx, common.ETHAsset)
	c.Check(iter, NotNil)
	iter.Close()
	k.RemoveLiquidityProvider(ctx, lp)
}

func (s *KeeperLiquidityProviderSuite) TestGetTotalSupplyAboveUint64(c *C) {
	ctx, k := setupKeeperForTest(c)
	amount := cosmos.NewUintFromString("2000000000000000000000")

	for _, asset := range []common.Asset{
		common.XECAsset.GetSyntheticAsset(),
		common.XECAsset.GetSecuredAsset(),
	} {
		c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(asset, amount)), IsNil)
		c.Assert(k.GetTotalSupply(ctx, asset).Equal(amount), Equals, true,
			Commentf("supply for %s was narrowed", asset))
	}
}
