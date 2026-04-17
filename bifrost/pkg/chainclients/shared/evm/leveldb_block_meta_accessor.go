package evm

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
)

// LevelDBBlockMetaAccessor struct
type LevelDBBlockMetaAccessor struct {
	PrefixBlockMeta    string
	PrefixSignedTxItem string

	// LevelDB serializes individual operations, but callers need these locks to
	// protect read-modify-write sequences that span multiple DB calls.
	blockMetaMu    sync.RWMutex
	signedTxItemMu sync.RWMutex
	db             *leveldb.DB
}

// NewLevelDBBlockMetaAccessor creates a new level db backed BlockMeta accessor
func NewLevelDBBlockMetaAccessor(prefixBlockMeta, prefixSignedTxItem string, db *leveldb.DB) (*LevelDBBlockMetaAccessor, error) {
	return &LevelDBBlockMetaAccessor{
		db:                 db,
		PrefixBlockMeta:    prefixBlockMeta,
		PrefixSignedTxItem: prefixSignedTxItem,
	}, nil
}

func (t *LevelDBBlockMetaAccessor) getBlockMetaKey(height int64) string {
	return fmt.Sprintf("%s%d", t.PrefixBlockMeta, height)
}

// GetBlockMeta at given block height ,  when the requested block meta doesn't exist , it will return nil , thus caller need to double check it
func (t *LevelDBBlockMetaAccessor) GetBlockMeta(height int64) (*evmtypes.BlockMeta, error) {
	t.blockMetaMu.RLock()
	defer t.blockMetaMu.RUnlock()

	return t.getBlockMeta(height)
}

// getBlockMeta reads one block meta without taking a lock; callers must already hold blockMetaMu.
func (t *LevelDBBlockMetaAccessor) getBlockMeta(height int64) (*evmtypes.BlockMeta, error) {
	key := t.getBlockMetaKey(height)
	exist, err := t.db.Has([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to check whether block meta(%s) exist: %w", key, err)
	}
	if !exist {
		return nil, nil
	}
	v, err := t.db.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get block meta(%s) from storage: %w", key, err)
	}
	var blockMeta evmtypes.BlockMeta
	if err = json.Unmarshal(v, &blockMeta); err != nil {
		return nil, fmt.Errorf("fail to unmarshal block meta from json: %w", err)
	}
	return &blockMeta, nil
}

// SaveBlockMeta persistent the given BlockMeta into storage
func (t *LevelDBBlockMetaAccessor) SaveBlockMeta(height int64, blockMeta *evmtypes.BlockMeta) error {
	t.blockMetaMu.Lock()
	defer t.blockMetaMu.Unlock()

	return t.saveBlockMeta(height, blockMeta)
}

// saveBlockMeta writes one block meta without taking a lock; callers must already hold blockMetaMu.
func (t *LevelDBBlockMetaAccessor) saveBlockMeta(height int64, blockMeta *evmtypes.BlockMeta) error {
	key := t.getBlockMetaKey(height)
	buf, err := json.Marshal(blockMeta)
	if err != nil {
		return fmt.Errorf("fail to marshal block meta to json: %w", err)
	}
	return t.db.Put([]byte(key), buf, nil)
}

// GetBlockMetas returns all the block metas in storage
// The chain client will Prune block metas every time it finished scan a block , so at maximum it will keep BlockCacheSize blocks
// thus it should not grow out of control
func (t *LevelDBBlockMetaAccessor) GetBlockMetas() ([]*evmtypes.BlockMeta, error) {
	t.blockMetaMu.RLock()
	defer t.blockMetaMu.RUnlock()

	return t.getBlockMetas()
}

// getBlockMetas reads all block metas without taking a lock; callers must already hold blockMetaMu.
func (t *LevelDBBlockMetaAccessor) getBlockMetas() ([]*evmtypes.BlockMeta, error) {
	blockMetas := make([]*evmtypes.BlockMeta, 0)
	iterator := t.db.NewIterator(util.BytesPrefix([]byte(t.PrefixBlockMeta)), nil)
	defer iterator.Release()
	for iterator.Next() {
		buf := iterator.Value()
		if len(buf) == 0 {
			continue
		}
		var blockMeta evmtypes.BlockMeta
		if err := json.Unmarshal(buf, &blockMeta); err != nil {
			return nil, fmt.Errorf("fail to unmarshal block meta: %w", err)
		}
		blockMetas = append(blockMetas, &blockMeta)
	}
	if err := iterator.Error(); err != nil {
		return nil, fmt.Errorf("fail to iterate block metas: %w", err)
	}
	return blockMetas, nil
}

// AppendBlockMetaTransaction appends a transaction to an existing block meta under the block-meta lock.
func (t *LevelDBBlockMetaAccessor) AppendBlockMetaTransaction(height int64, tx evmtypes.TransactionMeta) (bool, error) {
	t.blockMetaMu.Lock()
	defer t.blockMetaMu.Unlock()

	blockMeta, err := t.getBlockMeta(height)
	if err != nil {
		return false, err
	}
	if blockMeta == nil {
		return false, nil
	}
	// Keep the duplicate check and append in the same critical section so
	// observer and scanner paths cannot both append the same transaction.
	for _, existing := range blockMeta.Transactions {
		if existing.Hash == tx.Hash {
			return true, nil
		}
	}

	blockMeta.Transactions = append(blockMeta.Transactions, tx)
	if err := t.saveBlockMeta(height, blockMeta); err != nil {
		return true, err
	}
	return true, nil
}

// UpdateBlockMetas serializes read-modify-save cycles for all stored block metas.
func (t *LevelDBBlockMetaAccessor) UpdateBlockMetas(update BlockMetaUpdateFunc) error {
	t.blockMetaMu.Lock()
	defer t.blockMetaMu.Unlock()

	blockMetas, err := t.getBlockMetas()
	if err != nil {
		return err
	}
	for _, blockMeta := range blockMetas {
		// The callback runs under blockMetaMu and must not call block-meta
		// accessor methods, or it would deadlock on this same mutex.
		save, err := update(blockMeta)
		if err != nil {
			return err
		}
		if !save {
			continue
		}
		if err := t.saveBlockMeta(blockMeta.Height, blockMeta); err != nil {
			return err
		}
	}
	return nil
}

// PruneBlockMeta removes all block meta that is older than the given block height.
func (t *LevelDBBlockMetaAccessor) PruneBlockMeta(height int64) error {
	t.blockMetaMu.Lock()
	defer t.blockMetaMu.Unlock()

	iterator := t.db.NewIterator(util.BytesPrefix([]byte(t.PrefixBlockMeta)), nil)
	defer iterator.Release()
	targetToDelete := make([]string, 0)
	for iterator.Next() {
		buf := iterator.Value()
		if len(buf) == 0 {
			continue
		}
		var blockMeta evmtypes.BlockMeta
		if err := json.Unmarshal(buf, &blockMeta); err != nil {
			return fmt.Errorf("fail to unmarshal block meta: %w", err)
		}
		if blockMeta.Height < height {
			targetToDelete = append(targetToDelete, t.getBlockMetaKey(blockMeta.Height))
		}
	}
	if err := iterator.Error(); err != nil {
		return fmt.Errorf("fail to iterate block metas for pruning: %w", err)
	}

	for _, key := range targetToDelete {
		if err := t.db.Delete([]byte(key), nil); err != nil {
			return fmt.Errorf("fail to delete block meta with key(%s) from storage: %w", key, err)
		}
	}
	return nil
}

func (t *LevelDBBlockMetaAccessor) getSignedTxItemKey(hash string) string {
	return t.PrefixSignedTxItem + hash
}

// AddSignedTxItem add a signed tx item to key value store
func (t *LevelDBBlockMetaAccessor) AddSignedTxItem(item evmtypes.SignedTxItem) error {
	t.signedTxItemMu.Lock()
	defer t.signedTxItemMu.Unlock()

	return t.addSignedTxItem(item)
}

// addSignedTxItem writes one signed tx item without taking a lock; callers must already hold signedTxItemMu.
func (t *LevelDBBlockMetaAccessor) addSignedTxItem(item evmtypes.SignedTxItem) error {
	key := t.getSignedTxItemKey(item.Hash)
	buf, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("fail to marshal signed tx item to json: %w", err)
	}
	return t.db.Put([]byte(key), buf, nil)
}

// RemoveSignedTxItem remove a signed item from key value store
func (t *LevelDBBlockMetaAccessor) RemoveSignedTxItem(hash string) error {
	t.signedTxItemMu.Lock()
	defer t.signedTxItemMu.Unlock()

	return t.removeSignedTxItem(hash)
}

// removeSignedTxItem deletes one signed tx item without taking a lock; callers must already hold signedTxItemMu.
func (t *LevelDBBlockMetaAccessor) removeSignedTxItem(hash string) error {
	key := t.getSignedTxItemKey(hash)
	return t.db.Delete([]byte(key), nil)
}

// GetSignedTxItems get all the signed tx items that in the key value store
func (t *LevelDBBlockMetaAccessor) GetSignedTxItems() ([]evmtypes.SignedTxItem, error) {
	t.signedTxItemMu.RLock()
	defer t.signedTxItemMu.RUnlock()

	return t.getSignedTxItems()
}

// getSignedTxItems reads all signed tx items without taking a lock; callers must already hold signedTxItemMu.
func (t *LevelDBBlockMetaAccessor) getSignedTxItems() ([]evmtypes.SignedTxItem, error) {
	txItems := make([]evmtypes.SignedTxItem, 0)
	iterator := t.db.NewIterator(util.BytesPrefix([]byte(t.PrefixSignedTxItem)), nil)
	defer iterator.Release()
	for iterator.Next() {
		buf := iterator.Value()
		if len(buf) == 0 {
			continue
		}
		var txItem evmtypes.SignedTxItem
		if err := json.Unmarshal(buf, &txItem); err != nil {
			return nil, fmt.Errorf("fail to unmarshal sign tx items: %w", err)
		}
		txItems = append(txItems, txItem)
	}
	if err := iterator.Error(); err != nil {
		return nil, fmt.Errorf("fail to iterate signed tx items: %w", err)
	}
	return txItems, nil
}

// ProcessSignedTxItems serializes list-process-delete cycles for signed tx items.
func (t *LevelDBBlockMetaAccessor) ProcessSignedTxItems(process SignedTxItemProcessFunc) error {
	t.signedTxItemMu.Lock()
	defer t.signedTxItemMu.Unlock()

	txItems, err := t.getSignedTxItems()
	if err != nil {
		return err
	}
	for _, item := range txItems {
		// The callback runs under signedTxItemMu and must not call signed-tx
		// accessor methods, or it would deadlock on this same mutex.
		remove, stop, err := process(item)
		if err != nil {
			return err
		}
		if remove {
			if err := t.removeSignedTxItem(item.Hash); err != nil {
				return err
			}
		}
		if stop {
			return nil
		}
	}
	return nil
}
