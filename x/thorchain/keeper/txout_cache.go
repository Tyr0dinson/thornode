package keeper

import (
	"bytes"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type txOutCacheKey struct{}

// TxOutCache is a read-through snapshot of the stored TxOut bytes by height,
// absent heights included, scoped by a cosmos.Context. One swap quote walks
// the pending txout window several times (vault security sorts, pending
// outbounds) before it adds its own outbound; the cache lets those walks
// share one store read per height. Like MimirCache, any txout write through
// a context carrying the cache disables it for good: the pointer is shared
// with cache contexts that may be discarded, so a refill there could snapshot
// an uncommitted value.
//
// Only attach it to queries that are not run on behalf of a contract: store
// reads count gas, and gas inside a tx (including CosmWasm queries) is
// consensus-visible.
type TxOutCache struct {
	mu       sync.Mutex
	parent   *TxOutCache
	entries  map[int64]txOutCacheEntry
	chunks   map[int64]struct{}
	derived  map[any]any
	disabled bool
}

// Derived returns the value stored under key by SetDerived, while the cache
// is live. Values are computed from txouts read through the cache, so they
// hold for as long as its snapshot does; they must be treated as read-only.
func (c *TxOutCache) Derived(key any) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return nil, false
	}
	if c.parent != nil {
		c.mu.Unlock()
		return c.parent.Derived(key)
	}
	value, ok := c.derived[key]
	c.mu.Unlock()
	return value, ok
}

// SetDerived stores a value computed from txouts read through the cache
// under key (a comparable value naming the computation and its inputs).
func (c *TxOutCache) SetDerived(key, value any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return
	}
	if c.parent != nil {
		c.mu.Unlock()
		c.parent.SetDerived(key, value)
		return
	}
	if c.derived == nil {
		c.derived = make(map[any]any)
	}
	c.derived[key] = value
	c.mu.Unlock()
}

type txOutCacheEntry struct {
	bz      []byte
	present bool
}

// TxOutCacheChunk is how many consecutive heights a cache miss reads at
// once: one range iteration over the stored txouts of the aligned chunk
// holding the missed height, instead of a point read per height. Most
// heights of a quote's windows hold no txout, and each absent height costs
// a full store lookup.
const TxOutCacheChunk = 1000

// ClaimChunk reports whether the heights [chunk*TxOutCacheChunk,
// (chunk+1)*TxOutCacheChunk) are still to be read in one go, and marks them
// as read: the caller must then Fill them. It is false once claimed and
// whenever the cache is not live.
func (c *TxOutCache) ClaimChunk(chunk int64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return false
	}
	if c.parent != nil {
		c.mu.Unlock()
		return c.parent.ClaimChunk(chunk)
	}
	if c.chunks == nil {
		c.chunks = make(map[int64]struct{})
	}
	_, claimed := c.chunks[chunk]
	c.chunks[chunk] = struct{}{}
	c.mu.Unlock()
	return !claimed
}

// Fill records the store contents of the heights [lo, hi]: stored holds the
// bytes of the heights that have a TxOut, every other height is absent.
// Heights already cached keep their entry (it was read from the same store).
// The cache takes ownership of the stored bytes.
func (c *TxOutCache) Fill(lo, hi int64, stored map[int64][]byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return
	}
	if c.parent != nil {
		c.mu.Unlock()
		c.parent.Fill(lo, hi, stored)
		return
	}
	for height := lo; height <= hi; height++ {
		if _, ok := c.entries[height]; ok {
			continue
		}
		bz, present := stored[height]
		c.entries[height] = txOutCacheEntry{bz: bz, present: present}
	}
	c.mu.Unlock()
}

// WithTxOutCache attaches a fresh cache to ctx, unless ctx already carries
// one (which it then keeps using, disabled or not).
//
// Invariant: every txout write made while this cache is live must go through
// a ctx derived from the returned one, or the cache will not be disabled and
// reads will keep returning the stale snapshot.
func WithTxOutCache(ctx cosmos.Context) cosmos.Context {
	if TxOutCacheFromContext(ctx) != nil {
		return ctx
	}
	return ctx.WithValue(txOutCacheKey{}, &TxOutCache{entries: make(map[int64]txOutCacheEntry)})
}

// ForkTxOutCache gives ctx a view of its cache that a txout write through the
// returned ctx disables without disabling the original. While the view is
// live it reads and fills the original's snapshot.
//
// Only use it for a store branch that is never written back (e.g. a swap
// simulation's discarded CacheMultiStore), and do not write txouts through
// the original ctx while the view is in use: until its first txout write the
// branch sees exactly the original's txouts, after it the view is disabled.
// Without a cache on ctx it returns ctx unchanged.
func ForkTxOutCache(ctx cosmos.Context) cosmos.Context {
	parent := TxOutCacheFromContext(ctx)
	if parent == nil {
		return ctx
	}
	return ctx.WithValue(txOutCacheKey{}, &TxOutCache{parent: parent})
}

func TxOutCacheFromContext(ctx cosmos.Context) *TxOutCache {
	cache, _ := ctx.Value(txOutCacheKey{}).(*TxOutCache)
	return cache
}

// Get returns the stored bytes of height (present=false: no TxOut stored) and
// whether the cache held them. The bytes must not be modified.
func (c *TxOutCache) Get(height int64) (bz []byte, present, ok bool) {
	if c == nil {
		return nil, false, false
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return nil, false, false
	}
	if c.parent != nil {
		c.mu.Unlock()
		return c.parent.Get(height)
	}
	entry, ok := c.entries[height]
	c.mu.Unlock()
	return entry.bz, entry.present, ok
}

// Set records the store result for height, copying bz so later changes by
// the caller do not leak in.
func (c *TxOutCache) Set(height int64, bz []byte, present bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return
	}
	if c.parent != nil {
		c.mu.Unlock()
		c.parent.Set(height, bz, present)
		return
	}
	c.entries[height] = txOutCacheEntry{bz: bytes.Clone(bz), present: present}
	c.mu.Unlock()
}

// Live reports whether the cache serves reads: it is set and neither it nor
// the cache it forks has been disabled. While it stays live, a read of a
// height already read through it touches no store.
func (c *TxOutCache) Live() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	disabled, parent := c.disabled, c.parent
	c.mu.Unlock()
	if disabled {
		return false
	}
	if parent != nil {
		return parent.Live()
	}
	return true
}

// Disable drops the snapshot (a fork only stops using its parent) and makes
// every later Get miss.
func (c *TxOutCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabled = true
	c.parent = nil
	c.entries = nil
	c.derived = nil
	c.mu.Unlock()
}
