package observer

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"gitlab.com/thorchain/thornode/common"

	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
)

// ObserverStorage save the ondeck tx in item to key value store , in case bifrost restart
type ObserverStorage struct {
	db *leveldb.DB
}

const (
	OnDeckTxKey           = "ondeck-tx"
	prefixVaultBalanceKey = "vault-balance"
)

// NewObserverStorage create a new instance of LevelDBScannerStorage
func NewObserverStorage(path string) (*ObserverStorage, error) {
	// no directory given, use in memory store
	if path == "" {
		storage := storage.NewMemStorage()
		db, err := leveldb.Open(storage, nil)
		if err != nil {
			return nil, fmt.Errorf("fail to in memory open level db: %w", err)
		}
		return &ObserverStorage{db: db}, nil
	}
	levelDbFolder := filepath.Join(path, "observer")
	db, err := leveldb.OpenFile(levelDbFolder, nil)
	if err != nil {
		return nil, fmt.Errorf("fail to open level db %s: %w", levelDbFolder, err)
	}
	return &ObserverStorage{db: db}, nil
}

// GetOnDeckTxs retrieve the ondeck tx from key value store
func (s *ObserverStorage) GetOnDeckTxs() ([]types.TxIn, error) {
	buf, err := s.db.Get([]byte(OnDeckTxKey), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to get ondeck tx from key value store: %w", err)
	}
	var result []types.TxIn
	if err := json.Unmarshal(buf, &result); err != nil {
		return nil, fmt.Errorf("fail to unmarshal ondeck tx: %w", err)
	}
	return result, nil
}

// SetOnDeckTxs save the ondeck tx to key value store
func (s *ObserverStorage) SetOnDeckTxs(ondeck []types.TxIn) error {
	buf, err := json.Marshal(ondeck)
	if err != nil {
		return fmt.Errorf("fail to marshal ondeck tx to json: %w", err)
	}
	return s.db.Put([]byte(OnDeckTxKey), buf, nil)
}

// SetVaultBalance save the given vault balance to key value store
func (s *ObserverStorage) SetVaultBalance(bal VaultBalance) error {
	key := s.getVaultBalanceKey(bal.Chain, bal.PubKey)
	buf, err := json.Marshal(bal)
	if err != nil {
		return fmt.Errorf("fail to marshal vault balance,err: %w", err)
	}
	return s.db.Put([]byte(key), buf, nil)
}

// GetVaultBalance get vault balance from local key value store
func (s *ObserverStorage) GetVaultBalance(chain common.Chain, pubKey common.PubKey) (VaultBalance, error) {
	addr, err := pubKey.GetAddress(chain)
	if err != nil {
		return VaultBalance{}, fmt.Errorf("fail to get address,err: %w", err)
	}
	bal := VaultBalance{
		Chain:   chain,
		PubKey:  pubKey,
		Address: addr,
		Coins:   common.Coins{},
	}
	key := s.getVaultBalanceKey(chain, pubKey)
	buf, err := s.db.Get([]byte(key), nil)
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
func (s *ObserverStorage) getVaultBalanceKey(chain common.Chain, pubKey common.PubKey) string {
	return fmt.Sprintf("%s-%s-%s", prefixVaultBalanceKey, chain, pubKey)
}
func (s *ObserverStorage) Close() error {
	return s.db.Close()
}
