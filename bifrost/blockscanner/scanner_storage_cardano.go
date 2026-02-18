package blockscanner

import (
	"fmt"
	"io"

	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ScannerStorage define the method need to be used by scanner
type ScannerStorageCardano interface {
	GetScanStatus(account string) (string, uint64, error)
	SetScanStatus(account, lastSignature string, lastSlot uint64) error
	GetScanPos() (uint64, error)
	SetScanPos(block uint64) error
	GetInternalDb() *leveldb.DB
	io.Closer
}

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
