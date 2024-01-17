package signercache

import (
	"fmt"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
)

// StorageAccessor define the necessary methods to access the key value store
type StorageAccessor interface {
	SetSigned(hash string) error
	HasSigned(hash string) bool
	RemoveSigned(transactionHash string) error
	SetTransactionHashMap(txOutItemHash, transactionHash string) error
	SetLatestRecordedTx(vaultKey, transactionHash string) error
	GetLatestRecordedTx(vaultKey string) (string, error)
}

// CacheManager maintain a store of the transaction that signer already signed
type CacheManager struct {
	logger          zerolog.Logger
	storageAccessor StorageAccessor
}

// NewSignerCacheManager create a new instance of CacheManager
func NewSignerCacheManager(db *leveldb.DB) (*CacheManager, error) {
	if db == nil {
		return nil, fmt.Errorf("db parameter is nil")
	}
	cacheStore := NewCacheStore(db)
	return &CacheManager{
		logger:          log.With().Str("module", "SignerCacheManager").Logger(),
		storageAccessor: cacheStore,
	}, nil
}

// SetSigned mark a tx out item has been signed
func (cm *CacheManager) SetSigned(txOutItemHash, vaultKey, transactionHash string) error {
	// The CacheManager consistently uses THORChain-format TxIDs as when OnObservedTxIn observations are reported to THORNode,
	// but EVM/ETH chains specifically may also call SetSigned with a 0x-prefix hash when broadcasting,
	// so check for and prune if present.
	if len(transactionHash) > 2 && transactionHash[:2] == "0x" {
		transactionHash = transactionHash[2:]
	}

	if err := cm.storageAccessor.SetSigned(txOutItemHash); err != nil {
		cm.logger.Err(err).
			Str("txout_hash", txOutItemHash).
			Str("transaction_hash", transactionHash).
			Msg("fail to set signed cache")
		return fmt.Errorf("fail to set signed cache %w", err)
	}
	err := cm.storageAccessor.SetTransactionHashMap(txOutItemHash, transactionHash)
	if err != nil {
		return err
	}

	return cm.storageAccessor.SetLatestRecordedTx(vaultKey, transactionHash)
}

// HasSigned check whether the given tx out item has been signed before
func (cm *CacheManager) HasSigned(txOutItemHash string) bool {
	return cm.storageAccessor.HasSigned(txOutItemHash)
}

func (cm *CacheManager) GetLatestRecordedTx(vaultKey string) (string, error) {
	return cm.storageAccessor.GetLatestRecordedTx(vaultKey)
}

// RemoveSigned removes the corresponding TxOutItem from the signer cache. The provided
// transaction hash should be for the broadcast transaction - it is internally mapped to
// the cache key for the TxOutItem.
func (cm *CacheManager) RemoveSigned(transactionHash string) {
	// The CacheManager consistently uses THORChain-format TxIDs as when OnObservedTxIn observations are reported to THORNode,
	// but EVM/ETH chains specifically may also call RemoveSigned with a 0x-prefix hash upon failed-tx or unstuck-tx block scanning,
	// so check for and prune if present.
	if len(transactionHash) > 2 && transactionHash[:2] == "0x" {
		// TODO: Remove this unpruned RemoveSigned call after enough time has passed (for instance on hard fork)
		// that residual 0x-prefix hashes set by modified SetSigned are no longer a concern.
		if err := cm.storageAccessor.RemoveSigned(transactionHash); err != nil {
			cm.logger.Err(err).Msgf("fail to remove signed transaction hash: %s", transactionHash)
		}

		transactionHash = transactionHash[2:]
	}

	if err := cm.storageAccessor.RemoveSigned(transactionHash); err != nil {
		cm.logger.Err(err).Msgf("fail to remove signed transaction hash: %s", transactionHash)
	}
}
