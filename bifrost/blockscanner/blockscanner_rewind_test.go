package blockscanner

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "gopkg.in/check.v1"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type failingRewindStorage struct {
	*MockScannerStorage
	err error
}

func (s *failingRewindStorage) SetScanPos(block int64) error {
	if s.err != nil {
		return s.err
	}
	return s.MockScannerStorage.SetScanPos(block)
}

func newCursorRewindUnitScanner(storage ScannerStorage, previousHeight, floor int64) *BlockScanner {
	return &BlockScanner{
		cfg: config.BifrostBlockScannerConfiguration{
			FloorBlockHeight: floor,
		},
		scannerStorage: storage,
		previousBlock:  previousHeight,
	}
}

func (s *BlockScannerExtraSuite) TestCursorRewindUsesNextHeightMinusOne(c *C) {
	storage := NewMockScannerStorage()
	c.Assert(storage.SetScanPos(199), IsNil)
	scanner := newCursorRewindUnitScanner(storage, 199, 0)

	// This is the production XMR recovery shape: a 72-block cursor rewind.
	c.Assert(scanner.rewindToNextHeight(200, 128), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(127))
	position, err := storage.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(position, Equals, int64(127))
}

func (s *BlockScannerExtraSuite) TestCursorRewindRejectsNonBackwardHeights(c *C) {
	for _, nextHeight := range []int64{0, 200, 201} {
		storage := NewMockScannerStorage()
		c.Assert(storage.SetScanPos(199), IsNil)
		scanner := newCursorRewindUnitScanner(storage, 199, 0)

		c.Check(scanner.rewindToNextHeight(200, nextHeight), NotNil)
		c.Check(scanner.PreviousHeight(), Equals, int64(199))
		position, err := storage.GetScanPos()
		c.Assert(err, IsNil)
		c.Check(position, Equals, int64(199))
	}
}

func (s *BlockScannerExtraSuite) TestCursorRewindHardLimitBoundary(c *C) {
	storage := NewMockScannerStorage()
	c.Assert(storage.SetScanPos(999), IsNil)
	scanner := newCursorRewindUnitScanner(storage, 999, 0)

	// Exactly 730 blocks is permitted.
	c.Assert(scanner.rewindToNextHeight(1000, 270), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(269))

	storage = NewMockScannerStorage()
	c.Assert(storage.SetScanPos(999), IsNil)
	scanner = newCursorRewindUnitScanner(storage, 999, 0)

	// One block beyond the hard safety horizon is rejected without mutation.
	c.Check(scanner.rewindToNextHeight(1000, 269), NotNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(999))
	position, err := storage.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(position, Equals, int64(999))
}

func (s *BlockScannerExtraSuite) TestCursorRewindRespectsFloorWithoutClamping(c *C) {
	storage := NewMockScannerStorage()
	c.Assert(storage.SetScanPos(199), IsNil)
	scanner := newCursorRewindUnitScanner(storage, 199, 100)

	// next=floor+1 makes the durable previous height exactly the floor.
	c.Assert(scanner.rewindToNextHeight(200, 101), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(100))

	storage = NewMockScannerStorage()
	c.Assert(storage.SetScanPos(199), IsNil)
	scanner = newCursorRewindUnitScanner(storage, 199, 100)

	// next=floor would require previous=floor-1. Reject it; never clamp it.
	c.Check(scanner.rewindToNextHeight(200, 100), NotNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(199))
	position, err := storage.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(position, Equals, int64(199))
}

func (s *BlockScannerExtraSuite) TestCursorRewindStorageFailureLeavesRAMUnchanged(c *C) {
	underlying := NewMockScannerStorage()
	c.Assert(underlying.SetScanPos(199), IsNil)
	storage := &failingRewindStorage{
		MockScannerStorage: underlying,
		err:                errors.New("forced synchronous write failure"),
	}
	scanner := newCursorRewindUnitScanner(storage, 199, 0)

	c.Check(scanner.rewindToNextHeight(200, 128), ErrorMatches, ".*forced synchronous write failure")
	c.Check(scanner.PreviousHeight(), Equals, int64(199))
	position, err := underlying.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(position, Equals, int64(199))
}

func (s *BlockScannerExtraSuite) TestCursorRewindSurvivesStorageRestart(c *C) {
	folder := c.MkDir()
	storage, err := NewBlockScannerStorage(folder, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	scanner := newCursorRewindUnitScanner(storage, 199, 0)

	c.Assert(scanner.rewindToNextHeight(200, 128), IsNil)
	c.Assert(storage.Close(), IsNil)

	reopened, err := NewBlockScannerStorage(folder, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer reopened.Close()
	position, err := reopened.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(position, Equals, int64(127))
}

func (s *BlockScannerExtraSuite) TestRewindRequiredErrorExtractionAcceptsWrappedValueAndPointer(c *C) {
	nextHeight, ok := rewindRequiredNextHeight(fmt.Errorf("wrapped: %w", btypes.RewindRequiredError{NextHeight: 128}))
	c.Check(ok, Equals, true)
	c.Check(nextHeight, Equals, int64(128))

	nextHeight, ok = rewindRequiredNextHeight(fmt.Errorf("wrapped: %w", &btypes.RewindRequiredError{NextHeight: 64}))
	c.Check(ok, Equals, true)
	c.Check(nextHeight, Equals, int64(64))

	_, ok = rewindRequiredNextHeight(errors.New("ordinary fetch error"))
	c.Check(ok, Equals, false)
}

type rewindPrefetchFetcher struct {
	mu              sync.Mutex
	calls           map[int64]int
	chainHeight     int64
	errorAborts     atomic.Int64
	discardedAborts atomic.Int64
}

func newRewindPrefetchFetcher(chainHeight int64) *rewindPrefetchFetcher {
	return &rewindPrefetchFetcher{
		calls:       make(map[int64]int),
		chainHeight: chainHeight,
	}
}

func (f *rewindPrefetchFetcher) FetchMemPool(int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

func (f *rewindPrefetchFetcher) FetchTxs(fetchHeight, _ int64) (stypes.TxIn, error) {
	f.mu.Lock()
	f.calls[fetchHeight]++
	call := f.calls[fetchHeight]
	f.mu.Unlock()

	if fetchHeight == 11 && call == 1 {
		return stypes.TxIn{
			AbortPersistence: func() {
				f.errorAborts.Add(1)
			},
		}, fmt.Errorf("sidecar cursor moved: %w", btypes.RewindRequiredError{NextHeight: 9})
	}
	if fetchHeight == 12 && call == 1 {
		return stypes.TxIn{
			AbortPersistence: func() {
				f.discardedAborts.Add(1)
			},
		}, nil
	}
	return stypes.TxIn{}, nil
}

func (f *rewindPrefetchFetcher) GetHeight() (int64, error) {
	return f.chainHeight, nil
}

func (f *rewindPrefetchFetcher) GetNetworkFee() (uint64, uint64) {
	return 250, 30
}

func (f *rewindPrefetchFetcher) callCount(height int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[height]
}

type rollbackPrefetchFetcher struct {
	mu              sync.Mutex
	calls           map[int64]int
	chainHeight     int64
	persisted       chan error
	discardedAborts atomic.Int64
}

func newRollbackPrefetchFetcher(chainHeight int64) *rollbackPrefetchFetcher {
	return &rollbackPrefetchFetcher{
		calls:       make(map[int64]int),
		chainHeight: chainHeight,
		persisted:   make(chan error, 1),
	}
}

func (f *rollbackPrefetchFetcher) FetchMemPool(int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

func (f *rollbackPrefetchFetcher) FetchTxs(fetchHeight, _ int64) (stypes.TxIn, error) {
	f.mu.Lock()
	f.calls[fetchHeight]++
	call := f.calls[fetchHeight]
	f.mu.Unlock()

	if fetchHeight == 10 && call == 1 {
		return stypes.TxIn{
			Chain:     common.ETHChain,
			TxArray:   []*stypes.TxInItem{{Tx: "prefetch-rollback-test"}},
			Persisted: f.persisted,
		}, nil
	}
	if fetchHeight == 12 && call == 1 {
		return stypes.TxIn{
			AbortPersistence: func() {
				f.discardedAborts.Add(1)
			},
		}, nil
	}
	return stypes.TxIn{}, nil
}

func (f *rollbackPrefetchFetcher) GetHeight() (int64, error) {
	return f.chainHeight, nil
}

func (f *rollbackPrefetchFetcher) GetNetworkFee() (uint64, uint64) {
	return 250, 30
}

func (f *rollbackPrefetchFetcher) callCount(height int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[height]
}

func (s *BlockScannerExtraSuite) newRewindTestBridge(c *C) (*httptest.Server, thorclient.ThorchainBridge) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","lastobservedin":0,"lastsignedout":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","outbound_tx_size":"250","observed_fee_rate":"30"}]`))
		}
	}))
	return server, s.makeBridge(c, server)
}

func waitForFetchCalls(fetchCount func(int64) int, height int64, wanted int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fetchCount(height) >= wanted {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (s *BlockScannerExtraSuite) TestTypedCursorRewindClearsStalePrefetch(c *C) {
	server, bridge := s.newRewindTestBridge(c)
	defer server.Close()

	storage := NewMockScannerStorage()
	fetcher := newRewindPrefetchFetcher(12)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           9,
		BlockHeightDiscoverBackoff: 5 * time.Millisecond,
		ChainID:                    common.ETHChain,
		PrefetchBlocks:             3,
		MaxHealthyLag:              time.Hour,
	}, storage, m, bridge, fetcher)
	c.Assert(err, IsNil)

	scanner.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4))
	defer scanner.Stop()

	// Height 12 was fetched on the abandoned 10->11 cursor lineage. It must be
	// fetched a second time after the typed rewind restarts at height 9.
	c.Assert(waitForFetchCalls(fetcher.callCount, 12, 2), Equals, true)
	c.Check(fetcher.callCount(9) > 0, Equals, true)
	c.Check(fetcher.errorAborts.Load(), Equals, int64(1))
	c.Check(fetcher.discardedAborts.Load(), Equals, int64(1))
}

func (s *BlockScannerExtraSuite) TestRollbackChannelClearsStalePrefetch(c *C) {
	server, bridge := s.newRewindTestBridge(c)
	defer server.Close()

	storage := NewMockScannerStorage()
	fetcher := newRollbackPrefetchFetcher(12)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           9,
		BlockHeightDiscoverBackoff: 5 * time.Millisecond,
		ChainID:                    common.ETHChain,
		PrefetchBlocks:             3,
		MaxHealthyLag:              time.Hour,
	}, storage, m, bridge, fetcher)
	c.Assert(err, IsNil)

	scanner.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4))
	defer scanner.Stop()
	c.Assert(waitForFetchCalls(fetcher.callCount, 12, 1), Equals, true)

	// The scanner loop is parked in waitTxInPersistence for height 10, so it cannot
	// consume the height-11/12 prefetch until we release persistence below. Park the
	// rollback sender on the unbuffered rollbackChan first: when the loop wakes and
	// reaches its top-level select, the ready rollback case wins over the default
	// branch. Otherwise the loop races ahead, consumes the stale prefetch normally,
	// and clearPrefetch finds nothing to abort. The sleep only lets the rollback
	// goroutine reach its blocking send while the loop is still frozen.
	rollbackSent := make(chan struct{})
	go func() {
		scanner.rollbackChan <- 9
		close(rollbackSent)
	}()
	time.Sleep(200 * time.Millisecond)
	fetcher.persisted <- nil

	select {
	case <-rollbackSent:
	case <-time.After(3 * time.Second):
		c.Fatal("timed out delivering rollback request")
	}

	// The prefetched height 12 from before rollback must not survive the move
	// back to height 9.
	c.Assert(waitForFetchCalls(fetcher.callCount, 12, 2), Equals, true)
	c.Check(fetcher.discardedAborts.Load(), Equals, int64(1))
}

type abortingErrorFetcher struct {
	aborts atomic.Int64
}

func (f *abortingErrorFetcher) FetchMemPool(int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

func (f *abortingErrorFetcher) FetchTxs(int64, int64) (stypes.TxIn, error) {
	return stypes.TxIn{
		AbortPersistence: func() {
			f.aborts.Add(1)
		},
	}, errors.New("fetch failed after reserving replay delivery")
}

func (f *abortingErrorFetcher) GetHeight() (int64, error) {
	return 2, nil
}

func (f *abortingErrorFetcher) GetNetworkFee() (uint64, uint64) {
	return 250, 30
}

func (s *BlockScannerExtraSuite) TestFetchErrorAbortsPersistenceReservation(c *C) {
	server, bridge := s.newRewindTestBridge(c)
	defer server.Close()

	fetcher := &abortingErrorFetcher{}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockHeightDiscoverBackoff: 5 * time.Millisecond,
		ChainID:                    common.ETHChain,
	}, NewMockScannerStorage(), m, bridge, fetcher)
	c.Assert(err, IsNil)

	scanner.Start(make(chan stypes.TxIn, 1), make(chan common.NetworkFee, 1))
	defer scanner.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fetcher.aborts.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	c.Check(fetcher.aborts.Load() > 0, Equals, true)
}
