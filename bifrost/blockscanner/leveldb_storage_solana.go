package blockscanner

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/config"
)

// LevelDBScannerStorageSolana is a scanner storage backed by level db
type LevelDBScannerStorageSolana struct {
	db *leveldb.DB
}

// SolanaAccountScanStatus indicates the scan status of a Solana account
type SolanaAccountScanStatus struct {
	LastSignature string `json:"last_signature"`
	LastSlot      uint64 `json:"last_slot"`
}

func accountScanStatusKey(account string) string {
	return fmt.Sprintf("scanstat-%s", account)
}

func accountCursorSlotKey(account string) string {
	return fmt.Sprintf("scancursor-%s", account)
}

// NewLevelDBScannerStorageSolana create a new instance of LevelDBScannerStorageSolana
func NewLevelDBScannerStorageSolana(db *leveldb.DB) (*LevelDBScannerStorageSolana, error) {
	return &LevelDBScannerStorageSolana{db: db}, nil
}

// GetScanPos get current Scan Pos
func (ldbss *LevelDBScannerStorageSolana) GetScanStatus(account string) (string, uint64, error) {
	key := accountScanStatusKey(account)
	buf, err := ldbss.db.Get([]byte(key), nil)
	if err != nil {
		return "", 0, err
	}
	pos, _ := binary.Uvarint(buf)
	sig := string(buf[8:])
	return sig, pos, nil
}

// SetScanPos save current scan pos
func (ldbss *LevelDBScannerStorageSolana) SetScanStatus(account, lastSignature string, lastSlot uint64) error {
	buf := make([]byte, 8+len(lastSignature))
	_ = binary.PutUvarint(buf, lastSlot)
	copy(buf[8:], lastSignature)

	// a status saved without its cursor slot drops the slot of an earlier cursor, so
	// it reads as unknown (0) and is never paired with the new signature
	batch := new(leveldb.Batch)
	batch.Put([]byte(accountScanStatusKey(account)), buf)
	batch.Delete([]byte(accountCursorSlotKey(account)))
	return ldbss.db.Write(batch, nil)
}

// GetScanStatusWithCursor returns the account's scan status and the slot of its
// cursor signature: 0 when none was stored for that signature (a status saved
// before the cursor slot was kept, or rewritten since by a binary that does not
// keep it, such as after a rollback).
func (ldbss *LevelDBScannerStorageSolana) GetScanStatusWithCursor(account string) (string, uint64, uint64, error) {
	sig, slot, err := ldbss.GetScanStatus(account)
	if err != nil {
		return "", 0, 0, err
	}
	buf, err := ldbss.db.Get([]byte(accountCursorSlotKey(account)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return sig, slot, 0, nil
	}
	if err != nil {
		return "", 0, 0, err
	}
	cursorSlot, n := binary.Uvarint(buf)
	if n <= 0 || string(buf[n:]) != sig {
		return sig, slot, 0, nil // the slot of another signature
	}
	return sig, slot, cursorSlot, nil
}

// SetScanStatusWithCursor saves the account's scan status and the slot of its cursor
// signature in one batch. The slot has its own key, so the scan status record keeps
// its format, and is stored with the signature it belongs to, so it is never read
// for another cursor.
func (ldbss *LevelDBScannerStorageSolana) SetScanStatusWithCursor(account, lastSignature string, lastSlot, cursorSlot uint64) error {
	status := make([]byte, 8+len(lastSignature))
	_ = binary.PutUvarint(status, lastSlot)
	copy(status[8:], lastSignature)
	slot := make([]byte, binary.MaxVarintLen64, binary.MaxVarintLen64+len(lastSignature))
	n := binary.PutUvarint(slot, cursorSlot)
	slot = append(slot[:n], lastSignature...)
	batch := new(leveldb.Batch)
	batch.Put([]byte(accountScanStatusKey(account)), status)
	batch.Put([]byte(accountCursorSlotKey(account)), slot)
	return ldbss.db.Write(batch, nil)
}

// GetScanPos get current Scan Pos
func (ldbss *LevelDBScannerStorageSolana) GetScanPos() (uint64, error) {
	buf, err := ldbss.db.Get([]byte(ScanPosKey), nil)
	if err != nil {
		return 0, err
	}
	pos, _ := binary.Uvarint(buf)
	return pos, nil
}

// SetScanPos save current scan pos
func (ldbss *LevelDBScannerStorageSolana) SetScanPos(slot uint64) error {
	buf := make([]byte, 8)
	n := binary.PutUvarint(buf, slot)
	return ldbss.db.Put([]byte(ScanPosKey), buf[:n], nil)
}

func (ldbss *LevelDBScannerStorageSolana) Close() error {
	return ldbss.db.Close()
}

// ScannerStorage define the method need to be used by scanner
type ScannerStorageSolana interface {
	GetScanStatus(account string) (string, uint64, error)
	SetScanStatus(account, lastSignature string, lastSlot uint64) error
	GetScanStatusWithCursor(account string) (string, uint64, uint64, error)
	SetScanStatusWithCursor(account, lastSignature string, lastSlot, cursorSlot uint64) error
	GetScanPos() (uint64, error)
	SetScanPos(block uint64) error
	GetInternalDb() *leveldb.DB
	io.Closer
}

// BlockScannerStorage
type BlockScannerStorageSolana struct {
	*LevelDBScannerStorageSolana
	db *leveldb.DB
}

func NewBlockScannerStorageSolana(levelDbFolder string, opts config.LevelDBOptions) (*BlockScannerStorageSolana, error) {
	ldb, err := db.NewLevelDB(levelDbFolder, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create level db: %w", err)
	}

	levelDbStorage, err := NewLevelDBScannerStorageSolana(ldb)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	return &BlockScannerStorageSolana{
		LevelDBScannerStorageSolana: levelDbStorage,
		db:                          ldb,
	}, nil
}

func (s *BlockScannerStorageSolana) GetInternalDb() *leveldb.DB {
	return s.db
}
