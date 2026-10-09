package keeper

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMimirCacheFromContextCov(t *testing.T) {
	ctx := newDummyCtxCov()
	require.Nil(t, MimirCacheFromContext(ctx))

	cached := WithMimirCache(ctx)
	cache := MimirCacheFromContext(cached)
	require.NotNil(t, cache)
	// the original context is untouched
	require.Nil(t, MimirCacheFromContext(ctx))
	// each call attaches a fresh cache
	require.NotSame(t, cache, MimirCacheFromContext(WithMimirCache(ctx)))
	// derived contexts share the same cache pointer
	require.Same(t, cache, MimirCacheFromContext(cached.WithBlockHeight(10)))
}

func TestMimirCacheGetSetDisableCov(t *testing.T) {
	cache := MimirCacheFromContext(WithMimirCache(newDummyCtxCov()))

	_, ok := cache.Get("HALTBTCCHAIN")
	require.False(t, ok)

	cache.Set("HALTBTCCHAIN", 5)
	val, ok := cache.Get("HALTBTCCHAIN")
	require.True(t, ok)
	require.Equal(t, int64(5), val)

	cache.Set("HALTBTCCHAIN", -1)
	val, ok = cache.Get("HALTBTCCHAIN")
	require.True(t, ok)
	require.Equal(t, int64(-1), val)

	cache.Disable()
	_, ok = cache.Get("HALTBTCCHAIN")
	require.False(t, ok)

	// writes after disable are ignored
	cache.Set("HALTBTCCHAIN", 7)
	_, ok = cache.Get("HALTBTCCHAIN")
	require.False(t, ok)
	cache.Disable() // idempotent
	_, ok = cache.Get("HALTBTCCHAIN")
	require.False(t, ok)
}

func TestMimirCacheNilSafeCov(t *testing.T) {
	var cache *MimirCache
	require.NotPanics(t, func() {
		cache.Set("a", 1)
		cache.Disable()
	})
	val, ok := cache.Get("a")
	require.False(t, ok)
	require.Equal(t, int64(0), val)
}

func TestMimirCacheConcurrentCov(t *testing.T) {
	cache := MimirCacheFromContext(WithMimirCache(newDummyCtxCov()))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cache.Set("k", int64(i))
			_, _ = cache.Get("k")
		}(i)
	}
	wg.Wait()
	val, ok := cache.Get("k")
	require.True(t, ok)
	require.GreaterOrEqual(t, val, int64(0))
	require.Less(t, val, int64(8))
}
