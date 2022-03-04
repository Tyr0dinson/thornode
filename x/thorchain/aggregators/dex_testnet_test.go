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
	addr, err := FetchDexAggregator(ver, common.ETHChain, "42D")
	c.Assert(err, IsNil)
	c.Check(addr.String(), Equals, "0x7fD9bd7A2Cab44820DD2874859E461640F04542D")

	// unhappy path
	_, err = FetchDexAggregator(ver, common.BTCChain, "156")
	c.Assert(err, NotNil)
	_, err = FetchDexAggregator(ver, common.ETHChain, "foobar")
	c.Assert(err, NotNil)
}
