package common

// If package cosmos, 'Result already declared' error.

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type CosmosSuite struct{}

var _ = Suite(&CosmosSuite{})

func (s CosmosSuite) TestRoundToDecimal(c *C) {
	amt := cosmos.NewUint(9_12345678)

	dec := int64(0)
	rounded := cosmos.RoundToDecimal(amt, dec)
	c.Check(rounded.Equal(cosmos.NewUint(9_12345678)), Equals, true, Commentf("%s", rounded.String()))
	// '0' doesn't change the number.

	dec = int64(8)
	rounded = cosmos.RoundToDecimal(amt, dec)
	c.Check(rounded.Equal(cosmos.NewUint(9_12345678)), Equals, true, Commentf("%s", rounded.String()))
	// '8' doesn't change the number.

	dec = int64(2)
	rounded = cosmos.RoundToDecimal(amt, dec)
	c.Check(rounded.Equal(cosmos.NewUint(9_12000000)), Equals, true, Commentf("%s", rounded.String()))
	// '2' modfies the 8-decimal number to be 2-decimal.

	dec = int64(6)
	rounded = cosmos.RoundToDecimal(amt, dec)
	c.Check(rounded.Equal(cosmos.NewUint(9_12345600)), Equals, true, Commentf("%s", rounded.String()))
	// RoundToDecimal reliably always floors (avoiding overshooting), never rounding up.
}
