package observer

import (
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A nil batch acknowledgement does not mean that an inactive Observer accepted
// a DASH item. The producer must use the per-item stored/rejected callbacks.
func TestDashDeliveryRejectsInactiveObserverBeforeAcceptingBlockFallback(t *testing.T) {
	for _, status := range []stypes.NodeStatus{stypes.NodeStatus_Unknown, stypes.NodeStatus_Standby} {
		t.Run(status.String(), func(t *testing.T) {
			storage, tempDir := setupTestDB(t)
			defer cleanupTestDB(t, storage, tempDir)
			observer := &Observer{
				logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{},
				onDeck: make(map[txInKey]*types.TxIn), observerWorkers: 1,
				globalTxsQueue: make(chan types.TxIn), stopChan: make(chan struct{}), lastNodeStatus: status,
			}
			done := make(chan struct{})
			go func() { observer.processTxIns(); close(done) }()
			defer func() { close(observer.stopChan); waitObserverLoop(t, done) }()
			stored := make(chan struct{}, 1)
			rejected := make(chan error, 1)
			observation := func() types.TxIn {
				return types.TxIn{
					Chain: common.DASHChain, Filtered: true, Persisted: make(chan error, 1),
					TxArray: []*types.TxInItem{{
						Tx: "dash-delivery", BlockHeight: 123,
						OnObservedTxStored:   func() { stored <- struct{}{} },
						OnObservedTxRejected: func(err error) { rejected <- err },
					}},
				}
			}
			first := observation()
			observer.globalTxsQueue <- first
			require.NoError(t, waitPersistenceAck(t, first.Persisted))
			select {
			case err := <-rejected:
				require.ErrorIs(t, err, errObservedTxInactive)
			default:
				t.Fatal("inactive Observer did not reject the DASH delivery")
			}
			require.Empty(t, stored)
			observer.lastNodeStatusMu.Lock()
			observer.lastNodeStatus = stypes.NodeStatus_Active
			observer.lastNodeStatusMu.Unlock()
			fallback := observation()
			fallback.TxArray[0].BlockHeight = 150 // normal ChainLock observation
			observer.globalTxsQueue <- fallback
			require.NoError(t, waitPersistenceAck(t, fallback.Persisted))
			select {
			case <-stored:
			default:
				t.Fatal("active Observer did not acknowledge durable storage")
			}
			items, err := storage.GetOnDeckTxs()
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Len(t, items[0].TxArray, 1)
		})
	}
}

// Observer acceptance must complete the pending marker without releasing allowance.
func TestDashDeliveryCompletesMarkerAfterObserverPersistence(t *testing.T) {
	db, err := leveldb.OpenFile(t.TempDir(), nil)
	require.NoError(t, err)
	defer db.Close()
	scanner, err := sharedutxo.NewTemporalStorage(db, 0)
	require.NoError(t, err)
	require.NoError(t, scanner.StartInstantSendWarmup(10, nil))
	require.NoError(t, scanner.CompleteInstantSendWarmup(11))
	ids, err := scanner.AdmitInstantSendTxs([]sharedutxo.InstantSendClaim{{TxID: "dash-delivery", Amount: 10}}, 0, 100, true, 1)
	require.NoError(t, err)
	path := t.TempDir()
	storage, err := NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	observer := &Observer{logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{}, onDeck: make(map[txInKey]*types.TxIn)}
	item := &types.TxInItem{Tx: "dash-delivery", BlockHeight: 123, Memo: "NOOP"}
	item.OnObservedTxStored = func() { require.NoError(t, scanner.CompleteInstantSendObservations(ids)) }
	require.NoError(t, observer.processObservedTx(types.TxIn{Chain: common.DASHChain, Filtered: true, TxArray: []*types.TxInItem{item}}))
	require.NoError(t, storage.Close())
	storage, err = NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	defer storage.Close()
	deck, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, deck, 1)
	require.Len(t, deck[0].TxArray, 1)
	require.EqualValues(t, 123, deck[0].TxArray[0].BlockHeight)
	state, err := scanner.GetInstantSendState()
	require.NoError(t, err)
	require.EqualValues(t, 10, state.Reserved)
	fallback, err := scanner.TrackInstantSendBlockTxs(ids)
	require.NoError(t, err)
	require.Empty(t, fallback)
}

// A failed first write must reject the handoff without publishing an in-memory
// deck entry that could falsely acknowledge the next retry.
func TestDashDeliveryRejectsFailedInitialObserverWrite(t *testing.T) {
	path := t.TempDir()
	storage, err := NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	storage.db, err = leveldb.OpenFile(path, &opt.Options{ReadOnly: true})
	require.NoError(t, err)
	observer := &Observer{logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{}, onDeck: make(map[txInKey]*types.TxIn)}
	stored, rejected := 0, 0
	observation := func() types.TxIn {
		return types.TxIn{Chain: common.DASHChain, Filtered: true, TxArray: []*types.TxInItem{{
			Tx: "dash-delivery", BlockHeight: 123,
			OnObservedTxStored:   func() { stored++ },
			OnObservedTxRejected: func(error) { rejected++ },
		}}}
	}
	require.Error(t, observer.processObservedTx(observation()))
	require.Zero(t, stored)
	require.Equal(t, 1, rejected)
	require.Empty(t, observer.onDeck)
	require.NoError(t, storage.Close())
	storage, err = NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	defer storage.Close()
	observer.storage = storage
	require.NoError(t, observer.processObservedTx(observation()))
	require.Equal(t, 1, stored)
	deck, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, deck, 1)
}

// Multiple vault views share one completion callback, as needed for migrations.
func TestDashDeliveryVaultViewsAcknowledgeOnce(t *testing.T) {
	stored := make(chan struct{}, 2)
	source := &types.TxInItem{Tx: "migration", OnObservedTxStored: func() { stored <- struct{}{} }}
	views := []*types.TxInItem{source.Copy(), source.Copy()}
	coalesceObservedTxCallbacks(source, views)
	var wg sync.WaitGroup
	for _, item := range views {
		wg.Go(func() { item.OnObservedTxStored() })
	}
	wg.Wait()
	select {
	case <-stored:
	case <-time.After(time.Second):
		t.Fatal("vault transfer not acknowledged")
	}
	require.Empty(t, stored, "derived vault views must not acknowledge twice")
}
