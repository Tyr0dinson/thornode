package monero

import (
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Bumped from v1 to v2 after a bug where both frost and eddsa keys were
// persisted for the same vault address. v1 entries are ignored at load time
// and purged on next startup so only frost-key mappings are retained.
const (
	vaultAddressPrefix         = "xmr-vault-address-v2-"
	vaultAddressPrefixLegacyV1 = "xmr-vault-address-v1-"
)

type VaultAddressStore struct {
	db *leveldb.DB
}

func NewVaultAddressStore(db *leveldb.DB) *VaultAddressStore {
	return &VaultAddressStore{db: db}
}

func (s *VaultAddressStore) Set(vaultPubKey, address string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("vault address store is not initialized")
	}
	if vaultPubKey == "" || address == "" {
		return fmt.Errorf("vault pubkey and address cannot be empty")
	}
	return s.db.Put([]byte(vaultAddressPrefix+vaultPubKey), []byte(address), nil)
}

func (s *VaultAddressStore) Get(vaultPubKey string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("vault address store is not initialized")
	}
	if vaultPubKey == "" {
		return "", fmt.Errorf("vault pubkey cannot be empty")
	}
	value, err := s.db.Get([]byte(vaultAddressPrefix+vaultPubKey), nil)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// PurgeLegacyEntries removes any entries using the v1 prefix. The v1 store
// contained buggy double-registered mappings (both frost and eddsa keys for
// the same address) which caused the sidecar to return 500 errors during
// vault registration. Call this on startup before loading v2 entries.
func (s *VaultAddressStore) PurgeLegacyEntries() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("vault address store is not initialized")
	}
	iter := s.db.NewIterator(util.BytesPrefix([]byte(vaultAddressPrefixLegacyV1)), nil)
	defer iter.Release()

	batch := new(leveldb.Batch)
	for iter.Next() {
		batch.Delete(iter.Key())
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if batch.Len() == 0 {
		return nil
	}
	return s.db.Write(batch, nil)
}

func (s *VaultAddressStore) List() (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vault address store is not initialized")
	}
	result := make(map[string]string)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(vaultAddressPrefix)), nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if len(key) <= len(vaultAddressPrefix) {
			continue
		}
		vaultPubKey := key[len(vaultAddressPrefix):]
		result[vaultPubKey] = string(iter.Value())
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}
