package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
)

type THORNameSuite struct{}

var _ = Suite(&THORNameSuite{})

func (THORNameSuite) TestTHORName(c *C) {
	n := NewTHORName("iamthewalrus", common.BNBChain, GetRandomTHORAddress(), 0)
	c.Check(n.Valid(), IsNil)
	n1 := NewTHORName("", common.BNBChain, GetRandomTHORAddress(), 0)
	c.Check(n1.Valid(), NotNil)
	n2 := NewTHORName("hello", common.EmptyChain, GetRandomTHORAddress(), 0)
	c.Check(n2.Valid(), NotNil)
	n3 := NewTHORName("hello", common.THORChain, common.Address(""), 0)
	c.Check(n3.Valid(), NotNil)
}
