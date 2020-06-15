package ethereum

import (
	"encoding/json"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/ethereum/types"
)

// PrefixTxStorage declares prefix to use in leveldb to avoid conflicts
const (
	PrefixTokenMeta = `tokenmeta-`
)

// LevelDBTokenMeta struct
type LevelDBTokenMeta struct {
	db *leveldb.DB
}

// NewLevelDBTokenMeta creates a new level db backed TokenMeta
func NewLevelDBTokenMeta(db *leveldb.DB) (*LevelDBTokenMeta, error) {
	return &LevelDBTokenMeta{db: db}, nil
}

func (t *LevelDBTokenMeta) getTokenMetaKey(address string) string {
	return fmt.Sprintf(PrefixTokenMeta+"%s", address)
}

// GetTokenMeta for given token address
func (t *LevelDBTokenMeta) GetTokenMeta(address string) (*types.TokenMeta, error) {
	key := t.getTokenMetaKey(address)
	exist, err := t.db.Has([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to check whether token meta(%s) exist: %w", key, err)
	}
	if !exist {
		return nil, nil
	}
	v, err := t.db.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get token meta(%s) from storage: %w", key, err)
	}
	var TokenMeta types.TokenMeta
	if err := json.Unmarshal(v, &TokenMeta); err != nil {
		return nil, fmt.Errorf("fail to unmarshal token meta from json: %w", err)
	}
	return &TokenMeta, nil
}

// SaveTokenMeta persistent the given TokenMeta into storage
func (t *LevelDBTokenMeta) SaveTokenMeta(symbol, address string) error {
	key := t.getTokenMetaKey(address)
	tokenMeta := types.NewTokenMeta(symbol, address)
	buf, err := json.Marshal(tokenMeta)
	if err != nil {
		return fmt.Errorf("fail to marshal token meta to json: %w", err)
	}
	return t.db.Put([]byte(key), buf, nil)
}

// GetTokenMetas returns all the token metas in storage
func (t *LevelDBTokenMeta) GetTokens() ([]*types.TokenMeta, error) {
	TokenMetas := make([]*types.TokenMeta, 0)
	iterator := t.db.NewIterator(util.BytesPrefix([]byte(PrefixTokenMeta)), nil)
	defer iterator.Release()
	for iterator.Next() {
		buf := iterator.Value()
		if len(buf) == 0 {
			continue
		}
		var TokenMeta types.TokenMeta
		if err := json.Unmarshal(buf, &TokenMeta); err != nil {
			return nil, fmt.Errorf("fail to unmarshal block meta: %w", err)
		}
		TokenMetas = append(TokenMetas, &TokenMeta)
	}
	return TokenMetas, nil
}
