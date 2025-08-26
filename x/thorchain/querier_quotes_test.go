package thorchain

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
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
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, " a1 / a2 ", " 100 / 200 ")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))

	// Test empty parts in slash-separated string
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1//a3", "100//300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test mismatch between affiliates and bps (more affiliates than bps) - should use last BPS for extra affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3) // All affiliates should be processed
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))   // First BPS
	c.Assert(bps[1].Uint64(), Equals, uint64(200))   // Second BPS
	c.Assert(bps[2].Uint64(), Equals, uint64(200))   // Last BPS reused for third affiliate
	c.Assert(totalBps.Uint64(), Equals, uint64(500)) // 100 + 200 + 200

	// Test more affiliates than bps with multiple extra affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "xx/yy/zz/aa/bb", "10/20")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 5)
	c.Assert(len(bps), Equals, 5)
	c.Assert(affiliates[0], Equals, "xx")
	c.Assert(affiliates[1], Equals, "yy")
	c.Assert(affiliates[2], Equals, "zz")
	c.Assert(affiliates[3], Equals, "aa")
	c.Assert(affiliates[4], Equals, "bb")
	c.Assert(bps[0].Uint64(), Equals, uint64(10))   // First BPS
	c.Assert(bps[1].Uint64(), Equals, uint64(20))   // Second BPS
	c.Assert(bps[2].Uint64(), Equals, uint64(20))   // Last BPS reused
	c.Assert(bps[3].Uint64(), Equals, uint64(20))   // Last BPS reused
	c.Assert(bps[4].Uint64(), Equals, uint64(20))   // Last BPS reused
	c.Assert(totalBps.Uint64(), Equals, uint64(90)) // 10 + 20 + 20 + 20 + 20

	// Test affiliates with no BPS values (should skip all affiliates)
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 0) // All affiliates should be skipped
	c.Assert(len(bps), Equals, 0)
	c.Assert(totalBps.Uint64(), Equals, uint64(0))

	// Test invalid bps (should skip invalid ones)
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/invalid/300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2) // Skips a2 because of invalid bps
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test total bps exceeding limit (1000)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "600/500")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "total affiliate fee must not be more than 1000 bps")

	// Test edge case: exactly at limit
	_, _, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "500/500")
	c.Assert(err, IsNil)
	c.Assert(totalBps.Uint64(), Equals, uint64(1000))
}

func (s *QuotesSuite) TestQuoteHandleAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	amount := cosmos.NewUint(1000000000) // 1000 RUNE

	// Create valid THOR addresses for testing
	validAddr := GetRandomTHORAddress().String()
	validAddr2 := GetRandomTHORAddress().String()

	// Test affiliates with no BPS values (should skip all affiliates)
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 0) // All affiliates should be skipped
	c.Assert(len(bps), Equals, 0)
	c.Assert(totalBps.Uint64(), Equals, uint64(0))
}
