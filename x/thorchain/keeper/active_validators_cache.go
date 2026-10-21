package keeper

import (
	"slices"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type activeValidatorsCacheKey struct{}

// ActiveValidatorsCache is a read-through snapshot of the active validator
// list scoped by a cosmos.Context, so the many ListActiveValidators calls of
// one BeginBlock or EndBlock decode the node accounts once. Like MimirCache,
// any node-account write through a context carrying the cache disables it for
// good: the pointer is shared with cache contexts that may be discarded, so a
// refill there could snapshot an uncommitted value.
//
// Only attach it outside transactions: store reads inside a tx count towards
// gas_used, which is consensus-visible.
type ActiveValidatorsCache struct {
	mu       sync.Mutex
	nodes    types.NodeAccounts
	loaded   bool
	disabled bool
}

// WithActiveValidatorsCache attaches a fresh cache to ctx.
//
// Invariant: every node-account write made while this cache is live must go
// through a ctx derived from the returned one, or the cache will not be
// disabled and reads will keep returning the stale snapshot.
func WithActiveValidatorsCache(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(activeValidatorsCacheKey{}, &ActiveValidatorsCache{})
}

func ActiveValidatorsCacheFromContext(ctx cosmos.Context) *ActiveValidatorsCache {
	cache, _ := ctx.Value(activeValidatorsCacheKey{}).(*ActiveValidatorsCache)
	return cache
}

// Get returns a copy of the cached list, which the caller may modify freely.
func (c *ActiveValidatorsCache) Get() (types.NodeAccounts, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled || !c.loaded {
		return nil, false
	}
	return cloneNodeAccounts(c.nodes), true
}

// Set stores a copy of nodes, so later changes by the caller do not leak in.
func (c *ActiveValidatorsCache) Set(nodes types.NodeAccounts) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.disabled {
		c.nodes = cloneNodeAccounts(nodes)
		c.loaded = true
	}
	c.mu.Unlock()
}

// Disable drops the snapshot and makes every later Get miss.
func (c *ActiveValidatorsCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabled = true
	c.nodes = nil
	c.mu.Unlock()
}

// cloneNodeAccounts returns CloneNodeAccount of each element.
func cloneNodeAccounts(src types.NodeAccounts) types.NodeAccounts {
	out := make(types.NodeAccounts, len(src))
	for i := range src {
		out[i] = CloneNodeAccount(src[i])
	}
	return out
}

// CloneNodeAccount deep-copies the mutable parts of a NodeAccount (byte and
// string slices, the Bond big.Int) so the copy shares nothing a caller can
// change. Strings are immutable and are shared; every other field is a value.
func CloneNodeAccount(na types.NodeAccount) types.NodeAccount {
	na.NodeAddress = slices.Clone(na.NodeAddress)
	na.SignerMembership = slices.Clone(na.SignerMembership)
	if !na.Bond.IsNil() {
		na.Bond = cosmos.NewUintFromBigInt(na.Bond.BigIntMut())
	}
	return na
}
