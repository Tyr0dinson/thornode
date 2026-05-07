package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type HelperBondYieldSuite struct{}

var _ = Suite(&HelperBondYieldSuite{})

func (s *HelperBondYieldSuite) TestBondYieldPaused(c *C) {
	ctx, mgr := setupManagerForTest(c)

	c.Check(isBondYieldPaused(ctx, mgr), Equals, false)

	mgr.Keeper().SetMimir(ctx, "PauseBondYield", 1)
	c.Check(isBondYieldPaused(ctx, mgr), Equals, true)

	mgr.Keeper().SetMimir(ctx, "PauseBondYield", 0)
	c.Check(isBondYieldPaused(ctx, mgr), Equals, false)
}

func (s *HelperBondYieldSuite) TestAutoClaimBondYieldRespectsPause(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(110 * common.One)

	bp := NewBondProviders(nodeAcc.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(providerAcc)}
	bp.Bond(cosmos.NewUint(100*common.One), providerAcc)
	c.Assert(bp.SetAutoClaim(providerAcc, 10000, common.EmptyAsset, common.NoAddress), Equals, true)
	bp.Providers[0].Bond = cosmos.NewUint(110 * common.One)
	bp.Providers[0].ClaimableYield = cosmos.NewUint(10 * common.One)

	FundModule(c, ctx, mgr.Keeper(), BondName, 110*common.One)

	mgr.Keeper().SetMimir(ctx, "PauseBondYield", 1)
	c.Assert(autoClaimBondYield(ctx, mgr, &nodeAcc, &bp), IsNil)

	c.Check(bp.Get(providerAcc).ClaimableYield.Equal(cosmos.NewUint(10*common.One)), Equals, true)
	c.Check(nodeAcc.Bond.Equal(cosmos.NewUint(110*common.One)), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)

	mgr.Keeper().SetMimir(ctx, "PauseBondYield", 0)
	c.Assert(autoClaimBondYield(ctx, mgr, &nodeAcc, &bp), IsNil)

	c.Check(bp.Get(providerAcc).ClaimableYield.IsZero(), Equals, true)
	c.Check(nodeAcc.Bond.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.Equal(cosmos.NewInt(10*common.One)), Equals, true)
}
