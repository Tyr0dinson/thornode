package signercache

import (
	"errors"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/common"
)

const (
	signedCachePrefix = "signed-v6-"
	txMapPrefix       = "tx-map-v6-"
	checkpointPrefix  = "chpt-"
)

// CacheStore manage the key value store used to store what tx out items have been signed before
type CacheStore struct {
	logger zerolog.Logger
	db     *leveldb.DB
}

// NewCacheStore create a new instance of CacheStore
func NewCacheStore(db *leveldb.DB) *CacheStore {
	return &CacheStore{
		db:     db,
		logger: log.With().Str("module", "signer-cache").Logger(),
	}
}

// SetSigned update key value store to set the given height and hash as signed
func (s *CacheStore) SetSigned(hash string) error {
	key := s.getSignedKey(hash)
	s.logger.Debug().Msgf("key:%s set to signed", key)
	return s.db.Put([]byte(key), []byte{1}, nil)
}

func (s *CacheStore) getSignedKey(hash string) string {
	return fmt.Sprintf("%s%s", signedCachePrefix, hash)
}

func (s *CacheStore) getMapKey(txHash string) string {
	return fmt.Sprintf("%s%s", txMapPrefix, txHash)
}

func (s *CacheStore) getCheckpointKey(pubkey common.PubKey) string {
	return fmt.Sprintf("%s%s", checkpointPrefix, pubkey)
}

func (s *CacheStore) GetCheckpoint(pubkey common.PubKey) ([]byte, error) {
	key := s.getCheckpointKey(pubkey)
	exist, _ := s.db.Has([]byte(key), nil)
	if !exist {
		return nil, nil
	}
	return s.db.Get([]byte(key), nil)
}

func (s *CacheStore) SetCheckpoint(pubkey common.PubKey, data []byte) error {
	key := s.getCheckpointKey(pubkey)
	return s.db.Put([]byte(key), data, nil)
}

func (s *CacheStore) RemoveCheckpoint(pubkey common.PubKey) error {
	key := s.getCheckpointKey(pubkey)
	return s.db.Delete([]byte(key), nil)
}

// HasSigned check whether the given height and hash has been signed before or not
func (s *CacheStore) HasSigned(hash string) bool {
	key := s.getSignedKey(hash)
	exist, _ := s.db.Has([]byte(key), nil)
	s.logger.Debug().Msgf("key:%s has signed: %t", key, exist)
	return exist
}

// RemoveSigned delete a hash from the signed cache
func (s *CacheStore) RemoveSigned(transactionHash string) error {
	mapKey := s.getMapKey(transactionHash)
	value, err := s.db.Get([]byte(mapKey), nil)
	if err != nil {
		// bifrost didn't sign this tx , so it is fine
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil
		}
		s.logger.Err(err).Msg("fail to check map key exist")
		return err
	}
	key := s.getSignedKey(string(value))
	if err := s.db.Delete([]byte(key), nil); err != nil {
		s.logger.Error().Err(err).Msgf("fail to remove %s from signed cache", string(value))
		return fmt.Errorf("fail to remove signed cache, err: %w", err)
	}
	return nil
}

// SetTransactionHashMap map a transaction hash to a tx out item hash
func (s *CacheStore) SetTransactionHashMap(txOutItemHash, transactionHash string) error {
	key := s.getMapKey(transactionHash)
	return s.db.Put([]byte(key), []byte(txOutItemHash), nil)
}

// Close underlying db
func (s *CacheStore) Close() error {
	return s.db.Close()
}
