package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type KeeperNetworkSuite struct{}

var _ = Suite(&KeeperNetworkSuite{})

func (KeeperNetworkSuite) TestNetwork(c *C) {
	ctx, k := setupKeeperForTest(c)
	vd, err := k.GetNetwork(ctx)
	c.Check(err, IsNil)
	c.Check(vd.BondRewardRune.Equal(cosmos.ZeroUint()), Equals, true)

	vd1 := NewNetwork()
	vd1.BondRewardRune = cosmos.NewUint(common.One * 100)
	err1 := k.SetNetwork(ctx, vd1)
	c.Assert(err1, IsNil)

	vd2, err2 := k.GetNetwork(ctx)
	c.Check(err2, IsNil)
	c.Check(vd2.BondRewardRune.Equal(vd1.BondRewardRune), Equals, true)
}

func (KeeperNetworkSuite) TestChainIncome(c *C) {
	ctx, k := setupKeeperForTest(c)

	// missing chain returns zero
	amt, err := k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.ZeroUint()), Equals, true)

	// add income to a new chain
	c.Assert(k.AddChainIncome(ctx, common.BTCChain, cosmos.NewUint(1000)), IsNil)
	amt, err = k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.NewUint(1000)), Equals, true)

	// add more income accumulates
	c.Assert(k.AddChainIncome(ctx, common.BTCChain, cosmos.NewUint(500)), IsNil)
	amt, err = k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.NewUint(1500)), Equals, true)

	// different chain is independent
	c.Assert(k.AddChainIncome(ctx, common.ETHChain, cosmos.NewUint(2000)), IsNil)
	amt, err = k.GetChainIncome(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.NewUint(2000)), Equals, true)
	// BTC unchanged
	amt, err = k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.NewUint(1500)), Equals, true)

	// zero chain income
	c.Assert(k.ZeroChainIncome(ctx, common.BTCChain), IsNil)
	amt, err = k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.ZeroUint()), Equals, true)
	// ETH still intact
	amt, err = k.GetChainIncome(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(cosmos.NewUint(2000)), Equals, true)

	// zero on non-existent chain is a no-op
	c.Assert(k.ZeroChainIncome(ctx, common.DOGEChain), IsNil)

	// overflow safety: large values
	large := cosmos.NewUintFromString("99999999999999999999")
	c.Assert(k.AddChainIncome(ctx, common.DOGEChain, large), IsNil)
	c.Assert(k.AddChainIncome(ctx, common.DOGEChain, cosmos.NewUint(1)), IsNil)
	amt, err = k.GetChainIncome(ctx, common.DOGEChain)
	c.Assert(err, IsNil)
	c.Assert(amt.Equal(large.Add(cosmos.NewUint(1))), Equals, true)
}

func (KeeperNetworkSuite) TestPOL(c *C) {
	ctx, k := setupKeeperForTest(c)
	pol, err := k.GetPOL(ctx)
	c.Check(err, IsNil)
	c.Check(pol.RuneDeposited.Equal(cosmos.ZeroUint()), Equals, true)

	pol.RuneDeposited = cosmos.NewUint(common.One * 100)
	err1 := k.SetPOL(ctx, pol)
	c.Assert(err1, IsNil)

	pol2, err2 := k.GetPOL(ctx)
	c.Check(err2, IsNil)
	c.Check(pol2.RuneDeposited.Uint64(), Equals, uint64(100*common.One))
}
