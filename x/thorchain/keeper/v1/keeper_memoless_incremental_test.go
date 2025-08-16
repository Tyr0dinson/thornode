package keeperv1

import (
	"testing"

	. "gopkg.in/check.v1"
)

type IncrementalCounterSuite struct{}

var _ = Suite(&IncrementalCounterSuite{})

func TestIncrementalCounterSuite(t *testing.T) {
	TestingT(t)
}

// RED PHASE: Test for incremental counter that should FAIL initially
func (s *IncrementalCounterSuite) TestGetNextReferenceID_Sequential(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Test sequential generation starting from 10000
	ref1, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	c.Assert(ref1, Equals, "10000")
	
	ref2, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	c.Assert(ref2, Equals, "10001")
	
	ref3, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	c.Assert(ref3, Equals, "10002")
}

func (s *IncrementalCounterSuite) TestGetNextReferenceID_Wraparound(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Set counter near maximum
	err := k.SetReferenceCounter(ctx, "BTC", 99998)
	c.Assert(err, IsNil)
	
	// Get next should be 99999
	ref1, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	c.Assert(ref1, Equals, "99999")
	
	// Next should wrap to 10000
	ref2, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	c.Assert(ref2, Equals, "10000")
}

func (s *IncrementalCounterSuite) TestReferenceCounter_Storage(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Test counter storage
	err := k.SetReferenceCounter(ctx, "ETH", 12345)
	c.Assert(err, IsNil)
	
	counter, err := k.GetReferenceCounter(ctx, "ETH")
	c.Assert(err, IsNil)
	c.Assert(counter, Equals, int64(12345))
}