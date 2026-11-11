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

func (s *KeeperLiquidityProviderSuite) TestRagnarokOnlyZeroesSynthSupply(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	token, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	// a gas asset pool, a token pool, and a pool whose asset is a native coin
	pools := []common.Asset{common.BTCAsset, token, common.TCY}
	minted := []common.Asset{common.RuneAsset(), common.TCY}
	for _, asset := range []common.Asset{common.BTCAsset, token} {
		minted = append(minted, asset.GetSyntheticAsset(), asset.GetSecuredAsset())
	}
	for _, asset := range minted {
		c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(asset, cosmos.NewUint(100))), IsNil)
	}
	before := make(map[string]string, len(minted))
	for _, asset := range minted {
		supply := k.GetTotalSupply(ctx, asset)
		c.Assert(supply.IsZero(), Equals, false, Commentf("%s", asset))
		before[asset.String()] = supply.String()
	}

	for _, asset := range pools {
		k.SetPoolRagnarokStart(ctx, asset)
	}

	for _, asset := range minted {
		supply := k.GetTotalSupply(ctx, asset)
		if asset.IsSyntheticAsset() {
			c.Check(supply.IsZero(), Equals, true, Commentf("%s", asset))
			continue
		}
		c.Check(supply.String(), Equals, before[asset.String()], Commentf("%s", asset))
	}

	// once the Ragnarok finalizes the marker is gone and the synths read again
	for _, asset := range pools {
		k.DeletePoolRagnarokStart(ctx, asset)
	}
	for _, asset := range minted {
		c.Check(k.GetTotalSupply(ctx, asset).String(), Equals, before[asset.String()], Commentf("%s", asset))
	}
}
