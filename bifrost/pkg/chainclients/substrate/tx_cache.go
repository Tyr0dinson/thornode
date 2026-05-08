package substrate

import "sync"

// migrationTxCache records hashes of vault-sender internal-memo extrinsics
// (Migrate, Consolidate) observed by the scanner. BroadcastTx reads it on
// Stale errors to distinguish a confirmed retry from a true abandon.
type migrationTxCache struct {
	txs map[string]struct{}
	mtx sync.RWMutex
}

func newMigrationTxCache() *migrationTxCache {
	return &migrationTxCache{
		txs: make(map[string]struct{}),
		mtx: sync.RWMutex{},
	}
}

func (c *migrationTxCache) Add(hash string) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.txs[hash] = struct{}{}
}

func (c *migrationTxCache) Has(hash string) bool {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	_, found := c.txs[hash]
	return found
}
