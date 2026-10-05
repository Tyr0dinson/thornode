package observer

import (
	"errors"
	"sync"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/config"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestFilterObservationsPreservesBounced walks a scanner-produced Bounced
// outbound through the normal Filtered == false observer path (which clones the
// item) and asserts the signed ObservedTx still carries the flag. This is the
// propagation TxInItem.Copy() once silently dropped.
func TestFilterObservationsPreservesBounced(t *testing.T) {
	t.Parallel()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(nil, &metrics.Metrics{})
	require.NoError(t, err)
	pubkeyMgr.AddPubKey(vault, false, common.SigningAlgoSecp256k1)
	signedCache, err := lru.New(10)
	require.NoError(t, err)
	obs := &Observer{
		pubkeyMgr:           pubkeyMgr,
		signedTxOutCache:    signedCache,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}

	item := &types.TxInItem{
		BlockHeight: 100,
		Tx:          stypes.GetRandomTxHash().String(),
		Memo:        "OUT:" + stypes.GetRandomTxHash().String(),
		Sender:      vaultAddr.String(),
		To:          stypes.GetRandomETHAddress().String(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(37500))},
		Bounced:     true,
	}

	filtered := obs.filterObservations(common.ETHChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 1)
	require.True(t, filtered[0].Bounced)

	obsTxs, invalid, err := obs.getThorchainTxIns(&types.TxIn{Chain: common.ETHChain, TxArray: filtered}, true, 100)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, obsTxs, 1)
	require.True(t, obsTxs[0].ObservedPubKey.Equals(vault))
	require.True(t, obsTxs[0].Bounced)
}

// bounceDeckTxIn builds a pre-filtered single-item deck payload for an EVM
// outbound, so processObservedTx goes straight to the dedupe path.
func bounceDeckTxIn(tx string, vault common.PubKey, vaultAddr common.Address, bounced bool) types.TxIn {
	return types.TxIn{
		Chain:    common.ETHChain,
		Filtered: true,
		TxArray: []*types.TxInItem{{
			BlockHeight:         100,
			Tx:                  tx,
			Memo:                "OUT:" + stypes.GetRandomTxHash().String(),
			Sender:              vaultAddr.String(),
			To:                  stypes.GetRandomETHAddress().String(),
			Coins:               common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000))),
			Gas:                 common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(37500))},
			ObservedVaultPubKey: vault,
			Bounced:             bounced,
		}},
	}
}

func newBounceDeckObserver(t *testing.T, dir string) *Observer {
	t.Helper()
	storage, err := NewObserverStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	return &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  make(map[txInKey]*types.TxIn),
		storage: storage,
	}
}

// TestDeckBounceCorrectionReplacesStaleVerdict covers the correction path
// zlyzol described: the same tx/height/gas re-scanned with a different Bounced
// verdict must leave exactly one current deck entry carrying the new verdict,
// must clear a stale CommittedUnFinalised so the correction is actually re-sent,
// and must survive a restart.
func TestDeckBounceCorrectionReplacesStaleVerdict(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	txID := stypes.GetRandomTxHash().String()

	stale := bounceDeckTxIn(txID, vault, vaultAddr, false)
	key := TxInKey(&stale)
	require.NoError(t, obs.processObservedTx(stale))

	// The stale verdict was already sent and acked in the unfinalised state.
	obs.onDeck[key].TxArray[0].CommittedUnFinalised = true

	corrected := bounceDeckTxIn(txID, vault, vaultAddr, true)
	corrected.TxArray[0].Memo = stale.TxArray[0].Memo
	corrected.TxArray[0].To = stale.TxArray[0].To
	require.NoError(t, obs.processObservedTx(corrected))

	deck := obs.onDeck[key]
	require.Len(t, deck.TxArray, 1, "correction must replace, not append")
	require.True(t, deck.TxArray[0].Bounced)
	require.False(t, deck.TxArray[0].CommittedUnFinalised,
		"stale ack must not suppress sending the correction")

	// Restart: the correction must come back from storage, not the stale verdict.
	require.NoError(t, obs.storage.Close())
	restarted := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, restarted.storage.Close()) }()
	require.NoError(t, restarted.restoreDeck())
	restored, ok := restarted.onDeck[key]
	require.True(t, ok)
	require.Len(t, restored.TxArray, 1)
	require.True(t, restored.TxArray[0].Bounced)
}

// TestDeckBounceCorrectionRetriesAfterStorageFailure asserts a failed persist
// leaves the deck matching storage (stale verdict, stale committed flag) and
// the correction still applicable on the next arrival.
func TestDeckBounceCorrectionRetriesAfterStorageFailure(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, obs.storage.Close()) }()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	txID := stypes.GetRandomTxHash().String()

	stale := bounceDeckTxIn(txID, vault, vaultAddr, false)
	key := TxInKey(&stale)
	require.NoError(t, obs.processObservedTx(stale))
	obs.onDeck[key].TxArray[0].CommittedUnFinalised = true

	corrected := bounceDeckTxIn(txID, vault, vaultAddr, true)
	corrected.TxArray[0].Memo = stale.TxArray[0].Memo
	corrected.TxArray[0].To = stale.TxArray[0].To

	obs.storage.beforeSyncTxWrite = func() error { return errors.New("disk full") }
	require.Error(t, obs.processObservedTx(corrected))

	rolledBack := obs.onDeck[key]
	require.Len(t, rolledBack.TxArray, 1)
	require.False(t, rolledBack.TxArray[0].Bounced, "failed persist must roll the verdict back")
	require.True(t, rolledBack.TxArray[0].CommittedUnFinalised, "and the committed flag with it")

	// Retry once storage recovers.
	obs.storage.beforeSyncTxWrite = nil
	retry := bounceDeckTxIn(txID, vault, vaultAddr, true)
	retry.TxArray[0].Memo = stale.TxArray[0].Memo
	retry.TxArray[0].To = stale.TxArray[0].To
	require.NoError(t, obs.processObservedTx(retry))
	require.Len(t, obs.onDeck[key].TxArray, 1)
	require.True(t, obs.onDeck[key].TxArray[0].Bounced)
	require.False(t, obs.onDeck[key].TxArray[0].CommittedUnFinalised)
}

// TestDeckBounceCorrectionSurvivesStaleAck is the reviewer's ack scenario at the
// deck level: this node sent the stale verdict, corrected the deck, and only then
// does the committed-quorum ack for the stale payload arrive. It must not clear
// the corrected entry. The ack for the corrected payload still does.
func TestDeckBounceCorrectionSurvivesStaleAck(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, obs.storage.Close()) }()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	txID := stypes.GetRandomTxHash().String()

	stale := bounceDeckTxIn(txID, vault, vaultAddr, false)
	key := TxInKey(&stale)
	require.NoError(t, obs.processObservedTx(stale))

	corrected := bounceDeckTxIn(txID, vault, vaultAddr, true)
	corrected.TxArray[0].Memo = stale.TxArray[0].Memo
	corrected.TxArray[0].To = stale.TxArray[0].To
	require.NoError(t, obs.processObservedTx(corrected))
	require.True(t, obs.onDeck[key].TxArray[0].Bounced)

	item := stale.TxArray[0]
	id, err := common.NewTxID(item.Tx)
	require.NoError(t, err)
	from, err := common.NewAddress(item.Sender)
	require.NoError(t, err)
	to, err := common.NewAddress(item.To)
	require.NoError(t, err)
	acked := common.NewObservedTx(
		common.NewTx(id, from, to, item.Coins, item.Gas, item.Memo), 100, vault, 100,
	)

	// the late ack for the payload carrying the stale verdict
	obs.handleObservedTxCommitted(acked, false)
	require.Len(t, obs.onDeck[key].TxArray, 1, "stale ack must not clear the correction")
	require.True(t, obs.onDeck[key].TxArray[0].Bounced)

	// the ack for the corrected payload does clear it
	acked.Bounced = true
	obs.handleObservedTxCommitted(acked, false)
	require.NotContains(t, obs.onDeck, key)
}

// TestDeckCorrectionReplacesGasAndBounceTogether: a reorg can re-include the same
// transaction at the same height with both a different effective gas price and a
// different bounce outcome. Matching the deck entry on full equality (gas
// included) would treat that as a new observation and queue both receipts for the
// same tx, leaving this node signing the superseded one alongside its replacement.
func TestDeckCorrectionReplacesGasAndBounceTogether(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, obs.storage.Close()) }()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	txID := stypes.GetRandomTxHash().String()

	stale := bounceDeckTxIn(txID, vault, vaultAddr, false)
	key := TxInKey(&stale)
	require.NoError(t, obs.processObservedTx(stale))
	obs.onDeck[key].TxArray[0].CommittedUnFinalised = true

	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(42000))}
	corrected := bounceDeckTxIn(txID, vault, vaultAddr, true)
	corrected.TxArray[0].Memo = stale.TxArray[0].Memo
	corrected.TxArray[0].To = stale.TxArray[0].To
	corrected.TxArray[0].Gas = newGas
	require.NoError(t, obs.processObservedTx(corrected))

	deck := obs.onDeck[key]
	require.Len(t, deck.TxArray, 1, "the re-priced receipt must replace, not append")
	require.True(t, deck.TxArray[0].Bounced)
	require.True(t, deck.TxArray[0].Gas.Equals(newGas))
	require.False(t, deck.TxArray[0].CommittedUnFinalised,
		"the acked payload carried the stale receipt, so the correction must go out again")

	// A gas-only re-price is a correction too, not a second observation.
	newerGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(51000))}
	reGassed := bounceDeckTxIn(txID, vault, vaultAddr, true)
	reGassed.TxArray[0].Memo = stale.TxArray[0].Memo
	reGassed.TxArray[0].To = stale.TxArray[0].To
	reGassed.TxArray[0].Gas = newerGas
	require.NoError(t, obs.processObservedTx(reGassed))

	deck = obs.onDeck[key]
	require.Len(t, deck.TxArray, 1)
	require.True(t, deck.TxArray[0].Gas.Equals(newerGas))
}

// TestDeckBounceCorrectionAcrossRescanBuckets: a reorg rescan merges every
// re-read height into one TxIn keyed by its first item, so the corrected copy
// of an outbound at height 101 can arrive in a batch keyed at height 100. It
// must still replace the stale verdict in the 101 bucket rather than land beside
// it, while a new item in the same batch goes to the batch's own bucket.
func TestDeckBounceCorrectionAcrossRescanBuckets(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, obs.storage.Close()) }()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)

	stale := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	stale.TxArray[0].BlockHeight = 101
	staleKey := TxInKey(&stale)
	require.NoError(t, obs.processObservedTx(stale))
	obs.onDeck[staleKey].TxArray[0].CommittedUnFinalised = true

	rescan := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	corrected := *stale.TxArray[0]
	corrected.Bounced = true
	corrected.CommittedUnFinalised = false
	rescan.TxArray = append(rescan.TxArray, &corrected)
	rescanKey := TxInKey(&rescan)
	require.NotEqual(t, staleKey, rescanKey)
	require.NoError(t, obs.processObservedTx(rescan))

	require.Len(t, obs.onDeck, 2)
	staleDeck := obs.onDeck[staleKey]
	require.Len(t, staleDeck.TxArray, 1, "correction must replace, not append")
	require.True(t, staleDeck.TxArray[0].Bounced)
	require.False(t, staleDeck.TxArray[0].CommittedUnFinalised)
	rescanDeck := obs.onDeck[rescanKey]
	require.Len(t, rescanDeck.TxArray, 1, "only the new item belongs to the rescan bucket")
	require.Equal(t, rescan.TxArray[0].Tx, rescanDeck.TxArray[0].Tx)

	// The signer's instant observation lives in a future-observation bucket and
	// is never treated as a correction of the scanner's entry, or vice versa.
	instant := bounceDeckTxIn(stale.TxArray[0].Tx, vault, vaultAddr, false)
	*instant.TxArray[0] = *stale.TxArray[0]
	instant.TxArray[0].Bounced = false
	instant.AllowFutureObservation = true
	require.NoError(t, obs.processObservedTx(instant))
	require.Len(t, obs.onDeck, 3)
	require.True(t, obs.onDeck[staleKey].TxArray[0].Bounced)
}

// TestDeckRescanRemainderKeyedByItsOwnFirstItem: when a rescan batch's first
// item is routed to the bucket that already holds it, the rest of the batch
// must be keyed by its own first item - the key storage derives on persist - so
// the in-memory and stored buckets agree, and a remainder whose key is already
// on deck merges into that bucket instead of overwriting its stored row.
func TestDeckRescanRemainderKeyedByItsOwnFirstItem(t *testing.T) {
	dir := t.TempDir()
	obs := newBounceDeckObserver(t, dir)

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)

	// A@100 was first observed alone, with 2 confirmations.
	first := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	first.ConfirmationRequired = 2
	firstKey := TxInKey(&first)
	require.NoError(t, obs.processObservedTx(first))

	// C@110 already sits in the bucket the remainder below will key to.
	existing := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	existing.TxArray[0].BlockHeight = 110
	existing.ConfirmationRequired = 5
	existingKey := TxInKey(&existing)
	require.NoError(t, obs.processObservedTx(existing))

	// The rescan re-reads A and reads B@110 and D@110; the batch's total value
	// needs 5 confirmations, so its own key (100+5) matches neither bucket.
	rescan := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	a := *first.TxArray[0]
	b := *rescan.TxArray[0]
	b.BlockHeight = 110
	d := b
	d.Tx = stypes.GetRandomTxHash().String()
	rescan.TxArray = []*types.TxInItem{&a, &b, &d}
	rescan.ConfirmationRequired = 5
	require.NoError(t, obs.processObservedTx(rescan))

	require.Len(t, obs.onDeck, 2, "no bucket under the batch's stale key")
	require.Len(t, obs.onDeck[firstKey].TxArray, 1)
	require.Len(t, obs.onDeck[existingKey].TxArray, 3)

	// Restart: every bucket comes back under the key it had in memory.
	require.NoError(t, obs.storage.Close())
	restarted := newBounceDeckObserver(t, dir)
	require.NoError(t, restarted.restoreDeck())
	require.Len(t, restarted.onDeck, 2)
	require.Len(t, restarted.onDeck[firstKey].TxArray, 1)
	require.Len(t, restarted.onDeck[existingKey].TxArray, 3)

	// Without an existing bucket, the remainder gets a new one keyed by its own
	// first item, and storage agrees after a restart.
	lone := bounceDeckTxIn(stypes.GetRandomTxHash().String(), vault, vaultAddr, false)
	e := *lone.TxArray[0]
	e.BlockHeight = 120
	a2 := *first.TxArray[0]
	lone.TxArray = []*types.TxInItem{&a2, &e}
	lone.ConfirmationRequired = 5
	require.NoError(t, restarted.processObservedTx(lone))
	loneKey := txInKey{chain: common.ETHChain, height: 125}
	require.Len(t, restarted.onDeck, 3)
	require.Len(t, restarted.onDeck[loneKey].TxArray, 1)
	require.NoError(t, restarted.storage.Close())
	again := newBounceDeckObserver(t, dir)
	defer func() { require.NoError(t, again.storage.Close()) }()
	require.NoError(t, again.restoreDeck())
	require.Len(t, again.onDeck, 3)
	require.Len(t, again.onDeck[loneKey].TxArray, 1)
}
