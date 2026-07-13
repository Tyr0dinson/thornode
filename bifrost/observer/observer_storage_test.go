package observer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func setupTestDB(t *testing.T) (*ObserverStorage, string) {
	tempDir, err := os.MkdirTemp("", "observer-storage-test")
	require.NoError(t, err)

	opts := config.LevelDBOptions{
		FilterBitsPerKey:              10,
		CompactionTableSizeMultiplier: 1,
		WriteBuffer:                   4194304,
		BlockCacheCapacity:            8388608,
		CompactOnInit:                 true,
	}

	storage, err := NewObserverStorage(tempDir, opts)
	require.NoError(t, err)
	require.NotNil(t, storage)

	return storage, tempDir
}

func cleanupTestDB(t *testing.T, storage *ObserverStorage, tempDir string) {
	err := storage.Close()
	require.NoError(t, err)
	err = os.RemoveAll(tempDir)
	require.NoError(t, err)
}

func createTestTxIn(chain common.Chain, blockHeight int64) *types.TxIn {
	return &types.TxIn{
		Chain: chain,
		TxArray: []*types.TxInItem{
			{
				BlockHeight: blockHeight,
				Tx:          "tx-data",
				Memo:        "memo",
			},
		},
	}
}

func TestNewObserverStorage(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer os.RemoveAll(tempDir)

		assert.NotNil(t, storage)
		assert.NotNil(t, storage.db)

		err := storage.Close()
		assert.NoError(t, err)
	})

	t.Run("creation failure", func(t *testing.T) {
		// Create a file where we want to create the DB to force an error
		tempDir, err := os.MkdirTemp("", "observer-storage-test")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create a file with the same name as the path to cause an error
		dbPath := filepath.Join(tempDir, "invalid-db")
		err = os.WriteFile(dbPath, []byte("test"), 0o600)
		require.NoError(t, err)

		opts := config.LevelDBOptions{}
		storage, err := NewObserverStorage(dbPath, opts)

		assert.Error(t, err)
		assert.Nil(t, storage)
	})
}

func TestCreateTxKey(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	testCases := []struct {
		name        string
		txIn        *types.TxIn
		expectedKey string
	}{
		{
			name:        "BTC mempool transaction",
			txIn:        createTestTxIn(common.BTCChain, 100),
			expectedKey: "txs:BTC:100",
		},
		{
			name:        "ETH non-mempool transaction",
			txIn:        createTestTxIn(common.ETHChain, 200),
			expectedKey: "txs:ETH:200",
		},
		{
			name: "transaction with no items",
			txIn: &types.TxIn{
				Chain:   common.BTCChain,
				MemPool: true,
				TxArray: []*types.TxInItem{},
			},
			expectedKey: "txs:BTC:0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := storage.createTxKey(tc.txIn, 0)
			assert.Equal(t, tc.expectedKey, key)
		})
	}
}

func TestPendingErrataPersistsUntilCommitted(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: common.TxID("deadbeef")}
	block := types.ErrataBlock{
		Height:     123,
		DeliveryID: "xmr-reorg-123-deadbeef",
		Txs:        []types.ErrataTx{tx},
	}
	require.NoError(t, storage.AddErrataBlock(block))

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, block.Height, pending[0].Height)
	assert.Equal(t, block.DeliveryID, pending[0].DeliveryID)
	assert.Equal(t, tx, pending[0].Tx)
	assert.True(t, pending[0].DeliverySeen)
	assert.False(t, pending[0].CleanupComplete)
	assert.False(t, pending[0].Committed)

	// Re-persisting the same chain/tx is idempotent.
	require.NoError(t, storage.AddErrataBlock(block))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// Neither half of the state machine may remove the record alone.
	require.NoError(t, storage.MarkErrataCommitted(tx))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.True(t, pending[0].Committed)
	assert.False(t, pending[0].CleanupComplete)

	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestPendingErrataCommitBeforeScannerDeliveryIsNotLost(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: common.TxID("early-commit")}
	require.NoError(t, storage.MarkErrataCommitted(tx))
	observer := &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  make(map[txInKey]*types.TxIn),
		storage: storage,
	}

	// The durable-outbox retry tick synthesizes a block from every pending
	// record. A commit-only tombstone has no scanner delivery metadata and must
	// not enter local cleanup through that path.
	require.NoError(t, observer.attestErrataBlock(context.Background(), types.ErrataBlock{
		Txs: []types.ErrataTx{tx},
	}))

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.True(t, pending[0].Committed)
	assert.False(t, pending[0].CleanupComplete)
	assert.False(t, pending[0].DeliverySeen)

	// A later scanner delivery observes the durable tombstone and completes the
	// state machine without recreating an uncommitted outbox entry.
	delivered := types.ErrataBlock{
		Height:     321,
		DeliveryID: "xmr-reorg-321-early-commit",
		Txs:        []types.ErrataTx{tx},
	}
	require.NoError(t, storage.AddErrataBlock(delivered))
	require.NoError(t, observer.attestErrataBlock(context.Background(), delivered))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestPruneCommittedOnlyErrataHonorsSafetyAndDeliveryState(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	now := time.Now()
	old := now.Add(-31 * 24 * time.Hour)
	cutoff := now.Add(-30 * 24 * time.Hour)
	oldCommittedOnly := types.ErrataTx{Chain: common.XMRChain, TxID: "old-committed-only"}
	recentCommittedOnly := types.ErrataTx{Chain: common.XMRChain, TxID: "recent-committed-only"}
	deliveryPending := types.ErrataTx{Chain: common.XMRChain, TxID: "delivery-pending"}
	cleanupPending := types.ErrataTx{Chain: common.XMRChain, TxID: "cleanup-pending"}
	legacyTimestamp := types.ErrataTx{Chain: common.XMRChain, TxID: "legacy-no-commit-time"}

	for _, tx := range []types.ErrataTx{
		oldCommittedOnly,
		recentCommittedOnly,
		deliveryPending,
		cleanupPending,
		legacyTimestamp,
	} {
		require.NoError(t, storage.MarkErrataCommitted(tx))
	}
	require.NoError(t, storage.AddErrataBlock(types.ErrataBlock{
		Height:     900,
		DeliveryID: "delivery-must-survive-pruning",
		Txs:        []types.ErrataTx{deliveryPending},
	}))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{cleanupPending}, nil))

	setCommittedAt := func(tx types.ErrataTx, timestamp int64) {
		t.Helper()
		storage.errataMu.Lock()
		defer storage.errataMu.Unlock()
		record, found, err := storage.getPersistedErrataLocked(tx)
		require.NoError(t, err)
		require.True(t, found)
		record.CommittedAtUnixNano = timestamp
		require.NoError(t, storage.writePersistedErrataLocked(record))
	}
	setCommittedAt(oldCommittedOnly, old.UnixNano())
	setCommittedAt(deliveryPending, old.UnixNano())
	setCommittedAt(cleanupPending, old.UnixNano())
	setCommittedAt(legacyTimestamp, 0)

	pruned, err := storage.PruneCommittedOnlyErrata(cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, pruned)

	_, found, err := storage.GetPendingErrataTx(oldCommittedOnly)
	require.NoError(t, err)
	assert.False(t, found, "only an expired committed-only tombstone may be removed")
	for _, tx := range []types.ErrataTx{recentCommittedOnly, deliveryPending, cleanupPending, legacyTimestamp} {
		record, retained, getErr := storage.GetPendingErrataTx(tx)
		require.NoError(t, getErr)
		require.True(t, retained, "%s must remain durable", tx.TxID)
		assert.True(t, record.Committed)
	}

	deliveryRecord, _, err := storage.GetPendingErrataTx(deliveryPending)
	require.NoError(t, err)
	assert.True(t, deliveryRecord.DeliverySeen)
	assert.False(t, deliveryRecord.CleanupComplete)
	cleanupRecord, _, err := storage.GetPendingErrataTx(cleanupPending)
	require.NoError(t, err)
	assert.False(t, cleanupRecord.DeliverySeen)
	assert.True(t, cleanupRecord.CleanupComplete)
	legacyRecord, _, err := storage.GetPendingErrataTx(legacyTimestamp)
	require.NoError(t, err)
	assert.Zero(t, legacyRecord.CommittedAtUnixNano)
}

func TestFilterErrataDurablyProcessesEveryTxAndExactDeckHeight(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer os.RemoveAll(tempDir)

	first := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "tx-a"},
			{BlockHeight: 100, Tx: "tx-keep"},
			{BlockHeight: 100, Tx: "tx-c"},
		},
	}
	removeAll := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 20,
		TxArray:              []*types.TxInItem{{BlockHeight: 90, Tx: "tx-delete"}},
	}
	require.NoError(t, storage.AddOrUpdateTx(first))
	require.NoError(t, storage.AddOrUpdateTx(removeAll))

	block := types.ErrataBlock{
		// This is deliberately not either deck's finalization height (106/110).
		Height:     90,
		DeliveryID: "xmr-reorg-multi",
		Txs: []types.ErrataTx{
			{Chain: common.XMRChain, TxID: "tx-a"},
			{Chain: common.XMRChain, TxID: "tx-c"},
			{Chain: common.XMRChain, TxID: "tx-delete"},
		},
	}
	require.NoError(t, storage.AddErrataBlock(block))

	firstKey := TxInKey(first)
	removeAllKey := TxInKey(removeAll)
	observer := &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  map[txInKey]*types.TxIn{firstKey: first, removeAllKey: removeAll},
		storage: storage,
	}
	require.NoError(t, observer.filterErrataTx(block))
	require.Len(t, observer.onDeck, 1)
	require.Len(t, observer.onDeck[firstKey].TxArray, 1)
	assert.Equal(t, "tx-keep", observer.onDeck[firstKey].TxArray[0].Tx)
	assert.Equal(t, int64(6), observer.onDeck[firstKey].ConfirmationRequired)

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 3)
	for _, record := range pending {
		assert.True(t, record.CleanupComplete)
		assert.False(t, record.Committed)
	}

	// Reopen the database to prove the exact finalization-height delete/update
	// and cleanup flags survived a process boundary.
	require.NoError(t, storage.Close())
	reopened, err := NewObserverStorage(tempDir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer reopened.Close()
	storedDeck, err := reopened.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, storedDeck, 1)
	require.Len(t, storedDeck[0].TxArray, 1)
	assert.Equal(t, "tx-keep", storedDeck[0].TxArray[0].Tx)

	for _, tx := range block.Txs {
		require.NoError(t, reopened.MarkErrataCommitted(tx))
	}
	pending, err = reopened.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestFilterErrataStorageFailureLeavesMemoryAndOutboxRetryable(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer os.RemoveAll(tempDir)

	txIn := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray:              []*types.TxInItem{{BlockHeight: 100, Tx: "tx-fail"}},
	}
	block := types.ErrataBlock{
		Height: 100,
		Txs:    []types.ErrataTx{{Chain: common.XMRChain, TxID: "tx-fail"}},
	}
	require.NoError(t, storage.AddOrUpdateTx(txIn))
	require.NoError(t, storage.AddErrataBlock(block))
	key := TxInKey(txIn)
	observer := &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  map[txInKey]*types.TxIn{key: txIn},
		storage: storage,
	}

	// A closed LevelDB is a deterministic injected persistence failure. The
	// staged cleanup must not mutate in-memory state when the synced batch fails.
	require.NoError(t, storage.Close())
	err := observer.filterErrataTx(block)
	require.Error(t, err)
	require.Len(t, observer.onDeck[key].TxArray, 1)
	assert.Equal(t, "tx-fail", observer.onDeck[key].TxArray[0].Tx)

	reopened, err := NewObserverStorage(tempDir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer reopened.Close()
	storedDeck, err := reopened.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, storedDeck, 1)
	pending, err := reopened.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.False(t, pending[0].CleanupComplete)
}

func TestGetOnDeckTxs(t *testing.T) {
	t.Run("empty storage", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Empty(t, txs)
	})

	t.Run("legacy storage format", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Manually add a legacy format record
		legacyTxs := []*types.TxIn{
			createTestTxIn(common.BTCChain, 100),
			createTestTxIn(common.ETHChain, 200),
		}
		data, err := json.Marshal(legacyTxs)
		require.NoError(t, err)

		err = storage.db.Put([]byte(LegacyOnDeckKey), data, nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Len(t, txs, 2)
		assert.Equal(t, common.BTCChain, txs[0].Chain)
		assert.Equal(t, common.ETHChain, txs[1].Chain)

		// Verify legacy transactions were migrated
		for _, tx := range legacyTxs {
			key := storage.createTxKey(tx, 0)
			data, err = storage.db.Get([]byte(key), nil)
			assert.NoError(t, err)

			var storedTx types.TxIn
			err = json.Unmarshal(data, &storedTx)
			assert.NoError(t, err)
			assert.Equal(t, tx.Chain, storedTx.Chain)
			assert.Equal(t, tx.TxArray[0].BlockHeight, storedTx.TxArray[0].BlockHeight)
		}

		// Verify migration flag was set
		val, err := storage.db.Get([]byte(LegacyMigrationHandledKey), nil)
		assert.NoError(t, err)
		assert.Equal(t, "true", string(val))
	})

	t.Run("new storage format", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add transactions in new format
		tx1 := createTestTxIn(common.BTCChain, 100)
		tx2 := createTestTxIn(common.ETHChain, 200)

		err := storage.AddOrUpdateTx(tx1)
		require.NoError(t, err)
		err = storage.AddOrUpdateTx(tx2)
		require.NoError(t, err)

		// Make GetOnDeckTxs skip legacy check by setting migration flag
		err = storage.db.Put([]byte(LegacyMigrationHandledKey), []byte("true"), nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Len(t, txs, 2)

		// Check that we got both transactions (order may vary)
		foundBTC := false
		foundETH := false
		for _, tx := range txs {
			if tx.Chain.Equals(common.BTCChain) {
				foundBTC = true
			}
			if tx.Chain.Equals(common.ETHChain) {
				foundETH = true
			}
		}
		assert.True(t, foundBTC, "BTC transaction not found")
		assert.True(t, foundETH, "ETH transaction not found")
	})

	t.Run("error unmarshalling transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add invalid JSON data
		key := "txs:BTC:100"
		err := storage.db.Put([]byte(key), []byte("invalid json"), nil)
		require.NoError(t, err)

		// Make GetOnDeckTxs skip legacy check
		err = storage.db.Put([]byte(LegacyMigrationHandledKey), []byte("true"), nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.Error(t, err)
		assert.Nil(t, txs)
		assert.Contains(t, err.Error(), "fail to unmarshal ondeck tx")
	})
}

func TestAddOrUpdateTx(t *testing.T) {
	t.Run("add new transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Verify it was stored
		key := storage.createTxKey(tx, 0)
		data, err := storage.db.Get([]byte(key), nil)
		assert.NoError(t, err)

		var storedTx types.TxIn
		err = json.Unmarshal(data, &storedTx)
		assert.NoError(t, err)
		assert.Equal(t, common.BTCChain, storedTx.Chain)
		assert.Equal(t, int64(100), storedTx.TxArray[0].BlockHeight)
		assert.Equal(t, "tx-data", storedTx.TxArray[0].Tx)
	})

	t.Run("update existing transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add initial transaction
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Update the transaction
		tx.TxArray[0].Tx = "updated-tx-data"
		err = storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Verify it was updated
		key := storage.createTxKey(tx, 0)
		data, err := storage.db.Get([]byte(key), nil)
		assert.NoError(t, err)

		var storedTx types.TxIn
		err = json.Unmarshal(data, &storedTx)
		assert.NoError(t, err)
		assert.Equal(t, "updated-tx-data", storedTx.TxArray[0].Tx)
	})
}

func TestRemoveTx(t *testing.T) {
	t.Run("remove existing transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add a transaction
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Remove it
		err = storage.RemoveTx(tx, 0)
		assert.NoError(t, err)

		// Verify it's gone
		key := storage.createTxKey(tx, 0)
		_, err = storage.db.Get([]byte(key), nil)
		assert.True(t, errors.Is(err, leveldb.ErrNotFound))
	})

	t.Run("remove non-existent transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Try to remove a transaction that doesn't exist
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.RemoveTx(tx, 0)

		// This should not error, as leveldb.Delete doesn't error for non-existent keys
		assert.NoError(t, err)
	})
}

func TestClose(t *testing.T) {
	t.Run("close database", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer os.RemoveAll(tempDir)

		err := storage.Close()
		assert.NoError(t, err)

		// Verify it's closed by trying to use it (should error)
		tx := createTestTxIn(common.BTCChain, 100)
		err = storage.AddOrUpdateTx(tx)
		assert.Error(t, err)
	})
}
