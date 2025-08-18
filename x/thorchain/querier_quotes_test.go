package thorchain

import (
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type QuotesSuite struct{}

var _ = Suite(&QuotesSuite{})

func TestQuotes(t *testing.T) { TestingT(t) }

func (s *QuotesSuite) TestParseMultipleAffiliateParams(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test single affiliate
	affiliates, bps, totalBps, err := parseMultipleAffiliateParams(ctx, mgr, "affiliate1", "100")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 1)
	c.Assert(len(bps), Equals, 1)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(totalBps.Uint64(), Equals, uint64(100))

	// Test multiple affiliates with slash separation
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "affiliate1/affiliate2", "100/200")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(len(bps), Equals, 2)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(affiliates[1], Equals, "affiliate2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))
	c.Assert(totalBps.Uint64(), Equals, uint64(300))

	// Test three affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "50/75/25")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(50))
	c.Assert(bps[1].Uint64(), Equals, uint64(75))
	c.Assert(bps[2].Uint64(), Equals, uint64(25))
	c.Assert(totalBps.Uint64(), Equals, uint64(150))

	// Test single bps applied to multiple affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(100))
	c.Assert(bps[2].Uint64(), Equals, uint64(100))
	c.Assert(totalBps.Uint64(), Equals, uint64(300))

	// Test empty strings
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "", "")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 0)
	c.Assert(len(bps), Equals, 0)
	c.Assert(totalBps.Uint64(), Equals, uint64(0))

	// Test whitespace trimming
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, " a1 / a2 ", " 100 / 200 ")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))

	// Test empty parts in slash-separated string
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1//a3", "100//300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test mismatch between affiliates and bps (more affiliates than bps)
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2) // Only processes affiliates with corresponding bps
	c.Assert(len(bps), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")

	// Test invalid bps (should skip invalid ones)
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/invalid/300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2) // Skips a2 because of invalid bps
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test total bps exceeding limit (1000)
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "600/500")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "total affiliate fee must not be more than 1000 bps")

	// Test edge case: exactly at limit
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "500/500")
	c.Assert(err, IsNil)
	c.Assert(totalBps.Uint64(), Equals, uint64(1000))
}

func (s *QuotesSuite) TestQuoteHandleAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	amount := cosmos.NewUint(1000000000) // 1000 RUNE

	// Test single affiliate
	affiliate, memo, bps, newAmount, affAmt, err := quoteHandleAffiliate(ctx, mgr, "dx", "100", amount)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "dx")
	c.Assert(bps.Uint64(), Equals, uint64(100))
	c.Assert(newAmount.LT(amount), Equals, true) // Amount should be reduced by affiliate fee
	c.Assert(affAmt.GT(cosmos.ZeroUint()), Equals, true)

	// Test slash-separated (should take first values)
	affiliate, memo, bps, newAmount, affAmt, err = quoteHandleAffiliate(ctx, mgr, "dx/ss", "100/200", amount)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "dx")                // Should take first affiliate
	c.Assert(bps.Uint64(), Equals, uint64(100)) // Should take first bps

	// Test empty strings
	affiliate, memo, bps, newAmount, affAmt, err = quoteHandleAffiliate(ctx, mgr, "", "", amount)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "")
	c.Assert(bps.Uint64(), Equals, uint64(0))
	c.Assert(newAmount.Equal(amount), Equals, true) // Amount unchanged
	c.Assert(affAmt.Equal(cosmos.ZeroUint()), Equals, true)

	// Test whitespace trimming
	affiliate, memo, bps, newAmount, affAmt, err = quoteHandleAffiliate(ctx, mgr, " dx ", " 100 ", amount)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "dx")
	c.Assert(bps.Uint64(), Equals, uint64(100))

	// Test bps exceeding limit
	affiliate, memo, bps, newAmount, affAmt, err = quoteHandleAffiliate(ctx, mgr, "dx", "15000", amount)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "affiliate fee must be less than 10000 bps")

	// Test invalid bps
	affiliate, memo, bps, newAmount, affAmt, err = quoteHandleAffiliate(ctx, mgr, "dx", "invalid", amount)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "bad affiliate fee:.*")
}
