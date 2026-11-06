package utxo

import (
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

// Dash signing must stop well before a prolonged ChainLock stall can lead to
// outbound reassignment. Allow one block of lag and one expected block interval
// of height-data age, rather than the shared scanner's longer MaxHealthyLag.
const dashMaxHealthyBlockLag int64 = 1

// isHealthyDASH reports whether height data is fresh and both ChainLock and scanner
// lag are within the Dash signing limit.
func (h *finalitySnapshot) isHealthyDASH(now time.Time, scannedHeight int64) bool {
	maxAge := time.Duration(dashMaxHealthyBlockLag*common.DASHChain.ApproximateBlockMilliseconds()) * time.Millisecond
	if h == nil || h.checkedAt.IsZero() || now.Sub(h.checkedAt) >= maxAge {
		return false
	}
	// Check finality even when the scanner is caught up to a frozen ChainLock.
	// Also bound the unscanned backlog so a new ChainLock cannot immediately
	// restore signing using the scanner's old healthy flag before catch-up.
	return h.blockHeight >= h.chainLockHeight &&
		h.blockHeight-h.chainLockHeight <= dashMaxHealthyBlockLag &&
		h.blockHeight-scannedHeight <= dashMaxHealthyBlockLag
}

// isTransactionQuorumLockedDASH checks daemon lock status for balance reporting.
// The cache belongs to one operation, so a previously unlocked tx is refreshed
// on the next call and multiple outputs of one tx need only one RPC lookup.
func (c *Client) isTransactionQuorumLockedDASH(txID string, cache map[string]bool) (bool, error) {
	key := strings.ToLower(txID)
	if final, found := cache[key]; found {
		return final, nil
	}
	tx, err := c.rpc.GetRawTransactionVerbose(txID)
	if err != nil {
		return false, fmt.Errorf("fail to get transaction locks for %s: %w", txID, err)
	}
	if !strings.EqualFold(tx.Txid, txID) {
		return false, fmt.Errorf("finality txid mismatch: got %q, want %q", tx.Txid, txID)
	}
	cache[key] = tx.QuorumLockedDASH()
	return cache[key], nil
}

// getHeightDASH returns the network ChainLock target, including during local catch-up.
func (c *Client) getHeightDASH() (int64, error) {
	// Slow RPCs must not publish old height data with a new timestamp.
	checkedAt := time.Now()
	bestChainLock, err := c.rpc.GetBestChainLockDASH()
	if err != nil {
		return 0, err
	}
	if bestChainLock.Height < 0 {
		return 0, fmt.Errorf("best Dash ChainLock height is negative")
	}
	blockHeight, err := c.rpc.GetBlockCount()
	if err != nil {
		return 0, err
	}
	if blockHeight < 0 {
		return 0, fmt.Errorf("best Dash block height is negative")
	}
	// During catch-up the target may not even have a known header yet.
	// getBlock still requires each fetched block to be ChainLocked.
	if blockHeight >= bestChainLock.Height {
		if !bestChainLock.KnownBlock {
			return 0, fmt.Errorf("best Dash ChainLock block is not available locally")
		}
		hash, err := c.rpc.GetBlockHash(bestChainLock.Height)
		if err != nil {
			return 0, err
		}
		if !strings.EqualFold(hash, bestChainLock.BlockHash) {
			return 0, fmt.Errorf("best Dash ChainLock does not match the active chain")
		}
	}
	// Failed reads or validation leave the previous snapshot to expire.
	c.finality.Store(&finalitySnapshot{
		blockHeight:     blockHeight,
		chainLockHeight: bestChainLock.Height,
		checkedAt:       checkedAt,
	})
	// Keep the network's ChainLock as the target and lag reference, even when
	// the daemon cannot serve it yet. Never scan the unfinalized ordinary tip.
	return bestChainLock.Height, nil
}

// validateSolvencyDASH requires local ChainLock evidence before reporting balances.
func (c *Client) validateSolvencyDASH(height int64) error {
	// GetHeight permits scanner catch-up. Solvency must independently read
	// both heights and validate local finality before trusting balances.
	bestChainLock, err := c.rpc.GetBestChainLockDASH()
	if err != nil {
		return fmt.Errorf("fail to get Dash ChainLock for solvency: %w", err)
	}
	if bestChainLock.Height < 0 {
		return fmt.Errorf("best Dash ChainLock height is negative")
	}
	blockHeight, err := c.rpc.GetBlockCount()
	if err != nil {
		return fmt.Errorf("fail to get Dash block height for solvency: %w", err)
	}
	if blockHeight < bestChainLock.Height {
		return fmt.Errorf("best Dash block height is below the ChainLock height")
	}
	if height > bestChainLock.Height {
		return fmt.Errorf("Dash solvency height is above the ChainLock height")
	}
	if !bestChainLock.KnownBlock {
		return fmt.Errorf("best Dash ChainLock block is not available locally")
	}
	hash, err := c.rpc.GetBlockHash(bestChainLock.Height)
	if err != nil {
		return fmt.Errorf("fail to get Dash ChainLock block hash for solvency: %w", err)
	}
	if !strings.EqualFold(hash, bestChainLock.BlockHash) {
		return fmt.Errorf("best Dash ChainLock does not match the active chain")
	}
	return nil
}
