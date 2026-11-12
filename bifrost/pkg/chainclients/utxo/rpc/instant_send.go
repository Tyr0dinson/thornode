package rpc

import "encoding/json"

// GetBestChainLockResult describes Dash Core's best ChainLock.
type GetBestChainLockResult struct {
	BlockHash  string `json:"blockhash"`
	Height     int64  `json:"height"`
	Signature  string `json:"signature"`
	KnownBlock bool   `json:"known_block"`
}

// GetRawMempoolVerboseResult describes the daemon's lock-aware mempool entries.
type GetRawMempoolVerboseResult struct {
	// Dash Core returns "true", "false", or "unknown", rather than a JSON boolean.
	InstantLock string `json:"instantlock"`
	VSize       int64  `json:"vsize"`
	Fees        struct {
		Base json.Number `json:"base"`
	} `json:"fees"`
}

// InstantSendLocked interprets the daemon's internal and combined InstantSend flags.
func (tx *TxResult) InstantSendLocked() bool {
	return tx.InstantLockInternal || (tx.InstantLock && !tx.ChainLock)
}

// GetBestChainLock retrieves the daemon's best ChainLock.
func (c *Client) GetBestChainLock() (*GetBestChainLockResult, error) {
	var result GetBestChainLockResult
	err := c.Call(&result, "getbestchainlock")
	return &result, extractBTCError(err)
}

// GetRawMempoolVerbose retrieves the daemon's lock-aware mempool entries.
func (c *Client) GetRawMempoolVerbose() (map[string]GetRawMempoolVerboseResult, error) {
	result := make(map[string]GetRawMempoolVerboseResult)
	err := c.Call(&result, "getrawmempool", true)
	return result, extractBTCError(err)
}
