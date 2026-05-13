package evm

import (
	"encoding/json"
	"fmt"
	"reflect"
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

// UpdateBlockMetas snapshots block metas, runs the update callback without holding
// the block-meta lock, and then commits requested saves under the lock.
func (t *LevelDBBlockMetaAccessor) UpdateBlockMetas(update BlockMetaUpdateFunc) error {
	blockMetas, err := t.GetBlockMetas()
	if err != nil {
		return err
	}

	type blockMetaUpdate struct {
		original *evmtypes.BlockMeta
		updated  *evmtypes.BlockMeta
	}

	updates := make([]blockMetaUpdate, 0, len(blockMetas))
	for _, blockMeta := range blockMetas {
		original := cloneBlockMeta(blockMeta)
		save, err := update(blockMeta)
		if err != nil {
			return err
		}
		if save {
			updates = append(updates, blockMetaUpdate{
				original: original,
				updated:  cloneBlockMeta(blockMeta),
			})
		}
	}
	if len(updates) == 0 {
		return nil
	}

	t.blockMetaMu.Lock()
	defer t.blockMetaMu.Unlock()

	for _, item := range updates {
		current, err := t.getBlockMeta(item.original.Height)
		if err != nil {
			return err
		}
		if current == nil {
			continue
		}

		merged, err := mergeBlockMetaUpdate(item.updated, item.original, current)
		if err != nil {
			return err
		}
		if err := t.saveBlockMeta(item.original.Height, merged); err != nil {
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

// getSignedTxItem reads one signed tx item without taking a lock; callers must already hold signedTxItemMu.
func (t *LevelDBBlockMetaAccessor) getSignedTxItem(hash string) (*evmtypes.SignedTxItem, error) {
	key := t.getSignedTxItemKey(hash)
	exist, err := t.db.Has([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to check whether signed tx item(%s) exist: %w", key, err)
	}
	if !exist {
		return nil, nil
	}
	v, err := t.db.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get signed tx item(%s) from storage: %w", key, err)
	}
	var item evmtypes.SignedTxItem
	if err = json.Unmarshal(v, &item); err != nil {
		return nil, fmt.Errorf("fail to unmarshal signed tx item from json: %w", err)
	}
	return &item, nil
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

// ProcessSignedTxItems snapshots signed tx items, runs the process callback without
// holding the signed-tx lock, and removes requested unchanged items under the lock.
func (t *LevelDBBlockMetaAccessor) ProcessSignedTxItems(process SignedTxItemProcessFunc) error {
	txItems, err := t.GetSignedTxItems()
	if err != nil {
		return err
	}
	for _, item := range txItems {
		remove, stop, err := process(item)
		if err != nil {
			return err
		}
		if remove {
			if err = t.removeSignedTxItemIfUnchanged(item); err != nil {
				return err
			}
		}
		if stop {
			return nil
		}
	}
	return nil
}

func (t *LevelDBBlockMetaAccessor) removeSignedTxItemIfUnchanged(item evmtypes.SignedTxItem) error {
	t.signedTxItemMu.Lock()
	defer t.signedTxItemMu.Unlock()

	current, err := t.getSignedTxItem(item.Hash)
	if err != nil {
		return err
	}
	if current == nil || !reflect.DeepEqual(*current, item) {
		return nil
	}
	return t.removeSignedTxItem(item.Hash)
}

func cloneBlockMeta(blockMeta *evmtypes.BlockMeta) *evmtypes.BlockMeta {
	if blockMeta == nil {
		return nil
	}
	clone := *blockMeta
	if blockMeta.Transactions != nil {
		clone.Transactions = make([]evmtypes.TransactionMeta, len(blockMeta.Transactions))
		copy(clone.Transactions, blockMeta.Transactions)
	}
	return &clone
}

func mergeBlockMetaUpdate(updated, original, current *evmtypes.BlockMeta) (*evmtypes.BlockMeta, error) {
	if updated.Height != original.Height {
		return nil, fmt.Errorf("block meta height changed by update: original=%d updated=%d", original.Height, updated.Height)
	}
	if current.Height != original.Height {
		return nil, fmt.Errorf("block meta height changed during update: original=%d current=%d", original.Height, current.Height)
	}

	merged := cloneBlockMeta(updated)
	var err error
	merged.PreviousHash, err = mergeBlockMetaStringField("previous hash", original.PreviousHash, updated.PreviousHash, current.PreviousHash)
	if err != nil {
		return nil, err
	}
	merged.BlockHash, err = mergeBlockMetaStringField("block hash", original.BlockHash, updated.BlockHash, current.BlockHash)
	if err != nil {
		return nil, err
	}
	mergeCurrentBlockMetaTransactions(merged, original, current)
	return merged, nil
}

func mergeBlockMetaStringField(name, original, updated, current string) (string, error) {
	if updated == original {
		return current, nil
	}
	if current == original || current == updated {
		return updated, nil
	}
	return "", fmt.Errorf("block meta %s changed during update: original=%s updated=%s current=%s", name, original, updated, current)
}

func mergeCurrentBlockMetaTransactions(updated, original, current *evmtypes.BlockMeta) {
	originalTxs := make(map[string]struct{}, len(original.Transactions))
	for _, tx := range original.Transactions {
		originalTxs[tx.Hash] = struct{}{}
	}

	updatedTxs := make(map[string]struct{}, len(updated.Transactions))
	for _, tx := range updated.Transactions {
		updatedTxs[tx.Hash] = struct{}{}
	}

	for _, tx := range current.Transactions {
		if _, existedInSnapshot := originalTxs[tx.Hash]; existedInSnapshot {
			continue
		}
		if _, alreadyUpdated := updatedTxs[tx.Hash]; alreadyUpdated {
			continue
		}
		updated.Transactions = append(updated.Transactions, tx)
		updatedTxs[tx.Hash] = struct{}{}
	}
}
