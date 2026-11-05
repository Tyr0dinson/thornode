package thorclient

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ageHTTPResponseCaches moves every cache timestamp d into the past, as if d
// had passed since the entries were refreshed and the cache last swept.
func ageHTTPResponseCaches(d time.Duration) {
	httpResponseCachesMu.Lock()
	defer httpResponseCachesMu.Unlock()
	for _, entry := range httpResponseCaches {
		entry.httpResponseChecked = entry.httpResponseChecked.Add(-d)
	}
	httpResponseCachesSwept = httpResponseCachesSwept.Add(-d)
}

func resetHTTPResponseCaches(tb testing.TB) {
	tb.Helper()
	httpResponseCachesMu.Lock()
	saved, savedSwept := httpResponseCaches, httpResponseCachesSwept
	httpResponseCaches = make(map[string]*httpResponseCache)
	// the next get sweeps, so tests do not depend on when earlier ones swept
	httpResponseCachesSwept = time.Time{}
	httpResponseCachesMu.Unlock()
	tb.Cleanup(func() {
		httpResponseCachesMu.Lock()
		httpResponseCaches, httpResponseCachesSwept = saved, savedSwept
		httpResponseCachesMu.Unlock()
	})
}

func TestHTTPResponseCacheDropsIdleEntries(t *testing.T) {
	resetHTTPResponseCaches(t)
	bridge := &thorchainBridge{}
	get := func(url string) []byte {
		buf, _, err := bridge.getCached(context.Background(), url, func() ([]byte, int, error) {
			return []byte(url), http.StatusOK, nil
		})
		require.NoError(t, err)
		return buf
	}

	get("idle")
	get("busy")
	// an entry a caller is using is kept even when it is idle
	httpResponseCachesMu.Lock()
	busy := httpResponseCaches["busy"]
	busy.users++
	httpResponseCachesMu.Unlock()
	ageHTTPResponseCaches(httpResponseCacheIdle / 2)
	get("fresh")
	ageHTTPResponseCaches(httpResponseCacheIdle / 2)
	get("trigger") // the last sweep is now httpResponseCacheIdle ago

	httpResponseCachesMu.Lock()
	_, idle := httpResponseCaches["idle"]
	_, fresh := httpResponseCaches["fresh"]
	busyKept := httpResponseCaches["busy"] == busy
	busy.users--
	httpResponseCachesMu.Unlock()
	require.False(t, idle, "idle entry should be dropped")
	require.True(t, fresh, "entry refreshed within the idle window should be kept")
	require.True(t, busyKept, "entry in use should be kept")

	// a response fetched within the block time is still served from the cache
	calls := 0
	buf, _, err := bridge.getCached(context.Background(), "trigger", func() ([]byte, int, error) {
		calls++
		return []byte("refetched"), http.StatusOK, nil
	})
	require.NoError(t, err)
	require.Equal(t, "trigger", string(buf))
	require.Zero(t, calls)

	// a dropped entry is a plain cache miss
	buf = get("idle")
	require.Equal(t, "idle", string(buf))
}

func TestHTTPResponseCacheReleasesCancelledWaiter(t *testing.T) {
	resetHTTPResponseCaches(t)
	bridge := &thorchainBridge{}
	users := func() int {
		httpResponseCachesMu.Lock()
		defer httpResponseCachesMu.Unlock()
		entry := httpResponseCaches["slow"]
		if entry == nil {
			return -1
		}
		return entry.users
	}

	// a holds the entry lock in a slow fetch
	fetching, finish := make(chan struct{}), make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		_, _, err := bridge.getCached(context.Background(), "slow", func() ([]byte, int, error) {
			close(fetching)
			<-finish
			return []byte("slow"), http.StatusOK, nil
		})
		aDone <- err
	}()
	<-fetching

	// b waits on the lock, then gives up
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bDone := make(chan error, 1)
	go func() {
		_, _, err := bridge.getCached(ctx, "slow", func() ([]byte, int, error) {
			return nil, 0, fmt.Errorf("b must not fetch")
		})
		bDone <- err
	}()
	require.Eventually(t, func() bool { return users() == 2 }, 5*time.Second, time.Millisecond,
		"b should be waiting on the entry")
	cancel()
	require.ErrorIs(t, <-bDone, context.Canceled)
	require.Equal(t, 1, users(), "a cancelled waiter must release the entry")

	close(finish)
	require.NoError(t, <-aDone)
	require.Zero(t, users(), "an entry nobody holds must be free to sweep")
}

// BenchmarkHTTPResponseCacheSignerDay replays one day of the signer's THORChain
// polling through the shared response cache: per block, one keygen URL and one
// keysign URL for each of six signing pubkeys, each answered with a small body
// read the way io.ReadAll reads it. It reports the heap the cache still holds
// afterwards.
func BenchmarkHTTPResponseCacheSignerDay(b *testing.B) {
	const (
		blocksPerDay = 14400 // 6s blocks
		signPubKeys  = 6
	)
	bridge := &thorchainBridge{}
	body := []byte(`{"keysign":{"height":"1","tx_array":[],"chain":"BTC"},"signature":""}`)
	for range b.N {
		b.StopTimer()
		resetHTTPResponseCaches(b)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b.StartTimer()
		for height := int64(1); height <= blocksPerDay; height++ {
			urls := make([]string, 0, signPubKeys+1)
			urls = append(urls, fmt.Sprintf("http://thornode:1317/thorchain/keygen/%d/thorpub1node", height))
			for pk := range signPubKeys {
				urls = append(urls, fmt.Sprintf("http://thornode:1317/thorchain/keysign/%d/thorpub1vault%d", height, pk))
			}
			for _, url := range urls {
				_, _, err := bridge.getCached(context.Background(), url, func() ([]byte, int, error) {
					buf := make([]byte, 0, 512) // io.ReadAll's first buffer
					return append(buf, body...), http.StatusOK, nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			ageHTTPResponseCaches(6 * time.Second)
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		httpResponseCachesMu.Lock()
		entries := len(httpResponseCaches)
		httpResponseCachesMu.Unlock()
		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), "retained-MiB")
		b.ReportMetric(float64(entries), "entries")
		b.StartTimer()
	}
}
