package utxo

import (
	"fmt"
	"slices"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

type BitcoinTemporalStorageTestSuite struct{}

var _ = Suite(
	&BitcoinTemporalStorageTestSuite{},
)

func (s *BitcoinTemporalStorageTestSuite) TestNewTemporalStorage(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	dbTemporalStorage, err := NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)
	c.Assert(dbTemporalStorage, NotNil)
	c.Assert(db.Close(), IsNil)
}

func (s *BitcoinTemporalStorageTestSuite) TestPendingVaultTxTracking(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	store, err := NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)

	tx1 := PendingVaultTx{
		TxID:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Vins:            []PendingVaultTxOutpoint{{TxID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Vout: 1}},
		FirstSeenHeight: 100,
	}
	tx2 := PendingVaultTx{
		TxID:            "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Vins:            []PendingVaultTxOutpoint{{TxID: tx1.TxID, Vout: 0}},
		FirstSeenHeight: 101,
	}

	c.Assert(store.TrackPendingVaultTx(tx1), IsNil)
	c.Assert(store.TrackPendingVaultTx(tx2), IsNil)

	// idempotent, preserves first record
	c.Assert(store.TrackPendingVaultTx(PendingVaultTx{TxID: tx1.TxID, FirstSeenHeight: 999}), IsNil)

	tracked, err := store.HasPendingVaultTx(tx1.TxID)
	c.Assert(err, IsNil)
	c.Check(tracked, Equals, true)
	tracked, err = store.HasPendingVaultTx("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	c.Assert(err, IsNil)
	c.Check(tracked, Equals, false)

	pending, err := store.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 2)
	byID := map[string]PendingVaultTx{}
	for _, tx := range pending {
		byID[tx.TxID] = tx
	}
	c.Check(byID[tx1.TxID].FirstSeenHeight, Equals, int64(100))
	c.Check(byID[tx1.TxID].Vins, DeepEquals, tx1.Vins)
	c.Check(byID[tx2.TxID].Vins, DeepEquals, tx2.Vins)

	c.Assert(store.UntrackPendingVaultTx(tx1.TxID), IsNil)
	pending, err = store.GetPendingVaultTxs()
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)
	c.Check(pending[0].TxID, Equals, tx2.TxID)
}

func (s *BitcoinTemporalStorageTestSuite) TestTemporalStorage(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	store, err := NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)

	blockMeta := NewBlockMeta("00000000000000d9cba4b81d1f8fb5cecd54e4ec3104763ba937aa7692a86dc5",
		1722479,
		"00000000000000ca7a4633264b9989355e9709f9e9da19506b0f636cc435dc8f")
	c.Assert(store.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	key := store.getBlockMetaKey(blockMeta.Height)
	c.Assert(key, Equals, fmt.Sprintf(PrefixBlockMeta+"%d", blockMeta.Height))

	var bm *BlockMeta
	bm, err = store.GetBlockMeta(blockMeta.Height)
	c.Assert(err, IsNil)
	c.Assert(bm, NotNil)

	nbm, err := store.GetBlockMeta(1024)
	c.Assert(err, IsNil)
	c.Assert(nbm, IsNil)
	hash := thorchain.GetRandomTxHash()
	for i := 0; i < 1024; i++ {
		bm = NewBlockMeta(thorchain.GetRandomTxHash().String(), int64(i), thorchain.GetRandomTxHash().String())
		if i == 0 {
			bm.AddSelfTransaction(hash.String())
		}
		c.Assert(store.SaveBlockMeta(bm.Height, bm), IsNil)
	}
	blockMetas, err := store.GetBlockMetas()
	c.Assert(err, IsNil)
	c.Assert(blockMetas, HasLen, 1025)
	c.Assert(store.PruneBlockMeta(1000, func(meta *BlockMeta) bool {
		return !meta.TransactionHashExists(hash.String())
	}), IsNil)
	allBlockMetas, err := store.GetBlockMetas()
	c.Assert(err, IsNil)
	c.Assert(allBlockMetas, HasLen, 26)

	fee, vSize, err := store.GetTransactionFee()
	c.Assert(err, NotNil)
	c.Assert(fee, Equals, 0.0)
	c.Assert(vSize, Equals, int32(0))
	// upsert transaction fee
	c.Assert(store.UpsertTransactionFee(1.0, 1), IsNil)
	fee, vSize, err = store.GetTransactionFee()
	c.Assert(err, IsNil)
	c.Assert(fee, Equals, 1.0)
	c.Assert(vSize, Equals, int32(1))
	c.Assert(db.Close(), IsNil)
}

func (s *BitcoinTemporalStorageTestSuite) TestSpentUtxoHandling(c *C) {
	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	c.Assert(err, IsNil)
	store, err := NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)

	id1 := "id1"
	id2 := "id2"
	id3 := "id3"

	found, err := store.HasSpentUtxo(id1)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	err = store.SetSpentUtxos([]string{id1}, 99)
	c.Assert(err, IsNil)

	err = store.SetSpentUtxos([]string{id2}, 100)
	c.Assert(err, IsNil)

	// set empty slice => should simply skip
	err = store.SetSpentUtxos([]string{}, 101)
	c.Assert(err, IsNil)

	// has been spent => true
	found, err = store.HasSpentUtxo(id1)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	// has been spent => true
	found, err = store.HasSpentUtxo(id2)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	// has not been spent => false
	found, err = store.HasSpentUtxo(id3)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// set multiple ids
	err = store.SetSpentUtxos([]string{id2, id3}, 100)
	c.Assert(err, IsNil)

	var ids []string

	// return id1
	ids, err = store.GetSpentUtxosByHeight(99)
	c.Assert(err, IsNil)
	c.Assert(ids, HasLen, 1)
	c.Assert(ids[0], Equals, id1)

	// return id1, id3 (de-duped) for height 100
	ids, err = store.GetSpentUtxosByHeight(100)
	c.Assert(err, IsNil)
	c.Assert(ids, HasLen, 2)
	slices.Sort(ids)
	c.Assert(ids[0], Equals, id2)
	c.Assert(ids[1], Equals, id3)

	// no ids set for height 101
	ids, err = store.GetSpentUtxosByHeight(101)
	c.Assert(err, IsNil)
	c.Assert(ids, HasLen, 0)

	// remove all ids up to and including height 99
	err = store.PruneSpentUtxos(99)
	c.Assert(err, IsNil)

	ids, err = store.GetSpentUtxosByHeight(99)
	c.Assert(err, IsNil)
	c.Assert(ids, HasLen, 0)

	// was on block 99, should be gone
	found, err = store.HasSpentUtxo(id1)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// still there
	found, err = store.HasSpentUtxo(id3)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
}
