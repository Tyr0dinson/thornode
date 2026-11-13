package keeper

import (
	"slices"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type anchorsCacheKey struct{}

// AnchorsCache is a read-through snapshot of GetAnchors(TOR) scoped by a
// cosmos.Context, so the many TOR anchor reads of one swap queue EndBlock
// (stable-to-stable checks, DollarsPerRune) scan the pool store once. The list
// depends only on which pool keys exist and on the TorAnchor Mimir values.
//
// Like MimirCache, a write that could change it disables the cache for good:
// any Mimir write, any pool removal, a write to a pool key the snapshot did not
// see, and any pool write before the first fill. The pointer is shared with
// cache contexts that may be discarded, so a refill there could snapshot an
// uncommitted pool.
//
// Only attach it outside transactions: store reads inside a tx count towards
// gas_used, which is consensus-visible.
type AnchorsCache struct {
	mu       sync.Mutex
	anchors  []common.Asset
	poolKeys map[string]struct{}
	loaded   bool
	disabled bool
}

// WithAnchorsCache attaches a fresh cache to ctx.
//
// Invariant: every pool or Mimir write made while this cache is live must go
// through a ctx derived from the returned one, or the cache will not see it
// and reads will keep returning the stale snapshot.
func WithAnchorsCache(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(anchorsCacheKey{}, &AnchorsCache{})
}

func AnchorsCacheFromContext(ctx cosmos.Context) *AnchorsCache {
	cache, _ := ctx.Value(anchorsCacheKey{}).(*AnchorsCache)
	return cache
}

// Get returns a copy of the cached anchor list, which the caller may modify.
func (c *AnchorsCache) Get() ([]common.Asset, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled || !c.loaded {
		return nil, false
	}
	return slices.Clone(c.anchors), true
}

// Set stores a copy of anchors and takes ownership of poolKeys, the pool store
// keys the scan saw; the caller must not modify the map afterwards.
func (c *AnchorsCache) Set(anchors []common.Asset, poolKeys map[string]struct{}) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.disabled {
		c.anchors = slices.Clone(anchors)
		c.poolKeys = poolKeys
		c.loaded = true
	}
	c.mu.Unlock()
}

// NotePoolWrite disables the cache unless key is a pool the snapshot already
// holds: writing an existing pool cannot change which pools exist.
func (c *AnchorsCache) NotePoolWrite(key []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.disabled {
		if _, ok := c.poolKeys[string(key)]; !c.loaded || !ok {
			c.disable()
		}
	}
	c.mu.Unlock()
}

// Disable drops the snapshot and makes every later Get miss.
func (c *AnchorsCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disable()
	c.mu.Unlock()
}

func (c *AnchorsCache) disable() {
	c.disabled = true
	c.anchors = nil
	c.poolKeys = nil
}
