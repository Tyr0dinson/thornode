package helper

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

func TestHelperPackage(t *testing.T) { TestingT(t) }

type HelperTestSuite struct{}

var _ = Suite(&HelperTestSuite{})

// ---------------------------------------------------------------------------
// mockBridge — only GetAsgardAddresses is called by Helper
// ---------------------------------------------------------------------------

type mockBridge struct {
	thorclient.ThorchainBridge // embedded (nil) — only GetAsgardAddresses is called
	addresses                  []common.Address
	err                        error
	callCount                  atomic.Int32
}

func (m *mockBridge) GetAsgardAddresses(_ common.Chain) ([]common.Address, error) {
	m.callCount.Add(1)
	return m.addresses, m.err
}

// newTestHelper creates a Helper with the given mock bridge.
func newTestHelper(bridge *mockBridge) *Helper {
	h := NewHelper(zerolog.Nop(), common.ADAChain, bridge)
	return &h
}

// ---------------------------------------------------------------------------
// UpdateAsgardAddresses
// ---------------------------------------------------------------------------

func (s *HelperTestSuite) TestUpdateAsgardAddresses(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1"},
	}
	h := newTestHelper(bridge)

	err := h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)
	c.Assert(int(bridge.callCount.Load()), Equals, 1)

	// addresses should be populated
	c.Assert(h.asgardAddresses["addr1"], Equals, true)
}

func (s *HelperTestSuite) TestUpdateAsgardAddressesCacheHit(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1"},
	}
	h := newTestHelper(bridge)

	err := h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)
	c.Assert(int(bridge.callCount.Load()), Equals, 1)

	// second call within TTL should use cache
	err = h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)
	c.Assert(int(bridge.callCount.Load()), Equals, 1) // still 1, cache hit
}

func (s *HelperTestSuite) TestUpdateAsgardAddressesCacheExpiry(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1"},
	}
	h := newTestHelper(bridge)

	err := h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)
	c.Assert(int(bridge.callCount.Load()), Equals, 1)

	// wait for cache to expire (ThorchainBlockTime = 1s on mocknet)
	time.Sleep(constants.ThorchainBlockTime + 100*time.Millisecond)

	err = h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)
	c.Assert(int(bridge.callCount.Load()), Equals, 2) // cache expired, fetched again
}

func (s *HelperTestSuite) TestUpdateAsgardAddressesBridgeError(c *C) {
	bridge := &mockBridge{
		err: fmt.Errorf("bridge unavailable"),
	}
	h := newTestHelper(bridge)

	err := h.UpdateAsgardAddresses()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "bridge unavailable")
}

func (s *HelperTestSuite) TestUpdateAsgardAddressesEmpty(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{},
	}
	h := newTestHelper(bridge)

	err := h.UpdateAsgardAddresses()
	c.Assert(err, IsNil)

	// no addresses populated, IsAsgardAddress returns false
	c.Assert(h.IsAsgardAddress("anything"), Equals, false)
}

// ---------------------------------------------------------------------------
// IsAsgardAddress
// ---------------------------------------------------------------------------

func (s *HelperTestSuite) TestIsAsgardAddressFound(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1"},
	}
	h := newTestHelper(bridge)

	c.Assert(h.IsAsgardAddress("addr1"), Equals, true)
}

func (s *HelperTestSuite) TestIsAsgardAddressNotFound(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1"},
	}
	h := newTestHelper(bridge)

	c.Assert(h.IsAsgardAddress("addr2"), Equals, false)
}

func (s *HelperTestSuite) TestIsAsgardAddressBridgeError(c *C) {
	bridge := &mockBridge{
		err: fmt.Errorf("network error"),
	}
	h := newTestHelper(bridge)

	// error from bridge → returns false
	c.Assert(h.IsAsgardAddress("addr1"), Equals, false)
}

func (s *HelperTestSuite) TestIsAsgardAddressConcurrent(c *C) {
	bridge := &mockBridge{
		addresses: []common.Address{"addr1", "addr2", "addr3"},
	}
	h := newTestHelper(bridge)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.IsAsgardAddress("addr1")
			_ = h.IsAsgardAddress("addr2")
			_ = h.IsAsgardAddress("unknown")
		}()
	}
	wg.Wait()

	// if we get here without a race condition panic, the test passes
	c.Assert(h.IsAsgardAddress("addr1"), Equals, true)
	c.Assert(h.IsAsgardAddress("unknown"), Equals, false)
}
