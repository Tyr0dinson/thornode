package solana

import (
	"errors"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"

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

// pendingBridge stubs the not-yet-observed outbound queue for the N2 serialization gate.
type pendingBridge struct {
	thorclient.ThorchainBridge
	items []thorclient.PendingOutbound
	err   error
}

func (b *pendingBridge) GetPendingOutbounds() ([]thorclient.PendingOutbound, error) {
	return b.items, b.err
}

// TestDurableSerializeLowestOnly: only the lowest-height not-yet-observed SOL outbound
// for a vault may sign durable (exactly one in flight); higher-height siblings wait;
// the wait unblocks once the lower one drops from the pending queue (bounded, not a
// permanent wedge); same-height ties break by InHash; other vaults/chains don't block;
// and the gate fails open when unavailable.
func (s *SolTestSuite) TestDurableSerializeLowestOnly(c *C) {
	sol := common.SOLChain.String()
	mk := func(h int64, hash string) stypes.TxOutItem {
		return stypes.TxOutItem{Chain: common.SOLChain, VaultPubKey: "vaultA", InHash: common.TxID(hash), Height: h}
	}
	burst := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h100", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h101", Height: 101},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h102", Height: 102},
	}
	cl := &SOLClient{bridge: &pendingBridge{items: burst}, logger: log.Logger}
	c.Assert(cl.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)  // lowest — the one in flight
	c.Assert(cl.durableSerializeAllows(mk(101, "h101"), 101), Equals, false) // waits
	c.Assert(cl.durableSerializeAllows(mk(102, "h102"), 102), Equals, false) // waits

	// Lowest observed (drops from the pending queue) -> next becomes eligible (bounded).
	cl2 := &SOLClient{bridge: &pendingBridge{items: burst[1:]}, logger: log.Logger}
	c.Assert(cl2.durableSerializeAllows(mk(101, "h101"), 101), Equals, true)

	// Stuck lowest: as long as @100 is present, @101 keeps waiting (not a crash/panic —
	// bounded by the B1 reschedule guard in production).
	c.Assert(cl.durableSerializeAllows(mk(101, "h101"), 101), Equals, false)

	// Same-height tiebreak by InHash: lowest InHash goes, the other waits.
	sameHeight := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "aaa", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "bbb", Height: 100},
	}
	cl3 := &SOLClient{bridge: &pendingBridge{items: sameHeight}, logger: log.Logger}
	c.Assert(cl3.durableSerializeAllows(mk(100, "aaa"), 100), Equals, true)
	c.Assert(cl3.durableSerializeAllows(mk(100, "bbb"), 100), Equals, false)

	// A different vault's pending outbound does not block.
	otherVault := []thorclient.PendingOutbound{{Chain: sol, VaultPubKey: "vaultB", InHash: "hx", Height: 50}}
	cl4 := &SOLClient{bridge: &pendingBridge{items: otherVault}, logger: log.Logger}
	c.Assert(cl4.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)

	// A non-SOL pending outbound does not block.
	ethPending := []thorclient.PendingOutbound{{Chain: "ETH", VaultPubKey: "vaultA", InHash: "e50", Height: 50}}
	cl5 := &SOLClient{bridge: &pendingBridge{items: ethPending}, logger: log.Logger}
	c.Assert(cl5.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)

	// Fail-open: a query error proceeds (available; never halts SOL signing).
	cl6 := &SOLClient{bridge: &pendingBridge{err: errors.New("boom")}, logger: log.Logger}
	c.Assert(cl6.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)

	// Fail-open: a bridge without GetPendingOutbounds proceeds.
	cl7 := &SOLClient{bridge: &fallbackMimirBridge{}, logger: log.Logger}
	c.Assert(cl7.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)
}
