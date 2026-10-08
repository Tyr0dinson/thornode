package utxo

import (
	"fmt"
	"strings"
)

// isTransactionQuorumLocked shares daemon facts between balance and spending policy.
// The cache belongs to one operation, so a previously unlocked tx is refreshed
// on the next call and multiple outputs of one tx need only one RPC lookup.
func (c *Client) isTransactionQuorumLocked(txID string, cache map[string]bool) (bool, error) {
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
	cache[key] = tx.QuorumLocked()
	return cache[key], nil
}
