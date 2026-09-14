package substrate

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

func TestNonceCacheValuesAndPurgeBoundaries(t *testing.T) {
	entries := []struct {
		hash   string
		nonce  uint64
		height uint64
	}{
		{"later", math.MaxUint64, 120},
		{"zero-height", 7, 0},
		{"zero-nonce", 0, 100},
		{"same-height", 9, 100},
	}
	for _, cutoff := range []int64{-1, 0, 1, 100, 101, 120, 121} {
		t.Run(fmt.Sprint(cutoff), func(t *testing.T) {
			cache := newNonceCache()
			if _, found := cache.Get("missing"); found {
				t.Fatal("empty cache contains entry")
			}
			for _, entry := range entries {
				cache.Add(entry.hash, entry.nonce, entry.height)
			}
			cache.Purge(cutoff)
			for _, entry := range entries {
				nonce, found := cache.Get(entry.hash)
				want := cutoff <= 0 || entry.height >= uint64(cutoff)
				if found != want || (found && nonce != entry.nonce) {
					t.Errorf("Get(%s) = (%d, %v), want nonce %d, found %v", entry.hash, nonce, found, entry.nonce, want)
				}
			}
		})
	}
}

func TestNonceCacheRescanAndReadd(t *testing.T) {
	cache := newNonceCache()
	for i := 0; i < 3; i++ {
		cache.Add("tx", 7, 100)
	}
	cache.Purge(100)
	if nonce, found := cache.Get("tx"); !found || nonce != 7 {
		t.Fatalf("rescanned entry = (%d, %v), want (7, true)", nonce, found)
	}
	cache.Purge(101)
	if _, found := cache.Get("tx"); found || len(cache.hashesByHeight) != 0 {
		t.Fatal("expired entry was not fully removed")
	}
	cache.Add("tx", 7, 120)
	cache.Purge(120)
	if nonce, found := cache.Get("tx"); !found || nonce != 7 {
		t.Fatal("re-added entry is missing")
	}
	cache.Purge(121)
	if _, found := cache.Get("tx"); found {
		t.Fatal("re-added entry did not expire")
	}
}

func TestNonceCacheConcurrentAccess(t *testing.T) {
	cache := newNonceCache()
	cache.Add("old", 0, 99)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hash := fmt.Sprint(i)
			cache.Add(hash, uint64(i), 100)
			cache.Purge(100)
			if nonce, found := cache.Get(hash); !found || nonce != uint64(i) {
				t.Errorf("entry %s = (%d, %v), want (%d, true)", hash, nonce, found, i)
			}
		}()
	}
	wg.Wait()
	if _, found := cache.Get("old"); found {
		t.Fatal("expired entry remains")
	}
	cache.Purge(101)
	if len(cache.noncesByHash) != 0 || len(cache.hashesByHeight) != 0 {
		t.Fatal("cache is not empty after purge")
	}
}
