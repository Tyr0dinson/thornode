package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type CloutSuite struct{}

var _ = Suite(&CloutSuite{})

func (s *CloutSuite) TestValid(c *C) {
	// Valid clout with non-empty address
	addr := GetRandomTHORAddress()
	clout := NewSwapperClout(addr)
	c.Assert(clout.Valid(), IsNil)

	// Invalid clout with empty address
	emptyClout := NewSwapperClout("")
	c.Assert(emptyClout.Valid(), NotNil)
	c.Check(emptyClout.Valid().Error(), Equals, "invalid swapper clout address: ")
}

func (s *CloutSuite) TestClout(c *C) {
	addr := GetRandomTHORAddress()

	clout := NewSwapperClout(addr)
	clout.Score = cosmos.NewUint(100)
	clout.Reclaimed = cosmos.NewUint(20)
	clout.Spent = cosmos.NewUint(65)

	c.Check(clout.Available().String(), Equals, "55")
	c.Check(clout.Claimable().String(), Equals, "45")
	clout.Reclaim(cosmos.NewUint(10))
	c.Check(clout.Reclaimed.String(), Equals, "30")

	clout.Reclaim(cosmos.NewUint(10000000))
	c.Check(clout.Reclaimed.String(), Equals, clout.Spent.String())

	clout = NewSwapperClout(addr)
	clout.Score = cosmos.NewUint(100)
	clout.Reclaimed = cosmos.NewUint(20)
	clout.Spent = cosmos.NewUint(65)
	clout.LastSpentHeight = 10
	clout.Restore(100, 100)
	c.Check(clout.Reclaimed.String(), Not(Equals), clout.Spent.String())
	clout.Restore(200, 100)
	c.Check(clout.Reclaimed.String(), Equals, clout.Spent.String())
}
