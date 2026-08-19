package observer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func finalizedXMRObservation(txID common.TxID, minedHeight, finaliseHeight int64) common.ObservedTx {
	return common.ObservedTx{
		Tx: common.Tx{
			ID:    txID,
			Chain: common.XMRChain,
		},
		BlockHeight:    finaliseHeight,
		FinaliseHeight: finaliseHeight,
		XmrMinedHeight: minedHeight,
	}
}

func TestFinalizedXMRAdmissionUsesCanonicalMinedHeight(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	txID := common.TxID("finalized-xmr-canonical-height")
	require.NoError(t, storage.applyErrata(types.ErrataBlock{
		Height: 100,
		Txs: []types.ErrataTx{{
			Chain: common.XMRChain,
			TxID:  txID,
		}},
	}, nil))

	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)

	orphaned := finalizedXMRObservation(txID, 100, 106)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &orphaned, true, false))
	gossip.mu.Lock()
	_, admitted := gossip.observedTxs[observedTxKey(orphaned, true, false)]
	gossip.mu.Unlock()
	require.False(t, admitted, "finalization-height rewrite must not bypass the mined-height tombstone")

	remined := finalizedXMRObservation(txID, 101, 107)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &remined, true, false))
	gossip.mu.Lock()
	_, admitted = gossip.observedTxs[observedTxKey(remined, true, false)]
	gossip.mu.Unlock()
	require.True(t, admitted, "the same transaction at a different canonical mined height must remain admissible")

	missingLocation := finalizedXMRObservation(txID, 0, 108)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &missingLocation, true, false))
	gossip.mu.Lock()
	_, admitted = gossip.observedTxs[observedTxKey(missingLocation, true, false)]
	gossip.mu.Unlock()
	require.False(t, admitted, "an XMR observation without a mined location must fail closed once a tombstone exists")
}

func TestPermanentXMRTombstoneRejectsEveryMinedHeight(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	errata := common.ErrataTx{Chain: common.XMRChain, Id: "permanent-xmr-canonical-height"}
	require.NoError(t, storage.applyCommittedErrata(errata, nil))
	require.NoError(t, storage.recordErrataVoterResult(errata, true, nil))

	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.SetObserverStorage(storage)
	observation := finalizedXMRObservation(errata.Id, 999, 1005)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &observation, true, false))

	gossip.mu.Lock()
	_, admitted := gossip.observedTxs[observedTxKey(observation, true, false)]
	gossip.mu.Unlock()
	require.False(t, admitted)
}

func TestOrdinaryAndHistoricalXMRSpentRefsUseCanonicalMinedHeight(t *testing.T) {
	routes := []struct {
		name  string
		apply func(*AttestationGossip, common.ObservedTx, func(bool))
	}{
		{
			name: "ordinary gossip",
			apply: func(gossip *AttestationGossip, tx common.ObservedTx, onOutcome func(bool)) {
				gossip.applySpentRefsAsync(tx, onOutcome)
			},
		},
		{
			name: "historical certificate",
			apply: func(gossip *AttestationGossip, tx common.ObservedTx, onOutcome func(bool)) {
				gossip.applySpentRefsWithWireMemoAsync(tx, tx.Tx.Memo, onOutcome)
			},
		},
	}
	heights := []struct {
		name        string
		minedHeight int64
		shouldApply bool
	}{
		{name: "orphaned location", minedHeight: 100, shouldApply: false},
		{name: "different-height remine", minedHeight: 101, shouldApply: true},
	}

	for _, route := range routes {
		route := route
		for _, height := range heights {
			height := height
			t.Run(route.name+"/"+height.name, func(t *testing.T) {
				storage, tempDir := setupTestDB(t)
				defer cleanupTestDB(t, storage, tempDir)

				txID := common.TxID("spent-refs-canonical-height-" + route.name + "-" + height.name)
				require.NoError(t, storage.applyErrata(types.ErrataBlock{
					Height: 100,
					Txs: []types.ErrataTx{{
						Chain: common.XMRChain,
						TxID:  txID,
					}},
				}, nil))

				gossip, _, _, _, _, _ := setupTestGossip(t)
				gossip.SetObserverStorage(storage)
				applied := make(chan common.ObservedTx, 1)
				gossip.SetObserverApplySpentOutputRefs(func(tx common.ObservedTx, onOutcome func(bool)) {
					applied <- tx
					onOutcome(true)
				})

				outcome := make(chan bool, 1)
				route.apply(gossip, finalizedXMRObservation(txID, height.minedHeight, 106), func(ok bool) {
					outcome <- ok
				})
				select {
				case got := <-outcome:
					require.Equal(t, height.shouldApply, got)
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for the guarded spent-ref outcome")
				}
				if height.shouldApply {
					select {
					case got := <-applied:
						require.Equal(t, height.minedHeight, got.XmrMinedHeight)
					case <-time.After(time.Second):
						t.Fatal("timed out waiting for the accepted spent-ref mutation")
					}
				} else {
					select {
					case <-applied:
						t.Fatal("tombstoned spent refs reached the observer mutation callback")
					default:
					}
				}
			})
		}
	}
}

func TestXMRErrataCleanupUsesCanonicalMinedHeight(t *testing.T) {
	gossip, _, _, _, _, _ := setupTestGossip(t)
	txID := common.TxID("cleanup-xmr-canonical-height")
	orphaned := finalizedXMRObservation(txID, 100, 106)
	remined := finalizedXMRObservation(txID, 101, 107)

	require.NoError(t, gossip.AttestObservedTx(context.Background(), &orphaned, true, false))
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &remined, true, false))
	gossip.removeObservedTxsForErrata(common.ErrataTx{Chain: common.XMRChain, Id: txID}, 100)

	gossip.mu.Lock()
	_, orphanedPresent := gossip.observedTxs[observedTxKey(orphaned, true, false)]
	_, reminedPresent := gossip.observedTxs[observedTxKey(remined, true, false)]
	gossip.mu.Unlock()
	require.False(t, orphanedPresent)
	require.True(t, reminedPresent)

	gossip.batcher.mu.Lock()
	require.Len(t, gossip.batcher.observedTxBatch, 1)
	require.Equal(t, int64(101), gossip.batcher.observedTxBatch[0].ObsTx.XmrMinedHeight)
	gossip.batcher.mu.Unlock()
}

func TestXMRFutureObservationFailsClosedAfterTombstone(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	txID := common.TxID("future-xmr-canonical-height")
	require.NoError(t, storage.applyErrata(types.ErrataBlock{
		Height: 100,
		Txs: []types.ErrataTx{{
			Chain: common.XMRChain,
			TxID:  txID,
		}},
	}, nil))

	observer := &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  make(map[txInKey]*types.TxIn),
		storage: storage,
	}
	future := types.TxIn{
		Chain:                  common.XMRChain,
		Filtered:               true,
		MemPool:                true,
		AllowFutureObservation: true,
		TxArray: []*types.TxInItem{{
			BlockHeight: 90,
			Tx:          txID.String(),
		}},
	}
	require.NoError(t, observer.processObservedTx(future))
	require.Empty(t, observer.onDeck, "pre-mining location cannot bypass an existing tombstone")

	remined := types.TxIn{
		Chain:    common.XMRChain,
		Filtered: true,
		TxArray: []*types.TxInItem{{
			BlockHeight: 101,
			Tx:          txID.String(),
		}},
	}
	require.NoError(t, observer.processObservedTx(remined))
	require.Contains(t, observer.onDeck, TxInKey(&remined))
}

func TestGetThorchainTxInsPreservesXMRMinedHeight(t *testing.T) {
	vault := randomXMRObservedPubKey(t)
	vaultAddress, err := vault.GetAddress(common.XMRChain)
	require.NoError(t, err)
	destination := common.XMRSeededDummyAddress('8', []byte("canonical-mined-height-destination"))
	txIn := &types.TxIn{
		Chain:                common.XMRChain,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{{
			BlockHeight:         100,
			Tx:                  observerTestHash("canonical-mined-height-conversion"),
			Sender:              vaultAddress.String(),
			To:                  destination,
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
			Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			Memo:                "MIGRATE:100",
			ObservedVaultPubKey: vault,
		}},
	}
	observer := &Observer{
		logger:              log.Logger,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}

	observed, invalid, err := observer.getThorchainTxIns(txIn, true, 106)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, observed, 1)
	require.Equal(t, int64(106), observed[0].BlockHeight)
	require.Equal(t, int64(100), observed[0].XmrMinedHeight)

	txIn.MemPool = true
	txIn.AllowFutureObservation = true
	txIn.ConfirmationRequired = 0
	observed, invalid, err = observer.getThorchainTxIns(txIn, false, 100)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, observed, 1)
	require.Zero(t, observed[0].XmrMinedHeight, "pre-mining observations must not claim a canonical location")
}
