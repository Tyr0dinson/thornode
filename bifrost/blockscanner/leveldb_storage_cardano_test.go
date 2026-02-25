package blockscanner

import (
	"bytes"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"
)

type LevelDBStorageCardanoSuite struct {
	db      *leveldb.DB
	storage *LevelDBScannerStorageCardano
}

var _ = Suite(&LevelDBStorageCardanoSuite{})

func (s *LevelDBStorageCardanoSuite) SetUpTest(c *C) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	c.Assert(err, IsNil)
	s.db = db
	s.storage, err = NewLevelDBScannerStorageCardano(db)
	c.Assert(err, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TearDownTest(c *C) {
	c.Assert(s.db.Close(), IsNil)
}

// ---------------------------------------------------------------------------
// Block IDs
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestSetAndGetBlockId(c *C) {
	hash := []byte("blockhash1234")
	err := s.storage.SetBlockId(100, 200, hash)
	c.Assert(err, IsNil)

	height, slot, h, err := s.storage.GetBlockId(100)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(100))
	c.Assert(slot, Equals, uint64(200))
	c.Assert(bytes.Equal(h, hash), Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestGetBlockIdNotFound(c *C) {
	_, _, _, err := s.storage.GetBlockId(999)
	c.Assert(err, NotNil)
}

func (s *LevelDBStorageCardanoSuite) TestGetLastBlockIdEmpty(c *C) {
	_, _, _, err := s.storage.GetLastBlockId()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "no entry found")
}

func (s *LevelDBStorageCardanoSuite) TestGetLastBlockIdReturnsHighest(c *C) {
	// The inverted key encoding means the highest height sorts first in LevelDB.
	c.Assert(s.storage.SetBlockId(10, 100, []byte("h10")), IsNil)
	c.Assert(s.storage.SetBlockId(50, 500, []byte("h50")), IsNil)
	c.Assert(s.storage.SetBlockId(30, 300, []byte("h30")), IsNil)

	height, slot, hash, err := s.storage.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(50))
	c.Assert(slot, Equals, uint64(500))
	c.Assert(bytes.Equal(hash, []byte("h50")), Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestDeleteBlockId(c *C) {
	c.Assert(s.storage.SetBlockId(10, 100, []byte("h10")), IsNil)

	err := s.storage.DeleteBlockId(10)
	c.Assert(err, IsNil)

	_, _, _, err = s.storage.GetBlockId(10)
	c.Assert(err, NotNil)
}

func (s *LevelDBStorageCardanoSuite) TestPruneBlockIds(c *C) {
	// Insert blocks at heights 10, 20, 30, 40
	for h := uint64(10); h <= 40; h += 10 {
		c.Assert(s.storage.SetBlockId(h, h*10, []byte{byte(h)}), IsNil)
	}

	// Prune everything at height <= 20
	err := s.storage.PruneBlockIds(20)
	c.Assert(err, IsNil)

	// Heights 10, 20 should be gone
	_, _, _, err = s.storage.GetBlockId(10)
	c.Assert(err, NotNil)
	_, _, _, err = s.storage.GetBlockId(20)
	c.Assert(err, NotNil)

	// Heights 30, 40 should remain
	h, _, _, err := s.storage.GetBlockId(30)
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(30))
	h, _, _, err = s.storage.GetBlockId(40)
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(40))
}

func (s *LevelDBStorageCardanoSuite) TestPruneBlockIdsNoop(c *C) {
	// Prune on empty storage should not error.
	err := s.storage.PruneBlockIds(100)
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// GetBlockIdsAboveSlot
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestGetBlockIdsAboveSlot(c *C) {
	c.Assert(s.storage.SetBlockId(1, 100, []byte("a")), IsNil)
	c.Assert(s.storage.SetBlockId(2, 200, []byte("b")), IsNil)
	c.Assert(s.storage.SetBlockId(3, 300, []byte("c")), IsNil)

	result, err := s.storage.GetBlockIdsAboveSlot(150)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 2)

	// Verify returned blocks have slot > 150
	for _, id := range result {
		c.Assert(id.Slot > 150, Equals, true)
	}
}

func (s *LevelDBStorageCardanoSuite) TestGetBlockIdsAboveSlotNone(c *C) {
	c.Assert(s.storage.SetBlockId(1, 100, []byte("a")), IsNil)

	result, err := s.storage.GetBlockIdsAboveSlot(200)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 0)
}

func (s *LevelDBStorageCardanoSuite) TestGetBlockIdsAboveSlotEmpty(c *C) {
	result, err := s.storage.GetBlockIdsAboveSlot(0)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)
}

// ---------------------------------------------------------------------------
// Spent UTXOs
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestSetAndHasSpentUtxo(c *C) {
	ids := [][]byte{[]byte("utxo1"), []byte("utxo2")}
	err := s.storage.SetSpentUtxos(ids, 10)
	c.Assert(err, IsNil)

	found, err := s.storage.HasSpentUtxo([]byte("utxo1"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	found, err = s.storage.HasSpentUtxo([]byte("utxo2"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	found, err = s.storage.HasSpentUtxo([]byte("utxo3"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

func (s *LevelDBStorageCardanoSuite) TestSetSpentUtxosInvalidHeight(c *C) {
	err := s.storage.SetSpentUtxos([][]byte{[]byte("utxo1")}, 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "height must >= 0")

	err = s.storage.SetSpentUtxos([][]byte{[]byte("utxo1")}, -1)
	c.Assert(err, NotNil)
}

func (s *LevelDBStorageCardanoSuite) TestGetSpentUtxosByHeight(c *C) {
	ids := [][]byte{[]byte("utxo1"), []byte("utxo2")}
	c.Assert(s.storage.SetSpentUtxos(ids, 10), IsNil)

	result, err := s.storage.GetSpentUtxosByHeight(10)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 2)
}

func (s *LevelDBStorageCardanoSuite) TestGetSpentUtxosByHeightNotFound(c *C) {
	result, err := s.storage.GetSpentUtxosByHeight(999)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TestSetSpentUtxosAppends(c *C) {
	// Setting spent UTXOs at the same height should append, not overwrite.
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("utxo1")}, 10), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("utxo2")}, 10), IsNil)

	result, err := s.storage.GetSpentUtxosByHeight(10)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 2)

	found, err := s.storage.HasSpentUtxo([]byte("utxo1"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	found, err = s.storage.HasSpentUtxo([]byte("utxo2"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestSetSpentUtxosNilIds(c *C) {
	// nil ids should be a no-op for the by-height index (setSpentUtxosByHeight
	// returns nil early), though individual setSpentUtxoById calls are skipped
	// because the loop body never executes.
	err := s.storage.SetSpentUtxos(nil, 5)
	c.Assert(err, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TestSetSpentUtxoEmptyId(c *C) {
	// An empty id is skipped by setSpentUtxoById.
	err := s.storage.SetSpentUtxos([][]byte{{}}, 5)
	c.Assert(err, IsNil)

	// The by-height record should still store the empty id.
	result, err := s.storage.GetSpentUtxosByHeight(5)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 1)
}

func (s *LevelDBStorageCardanoSuite) TestPruneSpentUtxos(c *C) {
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("a1"), []byte("a2")}, 10), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("b1")}, 20), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("c1")}, 30), IsNil)

	// Prune everything at height <= 20
	err := s.storage.PruneSpentUtxos(20)
	c.Assert(err, IsNil)

	// Heights 10 and 20 should be pruned
	result, err := s.storage.GetSpentUtxosByHeight(10)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)

	result, err = s.storage.GetSpentUtxosByHeight(20)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)

	// Individual UTXO lookups should also be gone
	found, err := s.storage.HasSpentUtxo([]byte("a1"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	found, err = s.storage.HasSpentUtxo([]byte("b1"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// Height 30 should remain
	result, err = s.storage.GetSpentUtxosByHeight(30)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 1)

	found, err = s.storage.HasSpentUtxo([]byte("c1"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestPruneSpentUtxosEmpty(c *C) {
	err := s.storage.PruneSpentUtxos(100)
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// Observed Txs
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestSetAndGetObservedTxs(c *C) {
	ids := [][]byte{[]byte("tx1"), []byte("tx2")}
	err := s.storage.SetObservedTxs(100, ids)
	c.Assert(err, IsNil)

	result, err := s.storage.GetObservedTxs(100)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 2)
	c.Assert(bytes.Equal(result[0], []byte("tx1")), Equals, true)
	c.Assert(bytes.Equal(result[1], []byte("tx2")), Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestGetObservedTxsNotFound(c *C) {
	result, err := s.storage.GetObservedTxs(999)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TestDeleteObservedTxs(c *C) {
	c.Assert(s.storage.SetObservedTxs(100, [][]byte{[]byte("tx1")}), IsNil)

	err := s.storage.DeleteObservedTxs(100)
	c.Assert(err, IsNil)

	result, err := s.storage.GetObservedTxs(100)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TestPruneObservedTxs(c *C) {
	c.Assert(s.storage.SetObservedTxs(10, [][]byte{[]byte("tx1")}), IsNil)
	c.Assert(s.storage.SetObservedTxs(20, [][]byte{[]byte("tx2")}), IsNil)
	c.Assert(s.storage.SetObservedTxs(30, [][]byte{[]byte("tx3")}), IsNil)

	// Prune everything at height <= 20
	err := s.storage.PruneObservedTxs(20)
	c.Assert(err, IsNil)

	result, err := s.storage.GetObservedTxs(10)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)

	result, err = s.storage.GetObservedTxs(20)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)

	result, err = s.storage.GetObservedTxs(30)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 1)
	c.Assert(bytes.Equal(result[0], []byte("tx3")), Equals, true)
}

func (s *LevelDBStorageCardanoSuite) TestPruneObservedTxsEmpty(c *C) {
	err := s.storage.PruneObservedTxs(100)
	c.Assert(err, IsNil)
}

func (s *LevelDBStorageCardanoSuite) TestSetObservedTxsOverwrites(c *C) {
	// Unlike spent UTXOs, SetObservedTxs overwrites at the same height.
	c.Assert(s.storage.SetObservedTxs(10, [][]byte{[]byte("tx1")}), IsNil)
	c.Assert(s.storage.SetObservedTxs(10, [][]byte{[]byte("tx2"), []byte("tx3")}), IsNil)

	result, err := s.storage.GetObservedTxs(10)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 2)
	c.Assert(bytes.Equal(result[0], []byte("tx2")), Equals, true)
}

// ---------------------------------------------------------------------------
// Key ordering (inverted block ID keys)
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestBlockIdKeyOrdering(c *C) {
	// Verify that the inverted key scheme causes GetLastBlockId to return the
	// highest height even when blocks are inserted in ascending order.
	for h := uint64(1); h <= 5; h++ {
		c.Assert(s.storage.SetBlockId(h, h*100, []byte{byte(h)}), IsNil)
	}

	height, slot, _, err := s.storage.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(5))
	c.Assert(slot, Equals, uint64(500))
}

// ---------------------------------------------------------------------------
// Integration: full lifecycle
// ---------------------------------------------------------------------------

func (s *LevelDBStorageCardanoSuite) TestFullLifecycle(c *C) {
	// Simulate a few blocks being processed with UTXOs and observed txs,
	// then prune older data.

	// Block 10
	c.Assert(s.storage.SetBlockId(10, 1000, []byte("hash10")), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("u10a"), []byte("u10b")}, 10), IsNil)
	c.Assert(s.storage.SetObservedTxs(10, [][]byte{[]byte("tx10")}), IsNil)

	// Block 20
	c.Assert(s.storage.SetBlockId(20, 2000, []byte("hash20")), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("u20a")}, 20), IsNil)
	c.Assert(s.storage.SetObservedTxs(20, [][]byte{[]byte("tx20")}), IsNil)

	// Block 30
	c.Assert(s.storage.SetBlockId(30, 3000, []byte("hash30")), IsNil)
	c.Assert(s.storage.SetSpentUtxos([][]byte{[]byte("u30a")}, 30), IsNil)
	c.Assert(s.storage.SetObservedTxs(30, [][]byte{[]byte("tx30")}), IsNil)

	// Verify last block is height 30
	height, _, _, err := s.storage.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(30))

	// Prune block IDs and spent UTXOs at height <= 15 (only block 10)
	c.Assert(s.storage.PruneBlockIds(15), IsNil)
	c.Assert(s.storage.PruneSpentUtxos(15), IsNil)
	c.Assert(s.storage.PruneObservedTxs(15), IsNil)

	// Block 10 data should be gone
	_, _, _, err = s.storage.GetBlockId(10)
	c.Assert(err, NotNil)
	found, err := s.storage.HasSpentUtxo([]byte("u10a"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	result, err := s.storage.GetObservedTxs(10)
	c.Assert(err, IsNil)
	c.Assert(result, IsNil)

	// Block 20 and 30 data should remain
	height, _, _, err = s.storage.GetBlockId(20)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(20))
	found, err = s.storage.HasSpentUtxo([]byte("u20a"))
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	result, err = s.storage.GetObservedTxs(20)
	c.Assert(err, IsNil)
	c.Assert(len(result), Equals, 1)

	// Last block should now be height 30
	height, _, _, err = s.storage.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(30))
}
