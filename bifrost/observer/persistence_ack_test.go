package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
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

func TestProcessErrataAcknowledgesOnlyAfterDurableCleanup(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-orphan"}
	orphaned := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 456, Tx: tx.TxID.String()},
		},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	key := TxInKey(orphaned)
	gossip, _, _, _, _, _ := setupTestGossip(t)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    456,
		FinaliseHeight: 456,
	}, true, false))
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{key: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

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
	assert.True(t, record.CleanupComplete)
	assert.False(t, record.Committed)
	assert.Empty(t, observer.onDeck)

	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	assert.Empty(t, stored)
	gossip.mu.Lock()
	assert.Empty(t, gossip.observedTxs)
	gossip.mu.Unlock()

	close(stop)
	waitObserverLoop(t, done)
}

func TestCallbackErrataRetryPreservesCanonicalXMRRemine(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-callback-remine"}
	orphaned := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 0,
		TxArray: []*types.TxInItem{{
			BlockHeight: 600,
			Tx:          tx.TxID.String(),
		}},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	orphanedKey := TxInKey(orphaned)

	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	orphanedObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    600,
		FinaliseHeight: 600,
		XmrMinedHeight: 600,
	}
	require.NoError(t, gossip.AttestObservedTx(
		context.Background(),
		&orphanedObserved,
		true,
		false,
	))

	attestationAttempts := make(chan common.ErrataTx, 2)
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{orphanedKey: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
		errataAttestFn: func(_ context.Context, errata common.ErrataTx) error {
			attestationAttempts <- errata
			return nil
		},
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	callback := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height: 600,
		Txs:    []types.ErrataTx{tx},
		OnStored: func() {
			callback <- nil
		},
		OnRejected: func(err error) {
			callback <- err
		},
	}
	require.NoError(t, waitPersistenceAck(t, callback))

	select {
	case attempted := <-attestationAttempts:
		require.Equal(t, tx.Chain, attempted.Chain)
		require.Equal(t, tx.TxID, attempted.Id)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial callback-path errata attestation")
	}

	canonical := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 0,
		TxArray: []*types.TxInItem{{
			BlockHeight: 601,
			Tx:          tx.TxID.String(),
		}},
	}
	require.NoError(t, observer.processObservedTx(*canonical))
	canonicalKey := TxInKey(canonical)
	canonicalObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    601,
		FinaliseHeight: 601,
		XmrMinedHeight: 601,
	}
	canonicalGossipKey := observedTxKey(canonicalObserved, true, false)
	require.NoError(t, gossip.AttestObservedTx(
		context.Background(),
		&canonicalObserved,
		true,
		false,
	))

	// Model a lagging peer/state dump replaying the orphan-height observation
	// after initial cleanup. The ingress barrier must reject this stale view,
	// while admitting the same immutable transaction at its new canonical
	// height. Retry cleanup below remains a defense-in-depth backstop.
	orphanedGossipKey := observedTxKey(orphanedObserved, true, false)
	gossip.batcher.mu.Lock()
	batchedBeforeStale := len(gossip.batcher.observedTxBatch)
	gossip.batcher.mu.Unlock()
	require.NoError(t, gossip.AttestObservedTx(
		context.Background(),
		&orphanedObserved,
		true,
		false,
	))
	gossip.mu.Lock()
	_, canonicalAdmitted := gossip.observedTxs[canonicalGossipKey]
	_, orphanedAdmitted := gossip.observedTxs[orphanedGossipKey]
	gossip.mu.Unlock()
	gossip.batcher.mu.Lock()
	batchedAfterStale := len(gossip.batcher.observedTxBatch)
	gossip.batcher.mu.Unlock()
	require.True(t, canonicalAdmitted)
	require.False(t, orphanedAdmitted)
	require.Equal(t, batchedBeforeStale, batchedAfterStale)

	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(1), record.AttestationAttempts)
	nextAttempt := time.Unix(0, record.LastAttemptAtUnixNano).
		Add(errataAttestationRetryDelay(record.AttestationAttempts))
	observer.retryPendingErrataAt(context.Background(), nextAttempt)

	select {
	case attempted := <-attestationAttempts:
		require.Equal(t, tx.Chain, attempted.Chain)
		require.Equal(t, tx.TxID, attempted.Id)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback-path errata retry")
	}

	require.Contains(t, observer.onDeck, canonicalKey)
	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].TxArray, 1)
	require.Equal(t, int64(601), stored[0].TxArray[0].BlockHeight)
	gossip.mu.Lock()
	_, canonicalStillObserved := gossip.observedTxs[canonicalGossipKey]
	_, orphanedStillObserved := gossip.observedTxs[orphanedGossipKey]
	gossip.mu.Unlock()
	require.True(t, canonicalStillObserved)
	require.False(t, orphanedStillObserved)

	close(stop)
	waitObserverLoop(t, done)
}

func TestXMRErrataCleanupClosesIngressCheckInsertRace(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-ingress-cleanup-race"}
	orphanedObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    610,
		FinaliseHeight: 610,
	}
	orphanedKey := observedTxKey(orphanedObserved, true, false)

	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	observer := &Observer{
		logger:            log.Logger,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		storage:           storage,
		attestationGossip: gossip,
	}

	beforeWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	var signalOnce sync.Once
	storage.beforeErrataWrite = func() error {
		signalOnce.Do(func() { close(beforeWrite) })
		<-releaseWrite
		return nil
	}

	cleanupResult := make(chan error, 1)
	go func() {
		cleanupResult <- observer.cleanupErrataBlock(types.ErrataBlock{
			Height: 610,
			Txs:    []types.ErrataTx{tx},
		})
	}()

	select {
	case <-beforeWrite:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for errata cleanup before-write barrier")
	}

	// Cleanup owns the writer barrier before beginning its durable batch, so a
	// concurrent observation cannot pass the tombstone check or be submitted to
	// Thornode from the stale view.
	attestResult := make(chan error, 1)
	go func() {
		attestResult <- gossip.AttestObservedTx(
			context.Background(),
			&orphanedObserved,
			true,
			false,
		)
	}()

	select {
	case err := <-attestResult:
		t.Fatalf("observation crossed in-progress errata cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	gossip.mu.Lock()
	_, insertedBeforeCommit := gossip.observedTxs[orphanedKey]
	gossip.mu.Unlock()
	require.False(t, insertedBeforeCommit)

	close(releaseWrite)
	select {
	case err := <-cleanupResult:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for errata cleanup")
	}
	select {
	case err := <-attestResult:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rejected stale observation")
	}

	gossip.mu.Lock()
	_, survivedCleanup := gossip.observedTxs[orphanedKey]
	gossip.mu.Unlock()
	require.False(t, survivedCleanup)
	gossip.batcher.mu.Lock()
	defer gossip.batcher.mu.Unlock()
	require.Empty(t, gossip.batcher.observedTxBatch)
}

func TestXMRErrataCleanupWaitsForInFlightQuorumSubmission(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-quorum-cleanup-order"}
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    612,
		FinaliseHeight: 612,
	}
	key := observedTxKey(observed, true, false)

	gossip, _, _, grpcClient, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	gossip.avMu.Lock()
	for id := range gossip.activeVals {
		if id != gossip.host.ID() {
			delete(gossip.activeVals, id)
		}
	}
	gossip.avMu.Unlock()
	observer := &Observer{
		logger:            log.Logger,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		storage:           storage,
		attestationGossip: gossip,
	}

	quorumEntered := make(chan struct{})
	releaseQuorum := make(chan struct{})
	grpcClient.sendQuorumTxFunc = func(
		context.Context,
		*common.QuorumTx,
		...grpc.CallOption,
	) (*ebifrost.SendQuorumTxResult, error) {
		close(quorumEntered)
		<-releaseQuorum
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	attestResult := make(chan error, 1)
	go func() {
		attestResult <- gossip.AttestObservedTx(context.Background(), &observed, true, false)
	}()
	select {
	case <-quorumEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight quorum submission")
	}

	beforeWrite := make(chan struct{})
	storage.beforeErrataWrite = func() error {
		close(beforeWrite)
		return nil
	}
	cleanupResult := make(chan error, 1)
	go func() {
		cleanupResult <- observer.cleanupErrataBlock(types.ErrataBlock{
			Height: 612,
			Txs:    []types.ErrataTx{tx},
		})
	}()

	select {
	case <-beforeWrite:
		t.Fatal("durable errata write crossed an in-flight quorum submission")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseQuorum)
	select {
	case err := <-attestResult:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quorum submission")
	}
	select {
	case <-beforeWrite:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ordered errata write")
	}
	select {
	case err := <-cleanupResult:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for errata cleanup")
	}

	tombstone, err := storage.getErrataTombstone(tx.Chain, tx.TxID.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	gossip.mu.Lock()
	_, survivedCleanup := gossip.observedTxs[key]
	gossip.mu.Unlock()
	require.False(t, survivedCleanup)
	gossip.batcher.mu.Lock()
	defer gossip.batcher.mu.Unlock()
	require.Empty(t, gossip.batcher.observedTxBatch)
}

func TestXMRErrataCleanupWaitsForDequeuedBatchTransmission(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-dequeued-batch-order"}
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    613,
		FinaliseHeight: 613,
	}
	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	gossip.batcher.setActiveValGetter(gossip.getActiveValidators)
	observer := &Observer{
		logger:            log.Logger,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		storage:           storage,
		attestationGossip: gossip,
	}

	sendEntered := make(chan struct{})
	releaseSend := make(chan struct{})
	var enteredOnce sync.Once
	gossip.batcher.broadcastToPeerFn = func(context.Context, peer.ID, []byte) {
		enteredOnce.Do(func() { close(sendEntered) })
		<-releaseSend
	}
	gossip.batcher.AddObservedTx(common.AttestTx{
		ObsTx: observed,
		Attestation: &common.Attestation{
			PubKey:    []byte("pubkey"),
			Signature: []byte("signature"),
		},
	})

	sendDone := make(chan struct{})
	go func() {
		gossip.batcher.sendBatches(context.Background(), true)
		close(sendDone)
	}()
	select {
	case <-sendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dequeued XMR batch transmission")
	}

	beforeWrite := make(chan struct{})
	storage.beforeErrataWrite = func() error {
		close(beforeWrite)
		return nil
	}
	cleanupResult := make(chan error, 1)
	go func() {
		cleanupResult <- observer.cleanupErrataBlock(types.ErrataBlock{
			Height: 613,
			Txs:    []types.ErrataTx{tx},
		})
	}()
	select {
	case <-beforeWrite:
		t.Fatal("durable errata write crossed an in-flight batch transmission")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseSend)
	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for XMR batch transmission")
	}
	select {
	case <-beforeWrite:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ordered errata write")
	}
	select {
	case err := <-cleanupResult:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for errata cleanup")
	}
}

func TestXMRObservationIngressFailsClosedBeforeStorageWiring(t *testing.T) {
	gossip, _, _, _, _, _ := setupTestGossip(t)
	// NewAttestationGossip sets this before its constructor returns. Exercise
	// the narrow production construction window before Observer wires storage.
	gossip.xmrErrataAdmissionRequired = true
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:    common.TxID("xmr-before-storage-wiring"),
			Chain: common.XMRChain,
		},
		BlockHeight:    610,
		FinaliseHeight: 610,
	}
	key := observedTxKey(observed, true, false)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &observed, true, false))

	gossip.mu.Lock()
	_, admitted := gossip.observedTxs[key]
	gossip.mu.Unlock()
	require.False(t, admitted)
	gossip.batcher.mu.Lock()
	defer gossip.batcher.mu.Unlock()
	require.Empty(t, gossip.batcher.observedTxBatch)
}

func TestXMRObservationIngressFailsClosedOnErrataTombstoneReadError(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	txID := common.TxID("xmr-corrupt-errata-tombstone")
	require.NoError(t, storage.db.Put(
		[]byte(errataTombstoneKey(common.XMRChain, txID.String())),
		[]byte(`{"invalid":`),
		nil,
	))

	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:    txID,
			Chain: common.XMRChain,
		},
		BlockHeight:    611,
		FinaliseHeight: 611,
	}
	key := observedTxKey(observed, true, false)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &observed, true, false))

	gossip.mu.Lock()
	_, admitted := gossip.observedTxs[key]
	gossip.mu.Unlock()
	require.False(t, admitted)
	gossip.batcher.mu.Lock()
	defer gossip.batcher.mu.Unlock()
	require.Empty(t, gossip.batcher.observedTxBatch)
}

func TestCallbackErrataInitialPersistenceFailureHasNoPartialCleanup(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-initial-fsync-failure"}
	orphaned := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 0,
		TxArray: []*types.TxInItem{{
			BlockHeight: 700,
			Tx:          tx.TxID.String(),
		}},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	orphanedKey := TxInKey(orphaned)

	gossip, _, _, _, _, _ := setupTestGossip(t)
	orphanedObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    700,
		FinaliseHeight: 700,
	}
	require.NoError(t, gossip.AttestObservedTx(
		context.Background(),
		&orphanedObserved,
		true,
		false,
	))
	orphanedGossipKey := observedTxKey(orphanedObserved, true, false)

	injectedErr := errors.New("injected initial errata fsync failure")
	storage.beforeErrataWrite = func() error {
		return injectedErr
	}
	type callbackResult struct {
		stored bool
		err    error
	}
	callback := make(chan callbackResult, 2)
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{orphanedKey: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	observer.globalErrataQueue <- types.ErrataBlock{
		Height: 700,
		Txs:    []types.ErrataTx{tx},
		OnStored: func() {
			callback <- callbackResult{stored: true}
		},
		OnRejected: func(err error) {
			callback <- callbackResult{err: err}
		},
	}
	select {
	case result := <-callback:
		require.False(t, result.stored)
		require.ErrorIs(t, result.err, injectedErr)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rejected initial errata persistence")
	}
	select {
	case duplicate := <-callback:
		t.Fatalf("errata delivery invoked more than one terminal callback: %+v", duplicate)
	default:
	}

	tombstone, err := storage.getErrataTombstone(tx.Chain, tx.TxID.String())
	require.NoError(t, err)
	require.Nil(t, tombstone)
	_, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.False(t, found)

	require.Contains(t, observer.onDeck, orphanedKey)
	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].TxArray, 1)
	require.Equal(t, tx.TxID.String(), stored[0].TxArray[0].Tx)
	gossip.mu.Lock()
	_, orphanedStillObserved := gossip.observedTxs[orphanedGossipKey]
	gossip.mu.Unlock()
	require.True(t, orphanedStillObserved)

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataWithoutAckPreservesLegacyOneShotPath(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.BTCChain, TxID: "btc-orphan"}
	orphaned := &types.TxIn{
		Chain:                common.BTCChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 500, Tx: tx.TxID.String()},
		},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	key := TxInKey(orphaned)
	gossip, _, _, _, _, _ := setupTestGossip(t)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    500,
		FinaliseHeight: 500,
	}, true, false))
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{key: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	// A nil acknowledgement channel is the production contract used by the
	// non-XMR chain clients. It must remain a one-shot event.
	observer.globalErrataQueue <- types.ErrataBlock{
		Height: 500,
		Txs:    []types.ErrataTx{tx},
	}
	require.Eventually(t, func() bool {
		gossip.mu.Lock()
		defer gossip.mu.Unlock()
		_, found := gossip.errataTxs[common.ErrataTx{Chain: tx.Chain, Id: tx.TxID}]
		return found
	}, 2*time.Second, 10*time.Millisecond)

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Empty(t, observer.onDeck)
	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	assert.Empty(t, stored)
	gossip.mu.Lock()
	assert.Empty(t, gossip.observedTxs, "legacy one-shot delivery must retain its TxID-wide cleanup")
	gossip.mu.Unlock()

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataReturnsAttestationFailureWithoutDroppingOutbox(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.setActiveValidators(nil)
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-attestation-failure"}
	persisted := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     457,
		DeliveryID: "xmr-reorg-457-xmr-attestation-failure",
		Txs:        []types.ErrataTx{tx},
		Persisted:  persisted,
	}
	require.ErrorContains(t, waitPersistenceAck(t, persisted), "skipping attest errata tx: not active")

	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.CleanupComplete)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	assert.False(t, record.Committed)

	// The immutable transaction is mined again at a new canonical height before
	// the durable errata vote reaches Thornode. Both Observer's deck and Gossip's
	// attestation state must survive every scanner/outbox retransmission.
	canonical := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 0,
		TxArray: []*types.TxInItem{{
			BlockHeight: 458,
			Tx:          tx.TxID.String(),
		}},
	}
	require.NoError(t, observer.processObservedTx(*canonical))
	canonicalObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    458,
		FinaliseHeight: 458,
		XmrMinedHeight: 458,
	}
	canonicalKey := observedTxKey(canonicalObserved, true, false)
	gossip.mu.Lock()
	gossip.observedTxs[canonicalKey] = gossip.observedTxsPool.NewAttestationState(
		&attestableObservedTx{ObservedTx: &canonicalObserved, inbound: true},
	)
	gossip.mu.Unlock()

	// A scanner redelivery forces another real attempt even though the
	// autonomous outbox is still inside its retry delay. It must not receive a
	// false-success acknowledgement for deferred work or repeat cleanup.
	retryAck := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     457,
		DeliveryID: "xmr-reorg-457-xmr-attestation-failure",
		Txs:        []types.ErrataTx{tx},
		Persisted:  retryAck,
	}
	require.ErrorContains(t, waitPersistenceAck(t, retryAck), "skipping attest errata tx: not active")
	record, found, err = storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(2), record.AttestationAttempts)
	require.Len(t, observer.onDeck, 1)
	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, int64(458), stored[0].TxArray[0].BlockHeight)
	gossip.mu.Lock()
	_, canonicalStillObserved := gossip.observedTxs[canonicalKey]
	gossip.mu.Unlock()
	assert.True(t, canonicalStillObserved)

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

	observer := &Observer{
		logger:  log.Logger,
		storage: storage,
		lock:    &sync.Mutex{},
	}
	gossip := &AttestationGossip{
		logger:      log.Logger,
		pubKey:      []byte("local-validator"),
		errataTxs:   make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
		observedTxs: make(map[txKey]*AttestationState[*attestableObservedTx]),
	}
	gossip.SetObserverHandleErrataTxCommitted(observer)
	quorumErrata := common.QuorumErrataTx{
		ErrataTx: &common.ErrataTx{Chain: tx.Chain, Id: tx.TxID},
		Attestations: []*common.Attestation{{
			PubKey: gossip.pubKey,
		}},
	}
	payload, err := quorumErrata.Marshal()
	require.NoError(t, err)
	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestErrataAttestationRetryIsBackedOffAndTerminal(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "unconfirmed-errata"}
	block := types.ErrataBlock{Height: 900, Txs: []types.ErrataTx{tx}}
	require.NoError(t, storage.AddErrataBlock(block))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))

	now := time.Unix(1_750_000_000, 0)
	_, recorded, err := storage.RecordErrataAttestationAttempt(tx, now.Add(-errataAttestationRetryHorizon))
	require.NoError(t, err)
	require.True(t, recorded)

	observer := &Observer{logger: log.Logger, storage: storage}
	require.NoError(t, observer.broadcastErrataBlock(context.Background(), block, now, false))
	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.AttestationRetryExhausted)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)

	// Terminal quarantine suppresses later sends even when no gossip client is
	// configured; without it this call would try to attest and fail.
	require.NoError(t, observer.broadcastErrataBlock(context.Background(), block, now.Add(time.Hour), false))
	require.ErrorContains(
		t,
		observer.broadcastErrataBlock(context.Background(), block, now.Add(time.Hour), true),
		"operator recovery required",
	)
	record, found, err = storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
}

func TestErrataAttestationRetryDelayIsExponentiallyBounded(t *testing.T) {
	assert.Zero(t, errataAttestationRetryDelay(0))
	assert.Equal(t, constants.ThorchainBlockTime, errataAttestationRetryDelay(1))
	assert.Equal(t, 2*constants.ThorchainBlockTime, errataAttestationRetryDelay(2))
	assert.Equal(t, errataAttestationRetryMaxDelay, errataAttestationRetryDelay(32))
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
