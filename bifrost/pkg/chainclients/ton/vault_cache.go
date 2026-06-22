package ton

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
)

type Cache struct {
	pubkeyMgr pubkeymanager.PubKeyValidator
	vaults    map[string]struct{}
	chain     common.Chain
	mtx       sync.RWMutex
}

func NewCache(pubkeyMgr pubkeymanager.PubKeyValidator, chain common.Chain) *Cache {
	return &Cache{
		pubkeyMgr: pubkeyMgr,
		vaults:    make(map[string]struct{}),
		chain:     chain,
		mtx:       sync.RWMutex{},
	}
}

func (c *Cache) Reset() error {
	pubkeys := c.pubkeyMgr.GetAlgoPubKeys(c.chain.GetSigningAlgo(), true)
	vaults := make(map[string]struct{}, len(pubkeys))
	for _, pubkey := range pubkeys {
		address, err := pubkey.GetAddress(c.chain)
		if err != nil {
			return err
		}
		vaults[address.String()] = struct{}{}
	}

	c.mtx.Lock()
	c.vaults = vaults
	defer c.mtx.Unlock()

	return nil
}

func (c *Cache) Has(address string) bool {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	_, found := c.vaults[address]
	return found
}
