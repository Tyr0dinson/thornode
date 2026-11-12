package tron

import (
	"strings"
	"sync"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

type tronOutboundLock struct {
	mu    sync.Mutex
	users int
	// verifiedFailed is the cached attempt SignTx proved failed in solidified
	// state. A solidified failure is final, so BroadcastTx can reuse it rather
	// than repeat the lookup. It lives only as long as the lock entry.
	verifiedFailed string
}

// LockTxOut serializes the full sign-and-broadcast lifecycle by payment identity,
// including saved signed retries across different vaults. References include
// waiters, so entries can be removed after the last caller releases its lock.
func (c *TronClient) LockTxOut(tx types.TxOutItem) func() {
	key := tx.CacheHash()
	c.outboundLocksMu.Lock()
	if c.outboundLocks == nil {
		c.outboundLocks = make(map[string]*tronOutboundLock)
	}
	entry := c.outboundLocks[key]
	if entry == nil {
		entry = &tronOutboundLock{}
		c.outboundLocks[key] = entry
	}
	entry.users++
	c.outboundLocksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		c.outboundLocksMu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(c.outboundLocks, key)
		}
		c.outboundLocksMu.Unlock()
	}
}

// setVerifiedFailed records the attempt SignTx proved failed for a held
// payment lock; an empty sig clears it. Callers without a lock record nothing.
func (c *TronClient) setVerifiedFailed(key, sig string) {
	c.outboundLocksMu.Lock()
	defer c.outboundLocksMu.Unlock()
	if entry := c.outboundLocks[key]; entry != nil {
		entry.verifiedFailed = sig
	}
}

// isVerifiedFailed reports whether sig is the attempt SignTx proved failed.
func (c *TronClient) isVerifiedFailed(key, sig string) bool {
	c.outboundLocksMu.Lock()
	defer c.outboundLocksMu.Unlock()
	entry := c.outboundLocks[key]
	return entry != nil && entry.verifiedFailed != "" && strings.EqualFold(entry.verifiedFailed, sig)
}
