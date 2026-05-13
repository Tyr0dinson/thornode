package substrate

import (
	"fmt"
	"sync"
)

// migrationTxCache records hashes of vault-sender internal-memo extrinsics
// (Migrate, Consolidate) observed by the scanner. BroadcastTx reads it on
// Stale errors to distinguish a confirmed retry from a true abandon.
type migrationTxCache struct {
	mtx             sync.RWMutex
	migrationHashes map[string]struct{}
	hashesByHeight  map[uint64][]string
}

func newMigrationTxCache() *migrationTxCache {
	return &migrationTxCache{
		mtx:             sync.RWMutex{},
		migrationHashes: make(map[string]struct{}),
		hashesByHeight:  make(map[uint64][]string),
	}
}

func (c *migrationTxCache) Add(hash string, height uint64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	hashes, found := c.hashesByHeight[height]
	if !found {
		hashes = []string{}
	}

	c.migrationHashes[hash] = struct{}{}
	c.hashesByHeight[height] = append(hashes, hash)
}

func (c *migrationTxCache) Has(hash string) bool {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	_, found := c.migrationHashes[hash]
	return found
}

func (c *migrationTxCache) Purge(before int64) {
	if before <= 0 {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for height, hashes := range c.hashesByHeight {
		if height >= uint64(before) {
			continue
		}

		for _, hash := range hashes {
			delete(c.migrationHashes, hash)
		}
		delete(c.hashesByHeight, height)
	}
}

func migrationTxCacheKey(sender string, nonce uint64) string {
	return fmt.Sprintf("%s_%d", sender, nonce)
}
