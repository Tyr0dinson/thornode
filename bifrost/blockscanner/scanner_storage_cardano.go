package blockscanner

import (
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/config"
)

type BlockScannerStorageCardano struct {
	*LevelDBScannerStorageCardano
	db *leveldb.DB
}

func NewBlockScannerStorageCardano(
	levelDbFolder string,
	opts config.LevelDBOptions,
) (*BlockScannerStorageCardano, error) {
	ldb, err := db.NewLevelDB(levelDbFolder, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create level db: %w", err)
	}

	levelDbStorage, err := NewLevelDBScannerStorageCardano(ldb)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	return &BlockScannerStorageCardano{
		LevelDBScannerStorageCardano: levelDbStorage,
		db:                           ldb,
	}, nil
}

func (s *BlockScannerStorageCardano) GetInternalDb() *leveldb.DB {
	return s.db
}
