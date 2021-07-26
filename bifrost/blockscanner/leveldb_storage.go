package blockscanner

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
	"gitlab.com/thorchain/thornode/bifrost/types"
	"gitlab.com/thorchain/thornode/common"
)

// LevelDBScannerStorage is a scanner storage backed by level db
type LevelDBScannerStorage struct {
	db *leveldb.DB
}

const (
	ScanPosKey            = "blockscanpos"
	PrefixVaultBalanceKey = "vault-balance"
)

// BlockStatusItem indicate the status of a block
type BlockStatusItem struct {
	Block  Block           `json:"block"`
	Status BlockScanStatus `json:"status"`
}

// NewLevelDBScannerStorage create a new instance of LevelDBScannerStorage
func NewLevelDBScannerStorage(db *leveldb.DB) (*LevelDBScannerStorage, error) {
	return &LevelDBScannerStorage{db: db}, nil
}

// GetScanPos get current Scan Pos
func (ldbss *LevelDBScannerStorage) GetScanPos() (int64, error) {
	buf, err := ldbss.db.Get([]byte(ScanPosKey), nil)
	if err != nil {
		return 0, err
	}
	pos, _ := binary.Varint(buf)
	return pos, nil
}

// SetScanPos save current scan pos
func (ldbss *LevelDBScannerStorage) SetScanPos(block int64) error {
	buf := make([]byte, 8)
	n := binary.PutVarint(buf, block)
	return ldbss.db.Put([]byte(ScanPosKey), buf[:n], nil)
}

func (ldbss *LevelDBScannerStorage) SetBlockScanStatus(block Block, status BlockScanStatus) error {
	blockStatusItem := BlockStatusItem{
		Block:  block,
		Status: status,
	}
	buf, err := json.Marshal(blockStatusItem)
	if err != nil {
		return fmt.Errorf("fail to marshal BlockStatusItem to json: %w", err)
	}
	if err := ldbss.db.Put([]byte(getBlockStatusKey(block.Height)), buf, nil); err != nil {
		return fmt.Errorf("fail to set block scan status: %w", err)
	}
	return nil
}

// GetFailedBlocksForRetry
func (ldbss *LevelDBScannerStorage) GetBlocksForRetry(failedOnly bool) ([]Block, error) {
	iterator := ldbss.db.NewIterator(util.BytesPrefix([]byte("block-process-status-")), nil)
	defer iterator.Release()
	var results []Block
	for iterator.Next() {
		buf := iterator.Value()
		if len(buf) == 0 {
			continue
		}
		var blockStatusItem BlockStatusItem
		if err := json.Unmarshal(buf, &blockStatusItem); err != nil {
			return nil, fmt.Errorf("fail to unmarshal to block status item: %w", err)
		}
		if !failedOnly {
			results = append(results, blockStatusItem.Block)
			continue
		}
		if blockStatusItem.Status == Failed {
			results = append(results, blockStatusItem.Block)
		}
	}
	return results, nil
}

func getBlockStatusKey(block int64) string {
	return fmt.Sprintf("block-process-status-%d", block)
}

func (ldbss *LevelDBScannerStorage) RemoveBlockStatus(block int64) error {
	return ldbss.db.Delete([]byte(getBlockStatusKey(block)), nil)
}

func (ldbss *LevelDBScannerStorage) Close() error {
	return ldbss.db.Close()
}

// SetVaultBalance save the given vault balance to key value store
func (ldbss *LevelDBScannerStorage) SetVaultBalance(bal types.VaultBalance) error {
	key := ldbss.getVaultBalanceKey(bal.Chain, bal.Address)
	buf, err := json.Marshal(bal)
	if err != nil {
		return fmt.Errorf("fail to marshal vault balance,err: %w", err)
	}
	return ldbss.db.Put([]byte(key), buf, nil)
}

// GetVaultBalance get vault balance from local key value store
func (ldbss *LevelDBScannerStorage) GetVaultBalance(chain common.Chain, address common.Address) (types.VaultBalance, error) {
	bal := types.VaultBalance{
		Chain:   chain,
		Address: address,
		Coins:   common.Coins{},
	}
	key := ldbss.getVaultBalanceKey(chain, address)
	buf, err := ldbss.db.Get([]byte(key), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return bal, nil
		}
		return bal, fmt.Errorf("fail to get vault balance from key value store,err:%w", err)
	}
	if err := json.Unmarshal(buf, &bal); err != nil {
		return bal, fmt.Errorf("fail to unmarshal vault balance,err: %w", err)
	}
	return bal, nil
}
func (ldbss *LevelDBScannerStorage) getVaultBalanceKey(chain common.Chain, address common.Address) string {
	return fmt.Sprintf("%s-%s-%s", PrefixVaultBalanceKey, chain, address)
}
