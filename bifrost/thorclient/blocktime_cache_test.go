package thorclient

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/blocktime"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Reading block_time_ms sets the block time the bridge's one-block response
// cache, and Bifrost's per-block polling, run at. After the block time halves,
// a response three quarters of the old block old is stale.
func TestResponseCacheFollowsBlockTime(t *testing.T) {
	ref := constants.ThorchainBlockTime
	t.Cleanup(func() { blocktime.Set(ref.Milliseconds()) })
	resetHTTPResponseCaches(t)
	blockMs := ref.Milliseconds()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ThorchainConstants) {
			_, _ = w.Write([]byte(`{"int_64_values":{},"block_time_ms":` + strconv.FormatInt(blockMs, 10) + `}`))
		}
	}))
	defer server.Close()
	client := retryablehttp.NewClient()
	client.Logger = nil
	bridge := &thorchainBridge{httpClient: client, cfg: config.BifrostClientConfiguration{ChainHost: server.URL}}

	url := t.Name()
	// held for the whole test, so the cache's idle sweep keeps the entry
	cache := acquireHTTPResponseCache(url)
	defer releaseHTTPResponseCache(cache)
	fresh := func() ([]byte, int, error) { return []byte("fresh"), http.StatusOK, nil }
	read := func() string {
		cache.httpResponse = []byte("cached")
		cache.httpResponseChecked = time.Now().Add(-ref * 3 / 4)
		buf, _, err := bridge.getCached(t.Context(), url, fresh)
		require.NoError(t, err)
		return string(buf)
	}

	ms, _, err := bridge.GetBlockTime()
	require.NoError(t, err)
	require.Equal(t, ref.Milliseconds(), ms)
	require.Equal(t, ref, blocktime.Get())
	require.Equal(t, "cached", read())

	blockMs = ref.Milliseconds() / 2
	httpResponseCachesMu.Lock()
	delete(httpResponseCaches, bridge.getThorChainURL(ThorchainConstants))
	httpResponseCachesMu.Unlock()
	ms, _, err = bridge.GetBlockTime()
	require.NoError(t, err)
	require.Equal(t, ref.Milliseconds()/2, ms)
	require.Equal(t, ref/2, blocktime.Get())
	require.Equal(t, "fresh", read())
}
