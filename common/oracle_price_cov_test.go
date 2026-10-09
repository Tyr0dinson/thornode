package common

import (
	"math/big"

	. "gopkg.in/check.v1"
)

type OraclePriceCovSuite struct{}

var _ = Suite(&OraclePriceCovSuite{})

func (s *OraclePriceCovSuite) TestBigFloat(c *C) {
	p := &OraclePrice{Amount: 12345, Decimals: 2}
	c.Assert(p.BigFloat().Text('f', 2), Equals, "123.45")

	p = &OraclePrice{Amount: 7, Decimals: 0}
	c.Assert(p.BigFloat().Text('f', 0), Equals, "7")

	// round trip through NewOraclePrice
	in := big.NewFloat(1.5)
	op, err := NewOraclePrice(in)
	c.Assert(err, IsNil)
	c.Assert(op.BigFloat().Cmp(in), Equals, 0)

	_, err = NewOraclePrice(nil)
	c.Assert(err, NotNil)
}
