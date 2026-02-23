package blockscanner

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	leveldbutil "github.com/syndtr/goleveldb/leveldb/util"
)

const (
	PrefixBlockId           = "blockid-"
	PrefixSpentUtxoById     = "spentutxobyid-"
	PrefixSpentUtxoByHeight = "spentutxobyheight-"
)

type SpentUtxosByHeight struct {
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

// NewLevelDBScannerStorage create a new instance of LevelDBScannerStorage
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
	fmt.Println(">>", hex.EncodeToString(iterator.Key()))
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

	return s.setSpentUtxosByHeight(ids, uint64(height))
}

func (s *LevelDBScannerStorageCardano) GetSpentUtxosByHeight(height uint64) ([][]byte, error) {
	key := s.getSpentUtxoByHeightKey(height)

	bz, err := s.db.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to get data from storage: %w", err)
	}

	var record SpentUtxosByHeight
	if err = json.Unmarshal(bz, &record); err != nil {
		return nil, fmt.Errorf("fail to unmarshal data: %w", err)
	}

	return record.Ids, nil
}

func (s *LevelDBScannerStorageCardano) HasSpentUtxo(id []byte) (bool, error) {
	key := s.getSpentUtxoByIdKey(id)
	return s.db.Has(key, nil)
}

func (s *LevelDBScannerStorageCardano) PruneSpentUtxos(height uint64) error {
	iterator := s.db.NewIterator(
		leveldbutil.BytesPrefix([]byte(PrefixSpentUtxoByHeight)), nil,
	)
	defer iterator.Release()
	candidates := []uint64{}
	for ; iterator.Next(); iterator.Valid() {
		bz := iterator.Value()
		if len(bz) == 0 {
			continue
		}
		var record SpentUtxosByHeight
		if err := json.Unmarshal(bz, &record); err != nil {
			return fmt.Errorf("failed to unmarshal data: %w", err)
		}
		if record.Height <= height {
			candidates = append(candidates, record.Height)
		}
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
		err = s.db.Delete([]byte(key), nil)
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
		key := iterator.Key()
		if key == nil {
			continue
		}

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

//  keys
// ----------------------------------------------------------------------------

func (s *LevelDBScannerStorageCardano) getBlockIdKey(height uint64) []byte {
	key := make([]byte, len(PrefixBlockId)+8)
	copy(key[:], PrefixBlockId)
	inverted := ^height
	binary.BigEndian.PutUint64(key[len(PrefixBlockId):], inverted)

	return key
}

func (s *LevelDBScannerStorageCardano) getSpentUtxoByHeightKey(height uint64) []byte {
	key := make([]byte, len(PrefixSpentUtxoByHeight)+8)
	copy(key[:], PrefixSpentUtxoByHeight)
	binary.BigEndian.PutUint64(key[len(PrefixSpentUtxoByHeight):], height)

	return key
}

func (s *LevelDBScannerStorageCardano) getSpentUtxoByIdKey(id []byte) []byte {
	return append([]byte(PrefixSpentUtxoByHeight), id...)
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

	bz, err := json.Marshal(SpentUtxosByHeight{
		Height: height,
		Ids:    append(spent, ids...),
	})
	if err != nil {
		return fmt.Errorf("fail to marshal data: %w", err)
	}

	return s.db.Put(key, bz, nil)
}

func (s *LevelDBScannerStorageCardano) setSpentUtxosById(id []byte) error {
	if len(id) == 0 {
		return nil
	}

	key := s.getSpentUtxoByIdKey(id)
	return s.db.Put(key, nil, nil)
}
