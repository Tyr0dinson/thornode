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
	SetSignedWithExpiry(hash string, expiryUnixMs int64, transactionHash string) error
	HasSigned(hash string) bool
	HasSignedWithExpiry(hash string) (signed, expired bool)
	RemoveSigned(transactionHash string) error
	SetTransactionHashMap(txOutItemHash, transactionHash string) error
	GetTransactionHash(txOutItemHash string) (string, error)
	GetSignedTxHash(txOutItemHash string) (string, bool)
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

// SetSigned mark a tx out item has been signed with no expiry. The cache entry persists
// until RemoveSigned is called by a chain-specific observation or unstuck routine.
func (cm *CacheManager) SetSigned(txOutItemHash, vaultKey, txHash string) error {
	err := cm.storageAccessor.SetSigned(txOutItemHash)
	if err != nil {
		cm.logger.Err(err).
			Str("txout_hash", txOutItemHash).
			Str("transaction_hash", txHash).
			Msg("fail to set signed cache")
		return fmt.Errorf("fail to set signed cache %w", err)
	}
	err = cm.storageAccessor.SetTransactionHashMap(txOutItemHash, txHash)
	if err != nil {
		return err
	}

	return cm.storageAccessor.SetLatestRecordedTx(vaultKey, txHash)
}

// SetSignedWithExpiry marks a tx out item as signed with an absolute expiry and the
// broadcast transaction hash. HasSignedWithExpiry surfaces the expiry status, and
// GetSignedTxHash surfaces the tx hash so callers can verify on-chain inclusion
// before clearing the entry. The entry is never removed on read — only by explicit
// RemoveSigned after chain-side verification — because deleting on expiry alone
// can double-spend if thorchain observation consensus lags actual inclusion.
func (cm *CacheManager) SetSignedWithExpiry(txOutItemHash, vaultKey, txHash string, expiryUnixMs int64) error {
	err := cm.storageAccessor.SetSignedWithExpiry(txOutItemHash, expiryUnixMs, txHash)
	if err != nil {
		cm.logger.Err(err).
			Str("txout_hash", txOutItemHash).
			Str("transaction_hash", txHash).
			Msg("fail to set signed cache with expiry")
		return fmt.Errorf("fail to set signed cache with expiry: %w", err)
	}
	err = cm.storageAccessor.SetTransactionHashMap(txOutItemHash, txHash)
	if err != nil {
		return err
	}
	return cm.storageAccessor.SetLatestRecordedTx(vaultKey, txHash)
}

// HasSigned check whether the given tx out item has been signed before
func (cm *CacheManager) HasSigned(txOutItemHash string) bool {
	return cm.storageAccessor.HasSigned(txOutItemHash)
}

// HasSignedWithExpiry returns whether an entry exists for the given tx out item
// hash and whether its expiry timestamp has passed. Callers that invalidate based
// on expiry should also verify on chain before removing the entry; see the store
// doc for why.
func (cm *CacheManager) HasSignedWithExpiry(txOutItemHash string) (signed, expired bool) {
	return cm.storageAccessor.HasSignedWithExpiry(txOutItemHash)
}

// GetSignedTxHash returns the broadcast transaction hash recorded for the given tx out
// item hash, or ("", false) if none is recorded. Only entries written via
// SetSignedWithExpiry carry a tx hash.
func (cm *CacheManager) GetSignedTxHash(txOutItemHash string) (string, bool) {
	return cm.storageAccessor.GetSignedTxHash(txOutItemHash)
}

func (cm *CacheManager) GetLatestRecordedTx(vaultKey string) (string, error) {
	return cm.storageAccessor.GetLatestRecordedTx(vaultKey)
}

func (cm *CacheManager) GetTransactionHash(txOutItemHash string) (string, error) {
	return cm.storageAccessor.GetTransactionHash(txOutItemHash)
}

// RemoveSigned removes the corresponding TxOutItem from the signer cache. The provided
// transaction hash should be for the broadcast transaction - it is internally mapped to
// the cache key for the TxOutItem.
//
// A failure is logged here before it is returned, so a caller that cannot act on it may
// discard the error. Callers that must not proceed unless the entry is really gone - such
// as a client about to re-sign an expired outbound - have to check it.
func (cm *CacheManager) RemoveSigned(txHash string) error {
	err := cm.storageAccessor.RemoveSigned(txHash)
	if err != nil {
		cm.logger.Err(err).Msgf("fail to remove signed transaction hash: %s", txHash)
		return fmt.Errorf("fail to remove signed transaction hash %s: %w", txHash, err)
	}
	return nil
}
