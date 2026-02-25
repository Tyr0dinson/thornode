package blockscanner

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	leveldbutil "github.com/syndtr/goleveldb/leveldb/util"
)

const (
	PrefixBlockId             = "blockid-"
	PrefixSpentUtxoById       = "spentutxobyid-"
	PrefixSpentUtxoByHeight   = "spentutxobyheight-"
	PrefixObservedTxsByHeight = "observedtxsbyheight-"
)

type IdsByHeight struct {
	Height uint64
	Ids    [][]byte
}

type BlockId struct {
	Height uint64
	Slot   uint64
	Hash   []byte
}

// LevelDBScannerStorageCardano is a scanner storage backed by level db
type LevelDBScannerStorageCardano struct {
	LevelDBScannerStorage
}

// NewLevelDBScannerStorageCardano create a new instance of NewLevelDBScannerStorageCardano
func NewLevelDBScannerStorageCardano(
	db *leveldb.DB,
) (*LevelDBScannerStorageCardano, error) {
	return &LevelDBScannerStorageCardano{
		LevelDBScannerStorage{db: db},
	}, nil
}

// block id

func (s *LevelDBScannerStorageCardano) GetLastBlockId() (
	uint64, uint64, []byte, error,
) {
	iterator := s.db.NewIterator(
		leveldbutil.BytesPrefix([]byte(PrefixBlockId)), nil,
	)
	defer iterator.Release()
	if !iterator.First() {
		return 0, 0, nil, fmt.Errorf("no entry found")
	}
	key := iterator.Key()

	return s.getBlockId(key)
}

func (s *LevelDBScannerStorageCardano) GetBlockId(
	height uint64,
) (uint64, uint64, []byte, error) {
	return s.getBlockId(s.getBlockIdKey(height))
}

func (s *LevelDBScannerStorageCardano) SetBlockId(
	height, slot uint64,
	hash []byte,
) error {
	key := s.getBlockIdKey(height)

	bz, err := json.Marshal(BlockId{
		Height: height,
		Slot:   slot,
		Hash:   hash,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	return s.db.Put(key, bz, nil)
}

// spent utxos

func (s *LevelDBScannerStorageCardano) SetSpentUtxos(ids [][]byte, height int64) error {
	if height <= 0 {
		return fmt.Errorf("height must >= 0")
	}

	for _, id := range ids {
		err := s.setSpentUtxoById(id)
		if err != nil {
			return err
		}
	}

	return s.setSpentUtxosByHeight(ids, uint64(height))
}

func (s *LevelDBScannerStorageCardano) GetSpentUtxosByHeight(height uint64) ([][]byte, error) {
	return s.getIdsByHeightKey(s.getSpentUtxoByHeightKey(height))
}

func (s *LevelDBScannerStorageCardano) HasSpentUtxo(id []byte) (bool, error) {
	key := s.getSpentUtxoByIdKey(id)
	return s.db.Has(key, nil)
}

func (s *LevelDBScannerStorageCardano) PruneSpentUtxos(height uint64) error {
	candidates, err := s.getPruneCandidates(height, PrefixSpentUtxoByHeight)
	if err != nil {
		return err
	}

	for _, height = range candidates {
		ids, err := s.GetSpentUtxosByHeight(height)
		if err != nil {
			return fmt.Errorf("failed to get data from storage: %w", err)
		}
		for _, id := range ids {
			key := s.getSpentUtxoByIdKey(id)
			err = s.db.Delete(key, nil)
			if err != nil {
				return fmt.Errorf("failed to delete data from storage: %w", err)
			}
		}
		key := s.getSpentUtxoByHeightKey(height)
		err = s.db.Delete(key, nil)
		if err != nil {
			return fmt.Errorf("failed to delete data from storage: %w", err)
		}
	}

	return nil
}

func (s *LevelDBScannerStorageCardano) PruneBlockIds(height uint64) error {
	iterator := s.db.NewIterator(
		leveldbutil.BytesPrefix([]byte(PrefixBlockId)), nil,
	)
	defer iterator.Release()
	candidates := [][]byte{}
	for ; iterator.Next(); iterator.Valid() {
		if iterator.Key() == nil {
			continue
		}

		key := make([]byte, len(iterator.Key()))
		copy(key, iterator.Key())

		inverted := binary.BigEndian.Uint64(key[len(PrefixBlockId):])
		if ^inverted > height {
			continue
		}

		candidates = append(candidates, key)
	}

	for _, key := range candidates {
		err := s.db.Delete(key, nil)
		if err != nil {
			return fmt.Errorf("failed to delete data from storage: %w", err)
		}
	}

	return nil
}

// observed txs

func (s *LevelDBScannerStorageCardano) SetObservedTxs(height uint64, ids [][]byte) error {
	key := s.getObservedTxsByHeightKey(height)

	bz, err := json.Marshal(IdsByHeight{
		Height: height,
		Ids:    ids,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	return s.db.Put(key, bz, nil)
}

func (s *LevelDBScannerStorageCardano) GetObservedTxs(height uint64) ([][]byte, error) {
	return s.getIdsByHeightKey(s.getObservedTxsByHeightKey(height))
}

func (s *LevelDBScannerStorageCardano) DeleteObservedTxs(height uint64) error {
	key := s.getObservedTxsByHeightKey(height)
	return s.db.Delete(key, nil)
}

func (s *LevelDBScannerStorageCardano) PruneObservedTxs(height uint64) error {
	candidates, err := s.getPruneCandidates(height, PrefixObservedTxsByHeight)
	if err != nil {
		return err
	}

	for _, h := range candidates {
		err = s.DeleteObservedTxs(h)
		if err != nil {
			return fmt.Errorf("failed to delete observed txs at height %d: %w", h, err)
		}
	}

	return nil
}

// block id rollback helpers

func (s *LevelDBScannerStorageCardano) GetBlockIdsAboveSlot(slot uint64) ([]BlockId, error) {
	iterator := s.db.NewIterator(
		leveldbutil.BytesPrefix([]byte(PrefixBlockId)), nil,
	)
	defer iterator.Release()

	var result []BlockId
	for ; iterator.Next(); iterator.Valid() {
		bz := iterator.Value()
		if len(bz) == 0 {
			continue
		}
		var id BlockId
		if err := json.Unmarshal(bz, &id); err != nil {
			return nil, fmt.Errorf("failed to unmarshal block id: %w", err)
		}
		if id.Slot > slot {
			result = append(result, id)
		}
	}

	return result, nil
}

func (s *LevelDBScannerStorageCardano) DeleteBlockId(height uint64) error {
	key := s.getBlockIdKey(height)
	return s.db.Delete(key, nil)
}

//  keys
// ----------------------------------------------------------------------------

func (s *LevelDBScannerStorageCardano) getBlockIdKey(height uint64) []byte {
	key := make([]byte, len(PrefixBlockId)+8)
	copy(key[:], PrefixBlockId)
	inverted := ^height
	binary.BigEndian.PutUint64(key[len(PrefixBlockId):], inverted)
	return key
}

func (s *LevelDBScannerStorageCardano) getObservedTxsByHeightKey(height uint64) []byte {
	key := make([]byte, len(PrefixObservedTxsByHeight)+8)
	copy(key[:], PrefixObservedTxsByHeight)
	binary.BigEndian.PutUint64(key[len(PrefixObservedTxsByHeight):], height)
	return key
}

func (s *LevelDBScannerStorageCardano) getSpentUtxoByHeightKey(height uint64) []byte {
	key := make([]byte, len(PrefixSpentUtxoByHeight)+8)
	copy(key[:], PrefixSpentUtxoByHeight)
	binary.BigEndian.PutUint64(key[len(PrefixSpentUtxoByHeight):], height)
	return key
}

func (s *LevelDBScannerStorageCardano) getSpentUtxoByIdKey(id []byte) []byte {
	return append([]byte(PrefixSpentUtxoById), id...)
}

//  internal
// ----------------------------------------------------------------------------

func (s *LevelDBScannerStorageCardano) getBlockId(
	key []byte,
) (uint64, uint64, []byte, error) {
	bz, err := s.db.Get(key, nil)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to get data from storage: %w", err)
	}

	var id BlockId
	err = json.Unmarshal(bz, &id)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return id.Height, id.Slot, id.Hash, nil
}

func (s *LevelDBScannerStorageCardano) setSpentUtxosByHeight(ids [][]byte, height uint64) error {
	if ids == nil {
		return nil
	}

	key := s.getSpentUtxoByHeightKey(height)

	spent, err := s.GetSpentUtxosByHeight(height)
	if err != nil {
		return err
	}

	bz, err := json.Marshal(IdsByHeight{
		Height: height,
		Ids:    append(spent, ids...),
	})
	if err != nil {
		return fmt.Errorf("fail to marshal data: %w", err)
	}

	return s.db.Put(key, bz, nil)
}

func (s *LevelDBScannerStorageCardano) setSpentUtxoById(id []byte) error {
	if len(id) == 0 {
		return nil
	}

	key := s.getSpentUtxoByIdKey(id)
	return s.db.Put(key, nil, nil)
}

func (s *LevelDBScannerStorageCardano) getIdsByHeightKey(key []byte) ([][]byte, error) {
	bz, err := s.db.Get(key, nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to get data from storage: %w", err)
	}

	var record IdsByHeight
	if err = json.Unmarshal(bz, &record); err != nil {
		return nil, fmt.Errorf("fail to unmarshal data: %w", err)
	}

	return record.Ids, nil
}

func (s *LevelDBScannerStorageCardano) getPruneCandidates(height uint64, key string) ([]uint64, error) {
	iterator := s.db.NewIterator(
		leveldbutil.BytesPrefix([]byte(key)), nil,
	)
	defer iterator.Release()
	var candidates []uint64
	for ; iterator.Next(); iterator.Valid() {
		bz := iterator.Value()
		if len(bz) == 0 {
			continue
		}
		var record IdsByHeight
		if err := json.Unmarshal(bz, &record); err != nil {
			return nil, fmt.Errorf("failed to unmarshal data: %w", err)
		}
		if record.Height <= height {
			candidates = append(candidates, record.Height)
		}
	}

	return candidates, nil
}
