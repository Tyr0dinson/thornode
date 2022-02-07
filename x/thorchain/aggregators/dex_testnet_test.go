package aggregators

import (
	. "gopkg.in/check.v1"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
)

type DexAggregatorTestnetSuite struct{}

var _ = Suite(&DexAggregatorTestnetSuite{})

func (s *DexAggregatorTestnetSuite) TestDexAggregators(c *C) {
	ver := semver.MustParse("9999.0.0")

	// happy path
	addr, err := FetchDexAggregator(ver, common.ETHChain, "156")
	c.Assert(err, IsNil)
	c.Check(addr.String(), Equals, "0x69ba883Af416fF5501D54D5e27A1f497fBD97156")

	// unhappy path
	_, err = FetchDexAggregator(ver, common.BTCChain, "156")
	c.Assert(err, NotNil)
	_, err = FetchDexAggregator(ver, common.ETHChain, "foobar")
	c.Assert(err, NotNil)
}
