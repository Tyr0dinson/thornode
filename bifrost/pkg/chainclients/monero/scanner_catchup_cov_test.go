package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// catchupSidecarCov serves scanner state, canonical blocks and rewinds with
// injectable failures.
type catchupSidecarCov struct {
	mu          sync.Mutex
	nextHeight  uint64
	stateFail   bool
	blockFail   map[int64]int // height -> HTTP status
	blockHash   map[int64]string
	rewindFail  bool
	rewindCalls []sidecar.ScannerRewindReq
}

func (c *catchupSidecarCov) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
		if c.stateFail {
			http.Error(w, "state down", http.StatusInternalServerError)
			return
		}
		next := c.nextHeight
		_ = json.NewEncoder(w).Encode(sidecar.ScannerState{NextHeight: &next})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
		height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
		if err != nil {
			http.Error(w, "bad height", http.StatusBadRequest)
			return
		}
		if status, ok := c.blockFail[height]; ok {
			http.Error(w, "block unavailable", status)
			return
		}
		hash := testBlockHash(height)
		if override, ok := c.blockHash[height]; ok {
			hash = override
		}
		_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{Height: height, Hash: hash, PreviousHash: testBlockHash(height - 1)})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/rewind":
		var req sidecar.ScannerRewindReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.rewindCalls = append(c.rewindCalls, req)
		if c.rewindFail {
			http.Error(w, "rewind conflict", http.StatusConflict)
			return
		}
		previous := c.nextHeight
		c.nextHeight = req.TargetNextHeight
		_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{
			Rewound: true, PreviousNextHeight: previous, NextHeight: req.TargetNextHeight, ResetEpoch: req.ExpectedResetEpoch + 1,
		})
	default:
		http.NotFound(w, r)
	}
}

func (c *catchupSidecarCov) set(fn func(c *catchupSidecarCov)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

func newCatchupScannerCov(t *testing.T, next uint64, maxReorg int64) (*Scanner, *catchupSidecarCov, *ReorgStore) {
	t.Helper()
	fake := &catchupSidecarCov{nextHeight: next, blockFail: map[int64]int{}, blockHash: map[int64]string{}}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{SignerExpectedNetwork: "mocknet"}, server.URL)
	require.NoError(t, err)
	_, err = client.ScannerState()
	require.NoError(t, err)
	store := newScannerReceiptStore(t)
	scanner := NewScanner(testLogger(), client, &scannerMemoBridge{}, common.XMRChain, common.EmptyPubKey, maxReorg)
	scanner.SetReorgStore(store)
	return scanner, fake, store
}

func TestAdvanceSidecarCatchupPreconditionsCov(t *testing.T) {
	t.Parallel()

	_, _, err := (&Scanner{}).advanceSidecarCatchupThrough(1, 1, 1)
	require.ErrorContains(t, err, "sidecar is nil")

	scanner, fake, store := newCatchupScannerCov(t, 10, 8)

	// Cursor already at the target: nothing to do.
	handled, marker, err := scanner.advanceSidecarCatchup(10, 20)
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, marker)

	// Scanner state fetch failure.
	fake.set(func(c *catchupSidecarCov) { c.stateFail = true })
	_, _, err = scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "fetch sidecar scanner cursor")
	fake.set(func(c *catchupSidecarCov) { c.stateFail = false })

	// Corrupt durable marker.
	require.NoError(t, store.db.Put([]byte(scannerCatchupMarkerDBKey), []byte("{"), nil))
	_, _, err = scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "decode xmr scanner catch-up marker")
	require.NoError(t, store.db.Delete([]byte(scannerCatchupMarkerDBKey), nil))

	// Churn rewind beyond the supported window.
	scanner.maxReorgRescan = 0
	_, _, err = scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "beyond rewind limit")
	scanner.maxReorgRescan = 8

	// Anchor lookup failure.
	fake.set(func(c *catchupSidecarCov) { c.blockFail[4] = http.StatusInternalServerError })
	_, _, err = scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "fetch xmr catch-up anchor 4")
	m, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, m, "no marker is written without an anchor")

	// Without durable storage, catch-up refuses to begin.
	scanner.reorgStore = nil
	_, _, err = scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "without durable storage")
}

func TestAdvanceSidecarCatchupInvalidStartCov(t *testing.T) {
	t.Parallel()

	scanner, _, _ := newCatchupScannerCov(t, 0, 8)
	_, _, err := scanner.advanceSidecarCatchup(5, 20)
	require.ErrorContains(t, err, "invalid sidecar catch-up start height 0")
}

func TestAdvanceSidecarCatchupMarkerTransitionsCov(t *testing.T) {
	t.Parallel()

	t.Run("target-mismatch-incomplete", func(t *testing.T) {
		scanner, _, store := newCatchupScannerCov(t, 6, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 6, TargetHeight: 10, AnchorHeight: 5, AnchorHash: testBlockHash(5)}))
		_, _, err := scanner.advanceSidecarCatchup(12, 20)
		require.ErrorContains(t, err, "targets height 10 while blockscanner requested 12")
	})
	t.Run("churn-rollback-extends-target", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 6, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 6, TargetHeight: 10, AnchorHeight: 5, AnchorHash: testBlockHash(5), ChurnRollback: true}))
		fake.set(func(c *catchupSidecarCov) { c.blockFail[6] = http.StatusConflict })
		handled, _, err := scanner.advanceSidecarCatchup(12, 20)
		require.True(t, handled)
		require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, int64(12), m.TargetHeight)
	})
	t.Run("lower-target-rewinds-anchor", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 8, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 8, TargetHeight: 10, AnchorHeight: 7, AnchorHash: testBlockHash(7)}))
		fake.set(func(c *catchupSidecarCov) { c.blockFail[5] = http.StatusInternalServerError })
		handled, _, err := scanner.advanceSidecarCatchup(6, 20)
		require.True(t, handled)
		// the anchor lookup for block 5 fails
		require.ErrorContains(t, err, "/v1/monero/chain/block/5 returned status 500")
		fake.set(func(c *catchupSidecarCov) {
			delete(c.blockFail, 5)
			c.blockFail[6] = http.StatusConflict
		})
		_, _, err = scanner.advanceSidecarCatchup(6, 20)
		require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, int64(6), m.StartHeight)
		require.Equal(t, int64(6), m.TargetHeight)
		require.True(t, m.ChurnRollback)
		require.Equal(t, testBlockHash(5), m.AnchorHash)
		fake.mu.Lock()
		require.NotEmpty(t, fake.rewindCalls, "sidecar was rewound to the new start")
		fake.mu.Unlock()
	})
	t.Run("lower-target-without-rewind-window", func(t *testing.T) {
		scanner, _, store := newCatchupScannerCov(t, 8, 0)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 8, TargetHeight: 10, AnchorHeight: 7, AnchorHash: testBlockHash(7)}))
		handled, _, err := scanner.advanceSidecarCatchup(6, 20)
		require.True(t, handled)
		require.ErrorContains(t, err, "beyond rewind limit")
	})
	t.Run("completed-extends-or-reopens", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 11, 8)
		completed := scannerCatchupMarker{StartHeight: 11, TargetHeight: 10, AnchorHeight: 10, AnchorHash: testBlockHash(10)}
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 10, TargetHeight: 10, AnchorHeight: 9, AnchorHash: testBlockHash(9)}))
		require.NoError(t, store.CheckpointCatchup(completed, &reorgBlockMeta{Height: 10, BlockHash: testBlockHash(10), PreviousHash: testBlockHash(9)}, nil))

		// Same target and canonical: completed immediately.
		handled, marker, err := scanner.advanceSidecarCatchup(10, 20)
		require.NoError(t, err)
		require.True(t, handled)
		require.True(t, marker.completed())

		// Canonical lookup failure on the completed target.
		fake.set(func(c *catchupSidecarCov) { c.blockFail[10] = http.StatusInternalServerError })
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.Error(t, err)
		_, _, err = scanner.advanceSidecarCatchup(11, 20)
		require.Error(t, err)

		// Orphaned completed target with failing parent lookup.
		fake.set(func(c *catchupSidecarCov) {
			delete(c.blockFail, 10)
			c.blockHash[10] = testStaleBlockHash(10)
			c.blockFail[9] = http.StatusInternalServerError
		})
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.Error(t, err)
		_, _, err = scanner.advanceSidecarCatchup(11, 20)
		require.Error(t, err)

		// Orphaned completed target reopens at the target height and rewinds
		// the sidecar to it; the replacement scan itself then fails (no sync
		// endpoint), leaving the reopened marker durable.
		fake.set(func(c *catchupSidecarCov) { delete(c.blockFail, 9) })
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.Error(t, err)
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, int64(10), m.StartHeight)
		require.Equal(t, int64(10), m.TargetHeight)
		require.Equal(t, testBlockHash(9), m.AnchorHash)
		fake.mu.Lock()
		require.NotEmpty(t, fake.rewindCalls)
		require.Equal(t, uint64(10), fake.rewindCalls[len(fake.rewindCalls)-1].TargetNextHeight)
		fake.mu.Unlock()
	})
	t.Run("extend-completed-to-next-target", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 11, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 10, TargetHeight: 10, AnchorHeight: 9, AnchorHash: testBlockHash(9)}))
		require.NoError(t, store.CheckpointCatchup(
			scannerCatchupMarker{StartHeight: 11, TargetHeight: 10, AnchorHeight: 10, AnchorHash: testBlockHash(10)},
			&reorgBlockMeta{Height: 10, BlockHash: testBlockHash(10), PreviousHash: testBlockHash(9)}, nil))
		fake.set(func(c *catchupSidecarCov) { c.blockFail[11] = http.StatusConflict })
		_, _, err := scanner.advanceSidecarCatchup(11, 20)
		require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, int64(11), m.TargetHeight)
		require.Equal(t, int64(11), m.StartHeight)
	})
	t.Run("registration-rewinds-start", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 4, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 8, TargetHeight: 10, AnchorHeight: 7, AnchorHash: testBlockHash(7)}))
		fake.set(func(c *catchupSidecarCov) { c.blockFail[3] = http.StatusInternalServerError })
		_, _, err := scanner.advanceSidecarCatchup(10, 20)
		require.Error(t, err)
		fake.set(func(c *catchupSidecarCov) {
			delete(c.blockFail, 3)
			c.blockFail[4] = http.StatusInternalServerError
		})
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.ErrorContains(t, err, "fetch xmr catch-up block 4")
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, int64(4), m.StartHeight)
		require.Equal(t, testBlockHash(3), m.AnchorHash)
	})
	t.Run("anchor-rebind-and-ambiguous-advance", func(t *testing.T) {
		scanner, fake, store := newCatchupScannerCov(t, 9, 8)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 8, TargetHeight: 10, AnchorHeight: 7, AnchorHash: testStaleBlockHash(7)}))
		fake.set(func(c *catchupSidecarCov) { c.blockFail[7] = http.StatusInternalServerError })
		_, _, err := scanner.advanceSidecarCatchup(10, 20)
		require.Error(t, err)
		fake.set(func(c *catchupSidecarCov) {
			delete(c.blockFail, 7)
			c.rewindFail = true
		})
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.ErrorContains(t, err, "rewind")
		m, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.Equal(t, testBlockHash(7), m.AnchorHash, "anchor rebound to the canonical hash")

		scanner.maxReorgRescan = 0
		_, _, err = scanner.advanceSidecarCatchup(10, 20)
		require.ErrorContains(t, err, "exceeds rewind limit 0")
	})
	t.Run("disabled-history", func(t *testing.T) {
		scanner, _, store := newCatchupScannerCov(t, 8, 0)
		require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 8, TargetHeight: 10, AnchorHeight: 7, AnchorHash: testBlockHash(7)}))
		_, _, err := scanner.advanceSidecarCatchup(10, 20)
		require.ErrorContains(t, err, "catch-up is disabled")
	})
}
