package solana

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

type networkFeeBridge struct {
	thorclient.ThorchainBridge
	err            error
	called         chan struct{}
	agreed         uint64 // 0 keeps the default agreed fee of 5000
	lastObservedIn int64  // 0 never matches a boundary
	lastErr        error
}

func (b *networkFeeBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	if b.called != nil {
		close(b.called)
	}
	if b.agreed != 0 {
		return 1, b.agreed, b.err
	}
	return 1, 5000, b.err
}

func (b *networkFeeBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return b.lastObservedIn, b.lastErr
}

type networkFeePubKeys struct{ pubkeymanager.PubKeyValidator }

func (*networkFeePubKeys) GetAlgoPubKeys(common.SigningAlgo, bool) common.PubKeys {
	return nil
}

type networkFeeStorage struct {
	blockscanner.ScannerStorageSolana
}

func (*networkFeeStorage) SetScanPos(uint64) error { return nil }

type networkFeeRPC struct {
	mu          sync.Mutex
	tip         uint64
	rate        uint64
	fail        bool
	blocks      []uint64
	blockFees   map[uint64][]uint64
	blockErrors map[uint64]bool
	floor       uint64
	floorError  bool
	windows     [][2]uint64
	// root simulates a load-balanced getBlocks backend whose finalized root lags
	// getSlot; zero means the listing backend is caught up.
	root            uint64
	minContextSlots []uint64
}

func newNetworkFeeScanner(t *testing.T, fixture *networkFeeRPC) *SOLScanner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "getSlot":
			response["result"] = fixture.tip
		case "getFirstAvailableBlock":
			if fixture.floorError {
				response["error"] = map[string]any{"code": -32000, "message": "retention check unavailable"}
			} else {
				response["result"] = fixture.floor
			}
		case "getBlocks":
			var first, last uint64
			if len(request.Params) < 2 || json.Unmarshal(request.Params[0], &first) != nil || json.Unmarshal(request.Params[1], &last) != nil {
				http.Error(w, "invalid slot range", http.StatusBadRequest)
				return
			}
			var config struct {
				MinContextSlot *uint64 `json:"minContextSlot"`
			}
			if len(request.Params) > 2 && json.Unmarshal(request.Params[2], &config) != nil {
				http.Error(w, "invalid getBlocks config", http.StatusBadRequest)
				return
			}
			if config.MinContextSlot != nil {
				fixture.minContextSlots = append(fixture.minContextSlots, *config.MinContextSlot)
			}
			fixture.windows = append(fixture.windows, [2]uint64{first, last})
			switch {
			case fixture.fail:
				response["error"] = map[string]any{"code": -32000, "message": "temporary sampling failure"}
			case fixture.root != 0 && config.MinContextSlot != nil && *config.MinContextSlot > fixture.root:
				response["error"] = map[string]any{"code": -32016, "message": "Minimum context slot has not been reached"}
			case fixture.root != 0:
				// Agave clamps a finalized listing to the serving backend's root
				// without reporting that the requested range was truncated.
				listed := fixture.blocks
				if listed == nil {
					listed = []uint64{last}
				}
				clamped := make([]uint64, 0, len(listed))
				for _, b := range listed {
					if b <= min(last, fixture.root) {
						clamped = append(clamped, b)
					}
				}
				response["result"] = clamped
			case fixture.blocks != nil:
				response["result"] = fixture.blocks
			default:
				response["result"] = []uint64{last}
			}
		case "getBlock":
			var slot uint64
			if len(request.Params) == 0 || json.Unmarshal(request.Params[0], &slot) != nil {
				http.Error(w, "invalid block slot", http.StatusBadRequest)
				return
			}
			if fixture.blockErrors[slot] {
				response["error"] = map[string]any{"code": -32000, "message": "temporary block failure"}
				break
			}
			fees := []uint64{fixture.rate, fixture.rate, fixture.rate, fixture.rate}
			if configured, ok := fixture.blockFees[slot]; ok {
				fees = configured
			}
			transactions := make([]any, len(fees))
			for i := range transactions {
				transactions[i] = map[string]any{
					"transaction": map[string]any{"signatures": []string{"sig"}, "message": map[string]any{}},
					"meta":        map[string]any{"fee": fees[i], "err": nil},
				}
			}
			response["result"] = map[string]any{"transactions": transactions}
		default:
			http.Error(w, "unexpected RPC method "+request.Method, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	cfg := getConfigForTest(server.URL)
	cfg.GasCacheBlocks = 3
	cfg.GasPriceResolution = 100
	cfg.Solana.FeeUpdateInterval = 50
	cfg.Solana.FeeSampleSlots = 10
	scanner := &SOLScanner{
		cfg: cfg, logger: zerolog.Nop(), solRpc: rpc.NewSolRPC(server.URL, time.Second),
		bridge: &networkFeeBridge{}, pubKeyMgr: &networkFeePubKeys{},
		lastMimirCheck: time.Now(), lastHeight: fixture.tip,
		netFeeSlotMultiple: 2, globalNetworkFeeQueue: make(chan common.NetworkFee, 10),
	}
	return scanner
}

func TestSOLNetworkFeeIndependentOfPreviousSamples(t *testing.T) {
	for _, oldRate := range []uint64{5000, 9000} {
		fixture := &networkFeeRPC{tip: 201, rate: oldRate}
		scanner := newNetworkFeeScanner(t, fixture)
		warmNetworkFeeSamples(t, scanner)
		fixture.mu.Lock()
		fixture.rate = 7000
		fixture.mu.Unlock()
		require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(200))
		require.Len(t, scanner.globalNetworkFeeQueue, 1)
		fee := <-scanner.globalNetworkFeeQueue
		require.Equal(t, int64(200), fee.Height)
		require.Equal(t, uint64(7000), fee.TransactionRate, "same slot must not depend on earlier scans")
	}
}

func warmNetworkFeeSamples(t *testing.T, scanner *SOLScanner) {
	t.Helper()
	for _, slot := range []uint64{50, 100, 150} {
		require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(slot))
	}
	for len(scanner.globalNetworkFeeQueue) > 0 {
		<-scanner.globalNetworkFeeQueue
	}
}

func TestSOLNetworkFeeSamplingFailureDoesNotPublishCache(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 9000}
	scanner := newNetworkFeeScanner(t, fixture)
	warmNetworkFeeSamples(t, scanner)
	fixture.mu.Lock()
	fixture.fail = true
	fixture.mu.Unlock()
	scanner.lastFeeRate.Store(5000)
	require.Error(t, scanner.checkRecentBlocksAndUpdateNetworkFee(200))
	require.Empty(t, scanner.globalNetworkFeeQueue, "a failed sample must not relabel the previous fee with a new slot")
	require.Equal(t, uint64(5000), scanner.lastFeeRate.Load())
}

func TestSOLNetworkFeeScanDoesNotSkipBoundary(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	scanner.scan()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.NotEmpty(t, fixture.windows)
	require.Equal(t, uint64(150), fixture.windows[0][1], "after slot 100, process 150 before 200")
}

func TestSOLNetworkFeeScanRetriesFailedBoundary(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000, fail: true}
	scanner := newNetworkFeeScanner(t, fixture)
	scanner.scan()
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple, "failed sampling must leave the reporting cursor unchanged")
	fixture.mu.Lock()
	fixture.fail = false
	fixture.mu.Unlock()
	scanner.scan()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.GreaterOrEqual(t, len(fixture.windows), 2)
	require.Equal(t, fixture.windows[0], fixture.windows[1], "retry the failed bucket before advancing")
}

func TestSOLNetworkFeeScanCatchesUpWithoutDuplicates(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	for range 3 {
		scanner.scan()
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, [][2]uint64{{140, 150}, {190, 200}}, fixture.windows)
	require.Len(t, scanner.globalNetworkFeeQueue, 2)
	require.Equal(t, int64(150), (<-scanner.globalNetworkFeeQueue).Height)
	require.Equal(t, int64(200), (<-scanner.globalNetworkFeeQueue).Height)
}

func TestSOLNetworkFeeWaitsUntilBoundaryIsCrossed(t *testing.T) {
	fixture := &networkFeeRPC{tip: 150, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	scanner.scan()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Empty(t, fixture.windows)
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple)
}

func TestSOLNetworkFeeFailedBlockDoesNotPublishPartialSample(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 9000}
	scanner := newNetworkFeeScanner(t, fixture)
	warmNetworkFeeSamples(t, scanner)
	fixture.mu.Lock()
	fixture.rate = 7000
	fixture.blocks = []uint64{190, 195, 200}
	fixture.blockFees = map[uint64][]uint64{200: {7000}}
	fixture.blockErrors = map[uint64]bool{195: true}
	fixture.mu.Unlock()
	scanner.lastFeeRate.Store(5000)
	require.Error(t, scanner.checkRecentBlocksAndUpdateNetworkFee(200))
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(5000), scanner.lastFeeRate.Load())
	fixture.mu.Lock()
	fixture.blockErrors = nil
	fixture.mu.Unlock()
	require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(200))
	require.Len(t, scanner.globalNetworkFeeQueue, 1)
	require.Equal(t, uint64(7000), (<-scanner.globalNetworkFeeQueue).TransactionRate)
}

func TestSOLNetworkFeeSkippedSlotsDoNotRequireBlocks(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000, blocks: []uint64{141, 147, 149}}
	scanner := newNetworkFeeScanner(t, fixture)
	require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(150))
	require.Len(t, scanner.globalNetworkFeeQueue, 1)
	require.Equal(t, int64(150), (<-scanner.globalNetworkFeeQueue).Height)
}

func TestSOLNetworkFeeFailureDoesNotStopVaultScan(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 7000, fail: true})
	scanner.lastHeight = 0
	scanner.db = &networkFeeStorage{}
	scanner.healthy = &atomic.Bool{}
	var reportedSlot int64
	scanner.solvencyReporter = func(slot int64) error {
		reportedSlot = slot
		return nil
	}
	scanner.scan()
	require.Equal(t, int64(201), reportedSlot, "fee RPC failures must not stop independent vault/solvency scanning")
	require.Equal(t, uint64(201), scanner.getLastHeight())
}

func TestSOLNetworkFeeUnchangedNetworkRateDoesNotPublish(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 5000})
	scanner.scan()
	require.Empty(t, scanner.globalNetworkFeeQueue, "steady fees must not add a new report every boundary")
	require.Equal(t, uint64(3), scanner.netFeeSlotMultiple, "a successful unchanged sample still advances")
	require.Equal(t, uint64(5000), scanner.lastFeeRate.Load())
}

func TestSOLNetworkFeeBridgeFailureRetriesWithoutPublication(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 7000})
	bridge := &networkFeeBridge{err: errors.New("THORChain API temporarily unavailable")}
	scanner.bridge = bridge
	scanner.scan()
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple)
	bridge.err = nil
	scanner.scan()
	require.Len(t, scanner.globalNetworkFeeQueue, 1)
	require.Equal(t, int64(150), (<-scanner.globalNetworkFeeQueue).Height)
	require.Equal(t, uint64(3), scanner.netFeeSlotMultiple)
}

func TestSOLNetworkFeeLongOutageBoundsAvailableHistoryReplay(t *testing.T) {
	fixture := &networkFeeRPC{tip: 10001, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	for range 3 {
		scanner.scan()
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, [][2]uint64{{9940, 9950}, {9990, 10000}}, fixture.windows,
		"an archival provider must not trigger replay of every ancient fee boundary")
}

func TestSOLNetworkFeeLowVolumeRequiresHistoryCoverage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		blocks     []uint64
		floor      uint64
		floorError bool
		wantError  bool
	}{
		{name: "complete", blocks: []uint64{140, 145, 150}},
		{name: "partially_pruned", blocks: []uint64{150}, floor: 149, wantError: true},
		{name: "coverage_unknown", blocks: []uint64{150}, floorError: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &networkFeeRPC{
				tip: 201, rate: 9000, blocks: tc.blocks,
				blockFees: map[uint64][]uint64{150: {5000}}, floor: tc.floor, floorError: tc.floorError,
			}
			scanner := newNetworkFeeScanner(t, fixture)
			scanner.cfg.GasCacheBlocks = 100
			err := scanner.checkRecentBlocksAndUpdateNetworkFee(150)
			if tc.wantError {
				require.Error(t, err)
				require.Empty(t, scanner.globalNetworkFeeQueue)
				return
			}
			require.NoError(t, err)
			require.Len(t, scanner.globalNetworkFeeQueue, 1)
			require.Equal(t, uint64(9000), (<-scanner.globalNetworkFeeQueue).TransactionRate)
		})
	}
}

func TestSOLNetworkFeeRejectsInvalidBlockListing(t *testing.T) {
	for _, blocks := range [][]uint64{{139, 150}, {150, 151}, {150, 150}} {
		scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 7000, blocks: blocks})
		require.Error(t, scanner.checkRecentBlocksAndUpdateNetworkFee(150))
		require.Empty(t, scanner.globalNetworkFeeQueue)
	}
}

func TestSOLNetworkFeeZeroIntervalDoesNotPanic(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 7000})
	scanner.cfg.Solana.FeeUpdateInterval = 0
	require.NotPanics(t, scanner.scan)
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple)
}

func TestSOLNetworkFeeBlockedPublicationStops(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, rate: 7000})
	scanner.globalNetworkFeeQueue = make(chan common.NetworkFee)
	scanner.stopChan = make(chan struct{})
	bridgeCalled := make(chan struct{})
	scanner.bridge = &networkFeeBridge{called: bridgeCalled}
	done := make(chan error, 1)
	go func() { done <- scanner.checkRecentBlocksAndUpdateNetworkFee(150) }()
	select {
	case <-bridgeCalled:
	case <-time.After(time.Second):
		close(scanner.stopChan)
		t.Fatal("fee producer did not reach publication")
	}
	select {
	case err := <-done:
		t.Fatalf("unbuffered publication returned before shutdown: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(scanner.stopChan)
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		// Release a broken producer before failing so the regression cannot leak it.
		select {
		case <-scanner.globalNetworkFeeQueue:
		case <-time.After(time.Second):
		}
		t.Fatal("shutdown did not release the fee producer")
	}
	require.Zero(t, scanner.lastFeeRate.Load())
}

func TestSOLNetworkFeeStartupRetriesAnchoredBoundary(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000, fail: true}
	scanner := newNetworkFeeScanner(t, fixture)
	scanner.netFeeSlotMultiple = 0
	require.Error(t, scanner.initializeNetworkFee(201))
	require.NotNil(t, scanner.netFeePendingSlot)
	require.Equal(t, uint64(200), *scanner.netFeePendingSlot)
	require.Zero(t, scanner.netFeeSlotMultiple)
	fixture.mu.Lock()
	fixture.fail = false
	fixture.tip = 251
	fixture.mu.Unlock()
	scanner.lastHeight = 251
	scanner.scan()
	require.Nil(t, scanner.netFeePendingSlot)
	require.Equal(t, uint64(4), scanner.netFeeSlotMultiple)
	require.Equal(t, int64(200), (<-scanner.globalNetworkFeeQueue).Height)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, [][2]uint64{{190, 200}, {190, 200}}, fixture.windows)
}

func TestSOLNetworkFeeEmptyWindowAdvancesWithoutPublishing(t *testing.T) {
	scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201, blocks: []uint64{}})
	scanner.scan()
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(3), scanner.netFeeSlotMultiple)
}

func TestSOLNetworkFeePrunedWindowReanchorsWithoutCompleting(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000, floor: 160, blocks: []uint64{}}
	scanner := newNetworkFeeScanner(t, fixture)
	scanner.scan()
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple)
	require.NotNil(t, scanner.netFeePendingSlot)
	require.Equal(t, uint64(200), *scanner.netFeePendingSlot)
	fixture.mu.Lock()
	fixture.blocks = nil
	fixture.mu.Unlock()
	scanner.scan()
	require.Nil(t, scanner.netFeePendingSlot)
	require.Equal(t, uint64(4), scanner.netFeeSlotMultiple)
	require.Equal(t, int64(200), (<-scanner.globalNetworkFeeQueue).Height)
}

func TestSOLNetworkFeeLaggingListingBackendRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		root uint64
	}{
		{name: "truncated", root: 147},
		{name: "empty", root: 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &networkFeeRPC{
				tip: 201, rate: 7000, root: tc.root, blocks: []uint64{141, 145, 150},
				blockFees: map[uint64][]uint64{145: {9000, 9000, 9000, 9000}},
			}
			scanner := newNetworkFeeScanner(t, fixture)
			scanner.scan()
			require.Empty(t, scanner.globalNetworkFeeQueue, "a listing clamped below the boundary must not be voted")
			require.Equal(t, uint64(2), scanner.netFeeSlotMultiple, "a lagging listing backend must not complete the boundary")
			fixture.mu.Lock()
			fixture.root = 0
			fixture.mu.Unlock()
			scanner.scan()
			require.Len(t, scanner.globalNetworkFeeQueue, 1)
			fee := <-scanner.globalNetworkFeeQueue
			require.Equal(t, int64(150), fee.Height)
			require.Equal(t, uint64(7000), fee.TransactionRate)
			require.Equal(t, uint64(3), scanner.netFeeSlotMultiple)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			require.Equal(t, []uint64{150, 150}, fixture.minContextSlots, "the boundary must be the listing's minimum context slot")
		})
	}
}

func TestSOLNetworkFeeLateNodeAttestsCommittedBoundary(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	// Consensus for slot 200 at 7000 already landed before this node sampled it.
	scanner.bridge = &networkFeeBridge{agreed: 7000, lastObservedIn: 200}
	require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(200))
	require.Len(t, scanner.globalNetworkFeeQueue, 1, "late signer must attest to earn the refund")
	fee := <-scanner.globalNetworkFeeQueue
	require.Equal(t, int64(200), fee.Height)
	require.Equal(t, uint64(7000), fee.TransactionRate)

	// An unchanged fee agreed at an earlier boundary stays suppressed.
	scanner.bridge = &networkFeeBridge{agreed: 7000, lastObservedIn: 150}
	require.NoError(t, scanner.checkRecentBlocksAndUpdateNetworkFee(250))
	require.Empty(t, scanner.globalNetworkFeeQueue)
}

func TestSOLNetworkFeeLateSignerLookupFailureRetries(t *testing.T) {
	fixture := &networkFeeRPC{tip: 201, rate: 7000}
	scanner := newNetworkFeeScanner(t, fixture)
	bridge := &networkFeeBridge{agreed: 7000, lastObservedIn: 150, lastErr: errors.New("THORChain API temporarily unavailable")}
	scanner.bridge = bridge
	scanner.scan()
	require.Empty(t, scanner.globalNetworkFeeQueue)
	require.Equal(t, uint64(2), scanner.netFeeSlotMultiple, "an unknown agreed height must keep the boundary pending")
	bridge.lastErr = nil
	scanner.scan()
	require.Len(t, scanner.globalNetworkFeeQueue, 1)
	require.Equal(t, int64(150), (<-scanner.globalNetworkFeeQueue).Height)
	require.Equal(t, uint64(3), scanner.netFeeSlotMultiple)
}

func TestSOLNetworkFeeDeadbandAroundAgreedFee(t *testing.T) {
	for _, tc := range []struct {
		name           string
		agreed         uint64
		sample         uint64
		lastObservedIn int64
		publish        bool
	}{
		{name: "one_step_up", agreed: 5000, sample: 5100},
		{name: "one_step_down", agreed: 5000, sample: 4900},
		{name: "two_steps_up", agreed: 5000, sample: 5200, publish: true},
		{name: "two_steps_down", agreed: 5000, sample: 4800, publish: true},
		{name: "no_agreed_fee", agreed: 0, sample: 5000, publish: true},
		{name: "within_band_at_agreed_height", agreed: 5000, sample: 5100, lastObservedIn: 200},
		{name: "equal_at_agreed_height", agreed: 5000, sample: 5000, lastObservedIn: 200, publish: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner := newNetworkFeeScanner(t, &networkFeeRPC{tip: 201})
			scanner.bridge = &backfillFeeBridge{rate: tc.agreed, lastObservedIn: tc.lastObservedIn}
			require.NoError(t, scanner.sendNetworkFeeIfNecessary(200, []uint64{tc.sample}))
			if tc.publish {
				require.Len(t, scanner.globalNetworkFeeQueue, 1)
				fee := <-scanner.globalNetworkFeeQueue
				require.Equal(t, int64(200), fee.Height)
				require.Equal(t, tc.sample, fee.TransactionRate)
			} else {
				require.Empty(t, scanner.globalNetworkFeeQueue)
			}
			require.Equal(t, tc.sample, scanner.lastFeeRate.Load(), "lastFeeRate records the local sample even when suppressed")
		})
	}
}
