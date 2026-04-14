package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type KeeperMimirSuite struct{}

var _ = Suite(&KeeperMimirSuite{})

func (s *KeeperMimirSuite) TestMimir(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.SetMimir(ctx, "foo", 14)

	val, err := k.GetMimir(ctx, "foo")
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(14))

	val, err = k.GetMimir(ctx, "bogus")
	c.Assert(err, IsNil)
	c.Check(val, Equals, int64(-1))
}

func (s *KeeperMimirSuite) TestChainSupportMimirsOperational(c *C) {
	k := KVStore{}
	c.Assert(k.IsOperationalMimir(constants.MinNodeChainSupport.String()), Equals, true)
	c.Assert(k.IsOperationalMimir(constants.ChainLiquidityRunePerValidator.String()), Equals, true)
	c.Assert(k.IsOperationalMimir(constants.MinimumNodesForBFT.String()), Equals, true)
	c.Assert(k.IsOperationalMimir(constants.ChainSubBFTRatioNum.String()), Equals, true)
	c.Assert(k.IsOperationalMimir(constants.ChainSubBFTRatioDenom.String()), Equals, true)
}

func (s *KeeperMimirSuite) TestChainSupportMimirDefaultsWhenUnset(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(k.GetConfigInt64(ctx, constants.MinNodeChainSupport), Equals, int64(1))
	c.Assert(k.GetConfigInt64(ctx, constants.ChainLiquidityRunePerValidator), Equals, int64(10_000_00000000))
	c.Assert(k.GetConfigInt64(ctx, constants.MinimumNodesForBFT), Equals, int64(4))
	c.Assert(k.GetConfigInt64(ctx, constants.ChainSubBFTRatioNum), Equals, int64(2))
	c.Assert(k.GetConfigInt64(ctx, constants.ChainSubBFTRatioDenom), Equals, int64(3))
}
