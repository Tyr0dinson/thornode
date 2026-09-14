package substrate

import (
	"fmt"
	"sync"
	"testing"
)

func TestMigrationTxCacheHeightZero(t *testing.T) {
	cache := newMigrationTxCache()
	if cache.Has("tx") {
		t.Fatal("empty cache contains tx")
	}
	cache.Add("tx", 0)
	if !cache.Has("tx") {
		t.Fatal("height-zero entry was not stored")
	}
	cache.Purge(0)
	if !cache.Has("tx") {
		t.Fatal("zero cutoff removed entry")
	}
	cache.Purge(1)
	if cache.Has("tx") {
		t.Fatal("height-zero entry survived cutoff 1")
	}
}

func TestMigrationTxCachePurgeBoundaries(t *testing.T) {
	for _, cutoff := range []int64{-1, 0, 99, 100, 101, 120, 121} {
		t.Run(fmt.Sprint(cutoff), func(t *testing.T) {
			cache := newMigrationTxCache()
			cache.Add("later", 120)
			cache.Add("first", 100)
			cache.Add("second", 100)
			cache.Purge(cutoff)
			for key, height := range map[string]int64{"first": 100, "second": 100, "later": 120} {
				if got, want := cache.Has(key), height >= cutoff; got != want {
					t.Errorf("Has(%s) = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestMigrationTxCacheKeepsLatestObservation(t *testing.T) {
	for _, heights := range [][]uint64{{100, 120}, {120, 100}, {100, 120, 120, 100}} {
		t.Run(fmt.Sprint(heights), func(t *testing.T) {
			cache := newMigrationTxCache()
			for _, height := range heights {
				cache.Add("tx", height)
			}
			for _, cutoff := range []int64{110, 120} {
				cache.Purge(cutoff)
				if !cache.Has("tx") {
					t.Fatalf("cutoff %d removed the newer observation", cutoff)
				}
			}
			cache.Purge(121)
			if cache.Has("tx") || len(cache.hashesByHeight) != 0 {
				t.Fatal("expired entry was not fully removed")
			}
			cache.Add("tx", 130)
			cache.Purge(130)
			if !cache.Has("tx") {
				t.Fatal("re-added entry is missing")
			}
			cache.Purge(131)
			if cache.Has("tx") {
				t.Fatal("re-added entry did not expire")
			}
		})
	}
}

func TestMigrationTxCacheConcurrentAccess(t *testing.T) {
	cache := newMigrationTxCache()
	cache.Add("old", 99)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprint(i)
			cache.Add(key, 100)
			cache.Purge(100)
			if !cache.Has(key) {
				t.Errorf("entry %s is missing", key)
			}
		}()
	}
	wg.Wait()
	if cache.Has("old") {
		t.Fatal("expired entry remains")
	}
	cache.Purge(101)
	if len(cache.latestHeightByHash) != 0 || len(cache.hashesByHeight) != 0 {
		t.Fatal("cache is not empty after purge")
	}
}
