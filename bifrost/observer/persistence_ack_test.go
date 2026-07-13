package observer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestProcessTxInsAcknowledgesDurableObservation(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	observer := &Observer{
		logger:          log.Logger,
		stopChan:        stop,
		lock:            &sync.Mutex{},
		onDeck:          make(map[txInKey]*types.TxIn),
		globalTxsQueue:  make(chan types.TxIn, 1),
		storage:         storage,
		observerWorkers: 1,
		lastNodeStatus:  stypes.NodeStatus_Active,
	}
	go func() {
		observer.processTxIns()
		close(done)
	}()

	persisted := make(chan error, 1)
	observer.globalTxsQueue <- types.TxIn{
		Chain:    common.XMRChain,
		Filtered: true,
		TxArray: []*types.TxInItem{
			{BlockHeight: 123, Tx: "xmr-observation"},
		},
		Persisted: persisted,
	}
	require.NoError(t, waitPersistenceAck(t, persisted))

	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].TxArray, 1)
	assert.Equal(t, "xmr-observation", stored[0].TxArray[0].Tx)

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataAcknowledgesDurableOutbox(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-orphan"}
	persisted := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     456,
		DeliveryID: "xmr-reorg-456-xmr-orphan",
		Txs:        []types.ErrataTx{tx},
		Persisted:  persisted,
	}
	require.NoError(t, waitPersistenceAck(t, persisted))

	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.DeliverySeen)
	assert.False(t, record.Committed)

	close(stop)
	waitObserverLoop(t, done)
}

func TestQuorumErrataCommitCompletesDurableOutbox(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "committed-orphan"}
	require.NoError(t, storage.AddErrataBlock(types.ErrataBlock{
		Height: 789,
		Txs:    []types.ErrataTx{tx},
	}))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))

	gossip := &AttestationGossip{
		logger:      log.Logger,
		errataTxs:   make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
		observedTxs: make(map[txKey]*AttestationState[*attestableObservedTx]),
	}
	gossip.SetErrataCommittedHandler(func(errata common.ErrataTx) {
		require.NoError(t, storage.MarkErrataCommitted(types.ErrataTx{
			Chain: errata.Chain,
			TxID:  errata.Id,
		}))
	})
	quorumErrata := common.QuorumErrataTx{
		ErrataTx: &common.ErrataTx{Chain: tx.Chain, Id: tx.TxID},
	}
	payload, err := quorumErrata.Marshal()
	require.NoError(t, err)
	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func waitPersistenceAck(t *testing.T, persisted <-chan error) error {
	t.Helper()
	select {
	case err := <-persisted:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observer persistence acknowledgement")
		return nil
	}
}

func waitObserverLoop(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observer loop to stop")
	}
}
