package common

import (
	"math/big"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type OraclePriceSuite struct{}

var _ = Suite(&OraclePriceSuite{})

func (s *OraclePriceSuite) TestNewOraclePrice(c *C) {
	testCases := []struct {
		Number           string
		ExpectedAmount   uint64
		ExpectedDecimals uint32
		ExpectedError    bool
	}{
		{
			Number:           "0.12",
			ExpectedAmount:   12,
			ExpectedDecimals: 2,
		},
		{
			Number:           "0.0000001",
			ExpectedAmount:   1,
			ExpectedDecimals: 7,
		},
		{
			Number:           "0.000000000238467",
			ExpectedAmount:   2384,
			ExpectedDecimals: 13,
		},
		{
			Number:           "0",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
		},
		{
			Number:           "4247545",
			ExpectedAmount:   4247545,
			ExpectedDecimals: 0,
		},
		{
			Number:           "337.00000000000",
			ExpectedAmount:   337,
			ExpectedDecimals: 0,
		},
		{
			Number:           "8473.67000000",
			ExpectedAmount:   847367,
			ExpectedDecimals: 2,
		},
		{
			Number:           "17.256",
			ExpectedAmount:   17256,
			ExpectedDecimals: 3,
		},
		{
			Number:           "488.1564861",
			ExpectedAmount:   4881564,
			ExpectedDecimals: 4,
		},
		{
			Number:           "5373.065",
			ExpectedAmount:   5373065,
			ExpectedDecimals: 3,
		},
		{
			Number:           "82646.00230323",
			ExpectedAmount:   82646002303,
			ExpectedDecimals: 6,
		},
		{
			Number:           "1.0020304",
			ExpectedAmount:   100203,
			ExpectedDecimals: 5,
		},
		{
			Number:           "684.00158",
			ExpectedAmount:   68400158,
			ExpectedDecimals: 5,
		},
		{
			Number:           "38738.0000072635252",
			ExpectedAmount:   38738000007263,
			ExpectedDecimals: 9,
		},
		{
			// integer already 18 chars long, no precision left
			Number:           "684682837625151837.827262",
			ExpectedAmount:   684682837625151837,
			ExpectedDecimals: 0,
		},
		{
			// integer 15 chars, 3 places left for precision
			Number:           "927365252827625.05468",
			ExpectedAmount:   927365252827625054,
			ExpectedDecimals: 3,
		},
		{
			// integer 14 chars, 4 places left for precision,only zeros
			Number:           "20474683987738.00005468",
			ExpectedAmount:   20474683987738,
			ExpectedDecimals: 0,
		},
		{
			// integer 19 chars, is too big
			Number:           "1636348227336527223",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
			ExpectedError:    true,
		},
		{
			// negative number should fail
			Number:           "-157.88",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
			ExpectedError:    true,
		},
	}

	for _, tc := range testCases {
		number, _, err := big.ParseFloat(tc.Number, 10, 128, big.ToNearestEven)
		c.Assert(err, IsNil, Commentf(tc.Number))

		price, err := NewOraclePrice(number)
		if tc.ExpectedError {
			c.Assert(err, NotNil, Commentf(tc.Number))
			c.Assert(price, IsNil)
		} else {
			c.Assert(err, IsNil, Commentf(tc.Number))
			c.Assert(price, NotNil, Commentf(tc.Number))
			c.Assert(price.Decimals, Equals, tc.ExpectedDecimals, Commentf(tc.Number))
			c.Assert(price.Amount, Equals, tc.ExpectedAmount, Commentf(tc.Number))
		}
	}
}

func (s *OraclePriceSuite) TestOraclePriceRatioOutput(c *C) {
	in := cosmos.NewUint(100 * One)

	// Equal prices settle 1:1.
	out, ok := OraclePriceRatioOutput(in, "1.00", "1.00")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(in), Equals, true)

	// Opposite band edges scale down and floor toward the reserve:
	// floor(100e8 * 0.99 / 1.01) = floor(990_000_000_000 / 101) = 9_801_980_198.
	out, ok = OraclePriceRatioOutput(in, "0.99", "1.01")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(cosmos.NewUint(9_801_980_198)), Equals, true)
	// The output is strictly less than a flat 1:1 fill, so the reserve never
	// overpays at the band edge.
	c.Check(out.LT(in), Equals, true)

	// Source worth more than target scales the output up.
	out, ok = OraclePriceRatioOutput(in, "1.01", "0.99")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(cosmos.NewUint(10_202_020_202)), Equals, true)

	// Fractional prices retain precision when converting asset amounts to RUNE.
	out, ok = OraclePriceRatioOutput(cosmos.NewUint(3*One), "2.5", "1.5")
	c.Assert(ok, Equals, true)
	c.Check(out.Equal(cosmos.NewUint(5*One)), Equals, true)

	// Unparsable or non-positive prices are rejected, not panicked on.
	for _, tc := range []struct{ src, tgt string }{
		{"1.00", "0"},     // zero target -> no division by zero
		{"0", "1.00"},     // zero source -> invalid price
		{"1.00", "-1.00"}, // negative target
		{"-1.00", "1.00"}, // negative source -> floored result must stay non-negative
		{"abc", "1.00"},   // unparsable source
		{"1.00", "abc"},   // unparsable target
	} {
		_, ok = OraclePriceRatioOutput(in, tc.src, tc.tgt)
		c.Check(ok, Equals, false, Commentf("src=%s tgt=%s", tc.src, tc.tgt))
	}
}
