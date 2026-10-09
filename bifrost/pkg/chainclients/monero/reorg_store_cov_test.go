package monero

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func newReorgStoreCov(t *testing.T) (*ReorgStore, *leveldb.DB) {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewReorgStore(db), db
}

func blockWatchCov(seed string, height int64) scannerRelocationWatch {
	return scannerRelocationWatch{
		TxID:           testXMRHash(seed),
		OriginalHeight: height - 1,
		LocationHeight: height,
		BlockHash:      testXMRHash(seed + ":block"),
		PreviousHash:   testXMRHash(seed + ":parent"),
	}
}

func TestReorgStoreNilReceiverCov(t *testing.T) {
	t.Parallel()

	var s *ReorgStore
	h, err := s.LatestHeight()
	require.NoError(t, err)
	require.Equal(t, int64(-1), h)
	meta, err := s.Get(1)
	require.NoError(t, err)
	require.Nil(t, meta)
	require.NoError(t, s.Set(&reorgBlockMeta{}))
	require.NoError(t, s.SetBatch(nil))
	require.NoError(t, s.PutObservationReceipt(nil, nil))
	marker, err := s.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, marker)
	require.NoError(t, s.BeginCatchup(scannerCatchupMarker{}))
	require.NoError(t, s.CheckpointCatchup(scannerCatchupMarker{}, nil, nil))
	claims, err := s.ListObservationReceipts()
	require.NoError(t, err)
	require.Nil(t, claims)
	require.NoError(t, s.AckObservationReceipts(nil, nil))
	require.ErrorContains(t, s.WatchRelocation(scannerRelocationWatch{}), "not initialized")
	require.NoError(t, s.QuarantineRevertedTxs(1, nil))
	require.ErrorContains(t, s.QuarantineRevertedTxs(1, []stypes.ErrataTx{{}}), "not initialized")
	reverted, err := s.IsRevertedTx(testXMRHash("x"))
	require.NoError(t, err)
	require.False(t, reverted)
	watches, err := s.ListRelocationWatches(1)
	require.NoError(t, err)
	require.Nil(t, watches)
	watches, err = s.ListRelocationWatchesAfter("", 1)
	require.NoError(t, err)
	require.Nil(t, watches)
	require.NoError(t, s.DeleteRelocationWatch("x"))
	upgraded, err := s.UpgradeRelocationWatchVaultOrigin("x")
	require.NoError(t, err)
	require.False(t, upgraded)
	require.ErrorContains(t, s.BindRelocationWatch(scannerRelocationWatch{}, nil), "not initialized")
	require.NoError(t, s.Delete(1))
	heights, err := s.OutstandingWorkHeights()
	require.NoError(t, err)
	require.Nil(t, heights)
	pruned, err := s.PruneBefore(10)
	require.NoError(t, err)
	require.Zero(t, pruned)
	require.ErrorContains(t, s.PutAuthenticatedOutbound(&reorgBlockMeta{}, testXMRHash("x")), "durable storage")
	require.ErrorContains(t, s.PutAuthenticatedObservation(&reorgBlockMeta{}, nil), "durable storage")

	reverted, err = isRevertedTxQuarantined(nil, "x")
	require.NoError(t, err)
	require.False(t, reverted)
}

func TestReorgStoreValidationCov(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, validateReorgBlockMeta(nil), "is nil")
	require.ErrorContains(t, validateReorgBlockMeta(&reorgBlockMeta{Height: -1}), "height must be >= 0")
	require.ErrorContains(t, validateReorgBlockMeta(&reorgBlockMeta{Height: 1}), "hash is empty")
	require.ErrorContains(t, validateReorgBlockMeta(&reorgBlockMeta{
		Height: 1, BlockHash: "h", CustomerTransactions: []string{"ABC"},
	}), "non-normalized transaction")
	require.ErrorContains(t, validateReorgBlockMeta(&reorgBlockMeta{
		Height: 1, BlockHash: "h", VaultOriginTransactions: []string{"ABC"},
	}), "non-normalized vault transaction")
	require.ErrorContains(t, validateReorgBlockMeta(&reorgBlockMeta{
		Height: 1, BlockHash: "h", VaultOriginTransactions: []string{"abc"},
	}), "absent from block journal")

	// Relocation watch validity.
	w := blockWatchCov("valid", 10)
	require.True(t, w.valid())
	bad := w
	bad.TxID = "zz"
	require.False(t, bad.valid())
	pool := scannerRelocationWatch{TxID: w.TxID, OriginalHeight: 3, InPool: true}
	require.True(t, pool.valid())
	pool.HoldAbsent = true
	require.False(t, pool.valid(), "in-pool and hold-absent are exclusive")
	hold := scannerRelocationWatch{TxID: w.TxID, OriginalHeight: 3, HoldAbsent: true}
	require.False(t, hold.valid(), "hold-absent requires vault origin")
	hold.VaultOrigin = true
	require.True(t, hold.valid())
	hold.BlockHash = "x"
	require.False(t, hold.valid())

	// Receipt validity.
	require.False(t, scannerObservationReceipt{Height: -1}.valid())
	item := scannerReceiptItem("receipt-valid", 5)
	require.True(t, scannerObservationReceipt{Height: 5, BlockHash: "b", PreviousHash: "p", Items: []*stypes.TxInItem{item}}.valid())
	require.False(t, scannerObservationReceipt{Height: 6, BlockHash: "b", PreviousHash: "p", Items: []*stypes.TxInItem{item}}.valid())
	require.False(t, scannerObservationReceipt{Height: 5, BlockHash: "b", PreviousHash: "p", Items: []*stypes.TxInItem{nil}}.valid())

	// Receipt encoding.
	_, err := encodeScannerObservationReceipt(nil, nil)
	require.Error(t, err)
	value, err := encodeScannerObservationReceipt(&reorgBlockMeta{Height: 5, BlockHash: "b", PreviousHash: "p"}, nil)
	require.NoError(t, err)
	require.Nil(t, value)
	_, err = encodeScannerObservationReceipt(&reorgBlockMeta{Height: 5, BlockHash: "b"}, []*stypes.TxInItem{item})
	require.ErrorContains(t, err, "invalid xmr scanner observation receipt")

	// Catch-up marker.
	m := scannerCatchupMarker{StartHeight: 5, TargetHeight: 4, AnchorHeight: 4, AnchorHash: "h"}
	require.True(t, m.completed())
	require.False(t, scannerCatchupMarker{StartHeight: 0}.valid())
}

func TestReorgStoreCorruptEntriesCov(t *testing.T) {
	t.Parallel()

	store, db := newReorgStoreCov(t)

	// Corrupt meta.
	require.NoError(t, db.Put([]byte(reorgBlockMetaKey(7)), []byte("{"), nil))
	_, err := store.Get(7)
	require.ErrorContains(t, err, "fail to decode xmr reorg block meta")

	// Invalid journal key breaks LatestHeight and PruneBefore.
	require.NoError(t, db.Put([]byte(reorgBlockMetaPrefix+"abc"), []byte("{}"), nil))
	_, err = store.LatestHeight()
	require.ErrorContains(t, err, "invalid xmr reorg journal key")
	_, err = store.PruneBefore(100)
	require.ErrorContains(t, err, "invalid xmr reorg journal key")
	require.NoError(t, db.Delete([]byte(reorgBlockMetaPrefix+"abc"), nil))

	// Corrupt catch-up marker.
	require.NoError(t, db.Put([]byte(scannerCatchupMarkerDBKey), []byte("{"), nil))
	_, err = store.GetCatchupMarker()
	require.ErrorContains(t, err, "decode xmr scanner catch-up marker")
	_, err = store.OutstandingWorkHeights()
	require.Error(t, err)
	_, err = store.PruneBefore(100)
	require.Error(t, err)
	require.Error(t, store.Delete(1))
	require.NoError(t, db.Put([]byte(scannerCatchupMarkerDBKey), []byte(`{"start_height":0}`), nil))
	_, err = store.GetCatchupMarker()
	require.ErrorContains(t, err, "invalid xmr scanner catch-up marker")
	require.NoError(t, db.Delete([]byte(scannerCatchupMarkerDBKey), nil))

	// Corrupt relocation watch breaks every watch consumer.
	badKey := []byte(scannerRelocationWatchPrefix + testXMRHash("bad-watch"))
	require.NoError(t, db.Put(badKey, []byte("{"), nil))
	_, err = store.OutstandingWorkHeights()
	require.ErrorContains(t, err, "malformed")
	_, err = store.PruneBefore(100)
	require.ErrorContains(t, err, "malformed")
	require.ErrorContains(t, store.Delete(1), "malformed")
	_, err = store.ListRelocationWatches(10)
	require.ErrorContains(t, err, "invalid xmr scanner relocation watch")
	_, err = store.ListRelocationWatchesAfter("", 10)
	require.ErrorContains(t, err, "invalid xmr scanner relocation watch")
	require.ErrorContains(t, store.WatchRelocation(blockWatchCov("new-watch", 10)), "malformed")
	_, err = store.UpgradeRelocationWatchVaultOrigin(testXMRHash("bad-watch"))
	require.ErrorContains(t, err, "malformed")
	w := blockWatchCov("bad-watch", 10)
	require.ErrorContains(t, store.WatchRelocation(w), "malformed")
	meta := &reorgBlockMeta{Height: 10, BlockHash: "b", PreviousHash: "p"}
	require.ErrorContains(t, store.PutAuthenticatedOutbound(meta, w.TxID), "malformed")
	require.NoError(t, db.Delete(badKey, nil))

	// Corrupt receipt.
	require.NoError(t, db.Put([]byte(scannerObservationReceiptKey(3)), []byte("{"), nil))
	_, err = store.ListObservationReceipts()
	require.ErrorContains(t, err, "decode xmr scanner observation receipt")
	_, err = store.OutstandingWorkHeights()
	require.ErrorContains(t, err, "decode xmr scanner observation receipt")
	err = store.PutObservationReceipt(&reorgBlockMeta{Height: 3, BlockHash: "b", PreviousHash: "p"},
		[]*stypes.TxInItem{scannerReceiptItem("corrupt-receipt", 3)})
	require.ErrorContains(t, err, "decode existing xmr scanner observation receipt")
	// Decodable but invalid receipt.
	require.NoError(t, db.Put([]byte(scannerObservationReceiptKey(3)), []byte(`{"height":3}`), nil))
	_, err = store.ListObservationReceipts()
	require.ErrorContains(t, err, "invalid xmr scanner observation receipt")
	err = store.PutObservationReceipt(&reorgBlockMeta{Height: 3, BlockHash: "b", PreviousHash: "p"},
		[]*stypes.TxInItem{scannerReceiptItem("corrupt-receipt", 3)})
	require.ErrorContains(t, err, "is invalid")
}

func TestReorgStoreRelocationWatchesCov(t *testing.T) {
	t.Parallel()

	store, db := newReorgStoreCov(t)
	require.ErrorContains(t, store.WatchRelocation(scannerRelocationWatch{TxID: "zz"}), "invalid xmr scanner relocation watch")

	a := blockWatchCov("watch-a", 20)
	b := blockWatchCov("watch-b", 21)
	c := blockWatchCov("watch-c", 22)
	for _, w := range []scannerRelocationWatch{a, b, c} {
		require.NoError(t, store.WatchRelocation(w))
	}
	// Existing watch: vault origin is monotonic.
	vo := a
	vo.VaultOrigin = true
	require.NoError(t, store.WatchRelocation(vo))
	require.NoError(t, store.WatchRelocation(a))
	all, err := store.ListRelocationWatches(0)
	require.NoError(t, err)
	require.Len(t, all, 3)
	for _, w := range all {
		if w.TxID == a.TxID {
			require.True(t, w.VaultOrigin)
		}
	}
	limited, err := store.ListRelocationWatches(2)
	require.NoError(t, err)
	require.Len(t, limited, 2)

	// Circular pagination.
	_, err = store.ListRelocationWatchesAfter("", 0)
	require.NoError(t, err)
	first, err := store.ListRelocationWatchesAfter("", 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	wrap, err := store.ListRelocationWatchesAfter(first[1].TxID, 3)
	require.NoError(t, err)
	require.Len(t, wrap, 3)
	require.Equal(t, first[0].TxID, wrap[1].TxID, "page wraps to the beginning after the cursor")
	wrapShort, err := store.ListRelocationWatchesAfter(first[1].TxID, 2)
	require.NoError(t, err)
	require.Len(t, wrapShort, 2)

	// Upgrade provenance.
	ok, err := store.UpgradeRelocationWatchVaultOrigin(testXMRHash("missing"))
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.UpgradeRelocationWatchVaultOrigin(a.TxID)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.UpgradeRelocationWatchVaultOrigin(b.TxID)
	require.NoError(t, err)
	require.True(t, ok)
	value, err := db.Get([]byte(scannerRelocationWatchPrefix+b.TxID), nil)
	require.NoError(t, err)
	var stored scannerRelocationWatch
	require.NoError(t, json.Unmarshal(value, &stored))
	require.True(t, stored.VaultOrigin)

	// Outstanding heights include watch origins and locations, the marker
	// anchor, and receipt heights.
	require.NoError(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 51, TargetHeight: 60, AnchorHeight: 50, AnchorHash: "h50"}))
	require.NoError(t, store.PutObservationReceipt(&reorgBlockMeta{Height: 70, BlockHash: "b", PreviousHash: "p"},
		[]*stypes.TxInItem{scannerReceiptItem("outstanding", 70)}))
	heights, err := store.OutstandingWorkHeights()
	require.NoError(t, err)
	for _, h := range []int64{19, 20, 21, 22, 50, 70} {
		require.Contains(t, heights, h)
	}

	// Bind relocation watch.
	require.Error(t, store.BindRelocationWatch(c, nil))
	require.ErrorContains(t, store.BindRelocationWatch(c, &reorgBlockMeta{Height: 99, BlockHash: c.BlockHash}),
		"does not match canonical journal")
	cvo := c
	cvo.VaultOrigin = true
	meta := &reorgBlockMeta{Height: c.LocationHeight, BlockHash: c.BlockHash, PreviousHash: c.PreviousHash}
	require.NoError(t, store.BindRelocationWatch(cvo, meta))
	got, err := store.Get(c.LocationHeight)
	require.NoError(t, err)
	require.Equal(t, []string{c.TxID}, got.CustomerTransactions)
	require.Equal(t, []string{c.TxID}, got.VaultOriginTransactions)
	// Second bind fails as the watch was removed.
	require.ErrorIs(t, store.BindRelocationWatch(cvo, &reorgBlockMeta{Height: c.LocationHeight, BlockHash: c.BlockHash}), leveldb.ErrNotFound)

	require.NoError(t, store.DeleteRelocationWatch(b.TxID))
	remaining, err := store.ListRelocationWatches(10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestReorgStoreWatchQuotaCov(t *testing.T) {
	t.Parallel()

	store, _ := newReorgStoreCov(t)
	for i := 0; i < maxOrdinaryScannerRelocationWatches; i++ {
		w := scannerRelocationWatch{TxID: testXMRHash("quota-" + string(rune(i+'A')) + "-" + string(rune(i/50+'a'))), OriginalHeight: 1, InPool: true}
		require.NoError(t, store.WatchRelocation(w))
	}
	extra := scannerRelocationWatch{TxID: testXMRHash("quota-extra"), OriginalHeight: 1, InPool: true}
	require.ErrorIs(t, store.WatchRelocation(extra), errScannerRelocationWatchQuota)
	// Vault-origin watches bypass the quota.
	extra.VaultOrigin = true
	require.NoError(t, store.WatchRelocation(extra))
}

func TestReorgStoreAuthenticatedOutboundCov(t *testing.T) {
	t.Parallel()

	store, _ := newReorgStoreCov(t)
	txID := testXMRHash("auth-outbound")
	meta := &reorgBlockMeta{Height: 30, BlockHash: testXMRHash("b30"), PreviousHash: testXMRHash("p30")}

	// Conflicting canonical journal.
	require.NoError(t, store.Set(&reorgBlockMeta{Height: 30, BlockHash: testXMRHash("other"), PreviousHash: testXMRHash("p30")}))
	require.ErrorContains(t, store.PutAuthenticatedOutbound(meta, txID), "conflicts with canonical journal")
	require.NoError(t, store.Set(meta))

	// An existing watch is upgraded to vault origin.
	w := blockWatchCov("auth-outbound", 31)
	require.NoError(t, store.WatchRelocation(w))
	require.NoError(t, store.PutAuthenticatedOutbound(meta, w.TxID))
	watches, err := store.ListRelocationWatches(10)
	require.NoError(t, err)
	require.Len(t, watches, 1)
	require.True(t, watches[0].VaultOrigin)
	got, err := store.Get(30)
	require.NoError(t, err)
	require.Contains(t, got.CustomerTransactions, w.TxID)
	require.Contains(t, got.VaultOriginTransactions, w.TxID)

	// Reverted transactions are rejected.
	require.ErrorContains(t, store.QuarantineRevertedTxs(-1, []stypes.ErrataTx{{TxID: common.TxID(txID), Chain: common.XMRChain}}), "height is invalid")
	require.ErrorContains(t, store.QuarantineRevertedTxs(1, []stypes.ErrataTx{{TxID: common.TxID(txID), Chain: common.BTCChain}}), "invalid xmr reverted transaction")
	require.NoError(t, store.QuarantineRevertedTxs(1, []stypes.ErrataTx{{TxID: common.TxID(txID), Chain: common.XMRChain}}))
	require.ErrorContains(t, store.PutAuthenticatedOutbound(meta, txID), "permanently reverted")
	_, err = store.IsRevertedTx("zz")
	require.ErrorContains(t, err, "invalid xmr reverted transaction hash")
	require.ErrorContains(t, store.PutAuthenticatedOutbound(meta, "zz"), "invalid xmr reverted transaction hash")
}

func TestReorgStoreCatchupAndAckCov(t *testing.T) {
	t.Parallel()

	store, _ := newReorgStoreCov(t)
	require.ErrorContains(t, store.BeginCatchup(scannerCatchupMarker{}), "invalid initial")
	require.ErrorContains(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 5, TargetHeight: 4, AnchorHeight: 4, AnchorHash: "h"}), "invalid initial")

	marker := scannerCatchupMarker{StartHeight: 41, TargetHeight: 41, AnchorHeight: 40, AnchorHash: "h40"}
	require.NoError(t, store.BeginCatchup(marker))

	// Checkpoint validation failures.
	require.Error(t, store.CheckpointCatchup(marker, nil, nil))
	meta41 := &reorgBlockMeta{Height: 41, BlockHash: "h41", PreviousHash: "h40"}
	require.ErrorContains(t, store.CheckpointCatchup(marker, meta41, nil), "invalid xmr scanner catch-up checkpoint")
	next := scannerCatchupMarker{StartHeight: 42, TargetHeight: 41, AnchorHeight: 41, AnchorHash: "h41"}
	require.NoError(t, store.Set(&reorgBlockMeta{Height: 41, BlockHash: "h41", PreviousHash: "other"}))
	require.ErrorContains(t, store.CheckpointCatchup(next, meta41, nil), "parent mismatch")
	require.NoError(t, store.Set(meta41))
	item := scannerReceiptItem("catchup-ack", 41)
	require.NoError(t, store.CheckpointCatchup(next, meta41, []*stypes.TxInItem{item}))

	// Delete refuses protected heights (receipt at 41, marker anchor at 41).
	require.NoError(t, store.Delete(41))
	got, err := store.Get(41)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NoError(t, store.Delete(-1))

	claims, err := store.ListObservationReceipts()
	require.NoError(t, err)
	require.Len(t, claims, 1)

	// Ack rejects an incomplete or changed marker.
	require.ErrorContains(t, store.AckObservationReceipts(nil, &marker), "incomplete")
	changed := next
	changed.AnchorHash = "different"
	require.ErrorContains(t, store.AckObservationReceipts(nil, &changed), "changed before acknowledgement")
	// A modified receipt value fails the CAS.
	tampered := claims[0]
	tampered.value = append([]byte(nil), tampered.value...)
	tampered.value[len(tampered.value)-1] = ' '
	require.ErrorContains(t, store.AckObservationReceipts([]scannerObservationReceiptClaim{tampered}, nil), "changed before acknowledgement")

	require.NoError(t, store.AckObservationReceipts(claims, &next))
	// Missing claims are skipped.
	require.NoError(t, store.AckObservationReceipts(claims, nil))
	m, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, m)
	require.ErrorContains(t, store.ClearCompletedCatchupMarker(next), "changed before acknowledgement")

	// Now unprotected: delete and prune work.
	require.NoError(t, store.Delete(41))
	got, err = store.Get(41)
	require.NoError(t, err)
	require.Nil(t, got)

	for h := int64(1); h <= 5; h++ {
		require.NoError(t, store.Set(&reorgBlockMeta{Height: h, BlockHash: "b", PreviousHash: "p"}))
	}
	require.NoError(t, store.PutObservationReceipt(&reorgBlockMeta{Height: 2, BlockHash: "b", PreviousHash: "p"},
		[]*stypes.TxInItem{scannerReceiptItem("prune-protect", 2)}))
	require.NoError(t, store.WatchRelocation(scannerRelocationWatch{TxID: testXMRHash("prune-watch"), OriginalHeight: 3, InPool: true}))
	pruned, err := store.PruneBefore(5)
	require.NoError(t, err)
	require.Equal(t, 2, pruned, "heights 1 and 4 pruned; 2 (receipt) and 3 (watch) retained")
	latest, err := store.LatestHeight()
	require.NoError(t, err)
	require.Equal(t, int64(5), latest)
	pruned, err = store.PruneBefore(0)
	require.NoError(t, err)
	require.Zero(t, pruned)

	// Empty canonical replacement deletes an orphaned receipt.
	require.NoError(t, store.PutObservationReceipt(&reorgBlockMeta{Height: 2, BlockHash: "b2", PreviousHash: "p"}, nil))
	claims, err = store.ListObservationReceipts()
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestReorgStoreClosedDBCov(t *testing.T) {
	t.Parallel()

	store, db := newReorgStoreCov(t)
	require.NoError(t, db.Close())

	_, err := store.LatestHeight()
	require.Error(t, err)
	_, err = store.Get(1)
	require.ErrorContains(t, err, "fail to load xmr reorg block meta")
	require.Error(t, store.Set(&reorgBlockMeta{Height: 1, BlockHash: "b"}))
	require.Error(t, store.SetBatch([]*reorgBlockMeta{{Height: 1, BlockHash: "b"}}))
	require.ErrorContains(t, store.SetBatch([]*reorgBlockMeta{nil}), "is nil")
	_, err = store.GetCatchupMarker()
	require.ErrorContains(t, err, "load xmr scanner catch-up marker")
	require.Error(t, store.BeginCatchup(scannerCatchupMarker{StartHeight: 2, TargetHeight: 5, AnchorHeight: 1, AnchorHash: "h"}))
	_, err = store.ListObservationReceipts()
	require.Error(t, err)
	require.Error(t, store.AckObservationReceipts([]scannerObservationReceiptClaim{{key: []byte("k")}}, nil))
	require.Error(t, store.WatchRelocation(blockWatchCov("closed", 5)))
	require.Error(t, store.QuarantineRevertedTxs(1, []stypes.ErrataTx{{TxID: common.TxID(testXMRHash("c")), Chain: common.XMRChain}}))
	_, err = store.IsRevertedTx(testXMRHash("c"))
	require.ErrorContains(t, err, "load reverted xmr transaction quarantine")
	_, err = store.ListRelocationWatches(1)
	require.Error(t, err)
	_, err = store.ListRelocationWatchesAfter("", 1)
	require.Error(t, err)
	_, err = store.UpgradeRelocationWatchVaultOrigin(testXMRHash("c"))
	require.Error(t, err)
	require.Error(t, store.Delete(1))
	_, err = store.OutstandingWorkHeights()
	require.Error(t, err)
	_, err = store.PruneBefore(10)
	require.Error(t, err)
	require.Error(t, store.PutObservationReceipt(&reorgBlockMeta{Height: 1, BlockHash: "b", PreviousHash: "p"}, nil))
	require.Error(t, store.CheckpointCatchup(
		scannerCatchupMarker{StartHeight: 2, TargetHeight: 5, AnchorHeight: 1, AnchorHash: "b"},
		&reorgBlockMeta{Height: 1, BlockHash: "b", PreviousHash: "p"}, nil))
}
