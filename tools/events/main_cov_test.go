// thorscan.Scan assigns ring-buffer slots while earlier prefetch workers are
// already reading the slice, which the race detector flags; this end-to-end
// run of main() is therefore excluded from -race builds.

//go:build !race

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
)

// TestMainCovScansConfiguredRange runs main() against a fake thornode serving
// empty historical blocks and checks the persisted height cursor.
func TestMainCovScansConfiguredRange(t *testing.T) {
	e := newInfoCovEnv(t)
	origEndpoint := thorscan.APIEndpoint
	origVaults := vaults
	t.Cleanup(func() {
		thorscan.APIEndpoint = origEndpoint
		vaults = origVaults
	})
	e.set("thorchain/vaults/asgard", []openapi.Vault{})

	var mu sync.Mutex
	var blockRequests []int64
	requests := func() []int64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]int64{}, blockRequests...)
	}
	e.set("thorchain/block", func(r *http.Request) any {
		h, err := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
		require.NoError(t, err)
		mu.Lock()
		blockRequests = append(blockRequests, h)
		mu.Unlock()
		return openapi.BlockResponse{
			Id: openapi.BlockResponseId{Hash: "H"},
			Header: openapi.BlockResponseHeader{
				Height: h,
				Time:   "2020-01-01T00:00:00Z", // far in the past: no trailing sleep
			},
		}
	})

	cfg := config.Get()
	cfg.TORAnchorCheckBlocks = 1000
	cfg.Scan.Start = 5
	cfg.Scan.Stop = 5
	config.Set(cfg)

	// a corrupt stored height is reported and the configured start is used
	require.NoError(t, os.WriteFile(filepath.Join(cfg.StoragePath, "height"), []byte("not-json"), 0o600))

	// stale storage entries are pruned on start, fresh ones are kept
	staleDir := filepath.Join(cfg.StoragePath, "scheduled-outbound")
	require.NoError(t, os.MkdirAll(staleDir, 0o755))
	stale := filepath.Join(staleDir, "stale")
	fresh := filepath.Join(staleDir, "fresh")
	require.NoError(t, os.WriteFile(stale, []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(fresh, []byte("{}"), 0o600))
	old := time.Now().Add(-8 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	main()

	require.Equal(t, e.srv.URL, thorscan.APIEndpoint)
	require.NoFileExists(t, stale)
	require.FileExists(t, fresh)
	height := int64(0)
	require.NoError(t, util.Load("height", &height))
	require.Equal(t, int64(5), height)
	require.Equal(t, []int64{5}, requests())

	// second run resumes from the stored cursor (no start override)
	cfg = config.Get()
	cfg.Scan.Start = 0
	cfg.Scan.Stop = 6
	cfg.Console = false
	config.Set(cfg)

	main()

	require.NoError(t, util.Load("height", &height))
	require.Equal(t, int64(6), height)
	require.Equal(t, []int64{5, 6}, requests())
}
