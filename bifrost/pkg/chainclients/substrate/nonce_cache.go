package substrate

import "sync"

type nonceCache struct {
	mtx            *sync.RWMutex
	noncesByHash   map[string]uint64
	hashesByHeight map[uint64][]string
}

func newNonceCache() *nonceCache {
	return &nonceCache{
		mtx:            new(sync.RWMutex),
		noncesByHash:   make(map[string]uint64),
		hashesByHeight: make(map[uint64][]string),
	}
}

func (c *nonceCache) Add(hash string, nonce uint64, height uint64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	hashes, found := c.hashesByHeight[height]
	if !found {
		hashes = []string{}
	}

	c.noncesByHash[hash] = nonce
	c.hashesByHeight[height] = append(hashes, hash)
}

func (c *nonceCache) Get(hash string) (uint64, bool) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	nonce, found := c.noncesByHash[hash]
	return nonce, found
}

func (c *nonceCache) Purge(before int64) {
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
			delete(c.noncesByHash, hash)
		}
		delete(c.hashesByHeight, height)
	}
}
