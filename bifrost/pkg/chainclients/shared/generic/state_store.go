package generic

import (
	"fmt"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
	"gitlab.com/thorchain/thornode/v3/common"
)

// StateStore is a chain-isolated byte store for chain-specific client state.
// The concrete chain client remains responsible for record schemas and encoding.
type StateStore interface {
	Get(key string) (value []byte, found bool, err error)
	Put(key string, value []byte) error
	Delete(key string) error

	// Iterate visits matching logical keys in byte order.
	Iterate(prefix string, fn func(key string, value []byte) error) error
}

type stateStore struct {
	db     *leveldb.DB
	prefix string
}

// newStateStore creates a byte store isolated under the namespace of one chain.
func newStateStore(db *leveldb.DB, chain common.Chain) (StateStore, error) {
	if db == nil {
		return nil, fmt.Errorf("db parameter is nil")
	}
	if chain.IsEmpty() {
		return nil, fmt.Errorf("chain is empty")
	}
	return &stateStore{
		db:     db,
		prefix: "generic-state/" + chain.String() + "/",
	}, nil
}

// key validates a logical key and adds the store's chain namespace.
func (s *stateStore) key(key string) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("state store key is empty")
	}
	return []byte(s.prefix + key), nil
}

// Get retrieves a value and reports whether the logical key exists.
func (s *stateStore) Get(key string) ([]byte, bool, error) {
	dbKey, err := s.key(key)
	if err != nil {
		return nil, false, err
	}
	value, err := s.db.Get(dbKey, nil)
	if err == leveldb.ErrNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to get state: %w", err)
	}
	return value, true, nil
}

// Put stores a value under a chain-isolated logical key.
func (s *stateStore) Put(key string, value []byte) error {
	dbKey, err := s.key(key)
	if err != nil {
		return err
	}
	err = s.db.Put(dbKey, value, nil)
	if err != nil {
		return fmt.Errorf("failed to put state: %w", err)
	}
	return nil
}

// Delete removes a chain-isolated logical key.
func (s *stateStore) Delete(key string) error {
	dbKey, err := s.key(key)
	if err != nil {
		return err
	}
	err = s.db.Delete(dbKey, nil)
	if err != nil {
		return fmt.Errorf("failed to delete state: %w", err)
	}
	return nil
}

// Iterate visits matching logical keys in byte order.
func (s *stateStore) Iterate(prefix string, fn func(key string, value []byte) error) error {
	if fn == nil {
		return fmt.Errorf("state store iterate callback is nil")
	}

	iterator := s.db.NewIterator(util.BytesPrefix([]byte(s.prefix+prefix)), nil)
	defer iterator.Release()

	for iterator.Next() {
		key := strings.TrimPrefix(string(iterator.Key()), s.prefix)
		value := append([]byte(nil), iterator.Value()...)

		if err := fn(key, value); err != nil {
			return err
		}
	}
	if err := iterator.Error(); err != nil {
		return fmt.Errorf("failed to iterate state: %w", err)
	}

	return nil
}
