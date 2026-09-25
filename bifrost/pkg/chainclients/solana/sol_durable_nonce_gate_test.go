package solana

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"

	. "gopkg.in/check.v1"
)

// heightMimirBridge distinguishes the live mimir (GetMimir) from the value at a
// consensus height (GetMimirAtHeight). The durable-nonce gate must use the latter so
// nodes with divergent live mimir but the same keysign height decide identically.
type heightMimirBridge struct {
	thorclient.ThorchainBridge
	live     int64
	byHeight map[int64]int64
}

func (b *heightMimirBridge) GetMimir(string) (int64, error) { return b.live, nil }
func (b *heightMimirBridge) GetMimirAtHeight(_ string, height int64) (int64, error) {
	return b.byHeight[height], nil
}

// fallbackMimirBridge implements only the live GetMimir (no GetMimirAtHeight), like a
// test double, to exercise the type-assert fallback.
type fallbackMimirBridge struct {
	thorclient.ThorchainBridge
	live int64
}

func (b *fallbackMimirBridge) GetMimir(string) (int64, error) { return b.live, nil }

// TestDurableNonceGateUsesHeightNotLive: two nodes at the same keysign height H=100
// (gate value there = 1 = ON) but with DIFFERENT live mimir (node A sees 0, node B
// sees 1) must both resolve the gate to the height-anchored value, so they take the
// same path and sign identical bytes — never the divergent live value (N3/N10).
func (s *SolTestSuite) TestDurableNonceGateUsesHeightNotLive(c *C) {
	nodeA := &SOLClient{bridge: &heightMimirBridge{live: 0, byHeight: map[int64]int64{100: 1}}}
	nodeB := &SOLClient{bridge: &heightMimirBridge{live: 1, byHeight: map[int64]int64{100: 1}}}

	va, err := nodeA.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)
	vb, err := nodeB.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)

	c.Assert(va, Equals, int64(1)) // the height value, NOT node A's live 0
	c.Assert(vb, Equals, int64(1))
	c.Assert(va, Equals, vb) // identical gate => identical path => identical bytes

	// Different height with the gate OFF there is seen identically by both.
	nodeA2 := &SOLClient{bridge: &heightMimirBridge{live: 9, byHeight: map[int64]int64{200: 0}}}
	v, err := nodeA2.durableNonceMimirAtHeight(200)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(0)) // off at H=200 despite live=9
}

// TestDurableNonceGateFallsBackToLive: a bridge without GetMimirAtHeight (a test
// double) falls back to the live read.
func (s *SolTestSuite) TestDurableNonceGateFallsBackToLive(c *C) {
	fb := &SOLClient{bridge: &fallbackMimirBridge{live: 7}}
	v, err := fb.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(7))
}
