package keeper

import (
	"testing"
)

func TestTxOutCacheNilIsInert(t *testing.T) {
	var c *TxOutCache
	c.Set(1, []byte{1}, true)
	c.Fill(1, 2, map[int64][]byte{1: {1}})
	c.SetDerived("k", 1)
	c.Disable()
	if _, _, ok := c.Get(1); ok {
		t.Fatal("nil cache served a height")
	}
	if _, ok := c.Derived("k"); ok {
		t.Fatal("nil cache served a derived value")
	}
	if c.ClaimChunk(0) || c.Live() {
		t.Fatal("nil cache claimed a chunk or reported live")
	}
}

func TestTxOutCacheFillClaimDerived(t *testing.T) {
	c := &TxOutCache{entries: make(map[int64]txOutCacheEntry)}
	c.Set(5, []byte{9}, true)
	c.Fill(4, 6, map[int64][]byte{5: {1}, 6: {2}})

	// heights already cached keep their entry; others take the fill
	if bz, present, ok := c.Get(5); !ok || !present || bz[0] != 9 {
		t.Fatalf("height 5: %v %v %v", bz, present, ok)
	}
	if bz, present, ok := c.Get(6); !ok || !present || bz[0] != 2 {
		t.Fatalf("height 6: %v %v %v", bz, present, ok)
	}
	if _, present, ok := c.Get(4); !ok || present {
		t.Fatalf("height 4 should be cached as absent: %v %v", present, ok)
	}

	if !c.ClaimChunk(3) || c.ClaimChunk(3) {
		t.Fatal("a chunk is claimable exactly once")
	}
	c.SetDerived("k", 7)
	if v, ok := c.Derived("k"); !ok || v != 7 {
		t.Fatalf("derived: %v %v", v, ok)
	}
}

func TestTxOutCacheDisabledIgnoresWrites(t *testing.T) {
	c := &TxOutCache{entries: make(map[int64]txOutCacheEntry)}
	c.Disable()
	c.Set(1, []byte{1}, true)
	c.Fill(1, 1, map[int64][]byte{1: {1}})
	c.SetDerived("k", 1)
	if _, _, ok := c.Get(1); ok {
		t.Fatal("disabled cache served a height")
	}
	if _, ok := c.Derived("k"); ok {
		t.Fatal("disabled cache served a derived value")
	}
	if c.ClaimChunk(0) || c.Live() {
		t.Fatal("disabled cache claimed a chunk or reported live")
	}
}

// A fork reads and fills its parent's snapshot until it is disabled, and
// disabling it leaves the parent live.
func TestTxOutCacheForkDelegatesToParent(t *testing.T) {
	parent := &TxOutCache{entries: make(map[int64]txOutCacheEntry)}
	fork := &TxOutCache{parent: parent}

	fork.Set(1, []byte{1}, true)
	fork.Fill(2, 3, map[int64][]byte{2: {2}})
	fork.SetDerived("k", 5)
	if !fork.ClaimChunk(0) || parent.ClaimChunk(0) {
		t.Fatal("a chunk claimed through the fork is claimed on the parent")
	}
	for _, h := range []int64{1, 2, 3} {
		if _, _, ok := parent.Get(h); !ok {
			t.Fatalf("parent missing height %d written through the fork", h)
		}
		if _, _, ok := fork.Get(h); !ok {
			t.Fatalf("fork missing height %d", h)
		}
	}
	if v, ok := fork.Derived("k"); !ok || v != 5 {
		t.Fatalf("fork derived: %v %v", v, ok)
	}
	if v, ok := parent.Derived("k"); !ok || v != 5 {
		t.Fatalf("parent derived: %v %v", v, ok)
	}

	fork.Disable()
	if fork.Live() || !parent.Live() {
		t.Fatal("disabling the fork must leave the parent live")
	}
	if _, _, ok := fork.Get(1); ok {
		t.Fatal("disabled fork served a height")
	}
	if _, _, ok := parent.Get(1); !ok {
		t.Fatal("parent lost its snapshot when the fork was disabled")
	}
}
