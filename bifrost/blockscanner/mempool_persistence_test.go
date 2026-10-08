package blockscanner

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type persistenceMempoolFetcher struct {
	BlockScannerFetcher
	batch types.TxIn
}

func (f *persistenceMempoolFetcher) FetchMemPool(int64) (types.TxIn, error) { return f.batch, nil }

func TestMempoolPersistenceHandoff(t *testing.T) {
	for _, result := range []string{"stored", "failed", "stopped"} {
		t.Run(result, func(t *testing.T) {
			persisted := make(chan error, 1)
			finalized := make(chan struct{}, 1)
			aborted := make(chan struct{}, 1)
			batch := types.TxIn{
				Chain: common.DASHChain, TxArray: []*types.TxInItem{{Tx: "pending"}}, Persisted: persisted,
				FinalizePersistence: func() error { finalized <- struct{}{}; return nil },
				AbortPersistence:    func() { aborted <- struct{}{} },
			}
			scanner := &BlockScanner{
				cfg:    config.BifrostBlockScannerConfiguration{ScanMemPool: true, BlockHeightDiscoverBackoff: time.Millisecond},
				logger: zerolog.Nop(), wg: &sync.WaitGroup{}, stopChan: make(chan struct{}),
				globalTxsQueue: make(chan types.TxIn), healthy: &atomic.Bool{},
				chainScanner: &persistenceMempoolFetcher{batch: batch},
			}
			scanner.wg.Add(1)
			go scanner.scanMempool()
			var once sync.Once
			stop := func() { once.Do(func() { close(scanner.stopChan) }); scanner.wg.Wait() }
			defer stop()
			select {
			case <-scanner.globalTxsQueue:
			case <-time.After(time.Second):
				t.Fatal("delivery was not queued")
			}
			select {
			case <-finalized:
				t.Fatal("finalized before observer acknowledgement")
			default:
			}
			switch result {
			case "stored":
				persisted <- nil
			case "failed":
				persisted <- errors.New("disk full")
			default:
				once.Do(func() { close(scanner.stopChan) })
			}
			if result == "stored" {
				select {
				case <-finalized:
				case <-time.After(time.Second):
					t.Fatal("successful delivery was not finalized")
				}
			} else {
				select {
				case <-aborted:
				case <-time.After(time.Second):
					t.Fatal("failed delivery was not released for retry")
				}
				select {
				case <-finalized:
					t.Fatal("failed delivery was finalized")
				default:
				}
			}
		})
	}
}
