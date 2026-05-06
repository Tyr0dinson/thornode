package rpc

import (
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
)

type GetBestChainlockResult struct {
	BlockHash  string `json:"blockhash"`
	Height     int64  `json:"height"`
	Signature  string `json:"signature"`
	KnownBlock bool   `json:"known_block"`
}

type GetRawMempoolVerboseResult struct {
	Size             int32       `json:"size"`
	Vsize            int32       `json:"vsize"`
	Weight           int32       `json:"weight"`
	Fee              float64     `json:"fee"`
	Fees             MempoolFees `json:"fees"`
	Time             int64       `json:"time"`
	Height           int64       `json:"height"`
	StartingPriority float64     `json:"startingpriority"`
	CurrentPriority  float64     `json:"currentpriority"`
	Depends          []string    `json:"depends"`
}

type MempoolFees struct {
	Base float64 `json:"base"`
}

func (r GetRawMempoolVerboseResult) FeeRateMilliSatPerVByte() uint64 {
	if r.Vsize <= 0 {
		return 0
	}

	// Dash Core 23+ may omit top-level `fee` unless `-deprecatedrpc=fees`
	// is enabled. Prefer the legacy field when present, otherwise use
	// `fees.base`.
	fee := r.Fee
	// analyze-ignore(float-comparison)
	if fee <= 0 {
		fee = r.Fees.Base
	}
	// analyze-ignore(float-comparison)
	if fee <= 0 {
		return 0
	}

	amount, err := btcutil.NewAmount(fee)
	if err != nil {
		return 0
	}
	feeMilliSats := uint64(amount) * 1000
	vsize := uint64(r.Vsize)
	return (feeMilliSats + vsize/2) / vsize
}

type DashTxRawResult struct {
	btcjson.TxRawResult
	InstantLock         bool `json:"instantlock,omitempty"`
	InstantLockInternal bool `json:"instantlock_internal,omitempty"`
	ChainLock           bool `json:"chainlock,omitempty"`
}

func (c *Client) GetBestChainlock() (*GetBestChainlockResult, error) {
	var result GetBestChainlockResult
	err := c.Call(&result, "getbestchainlock")
	return &result, extractBTCError(err)
}

func (c *Client) GetRawMempoolVerbose() (map[string]GetRawMempoolVerboseResult, error) {
	result := make(map[string]GetRawMempoolVerboseResult)
	err := c.Call(&result, "getrawmempool", true)
	return result, extractBTCError(err)
}

func (c *Client) GetRawTransactionVerboseDASH(txid string) (*DashTxRawResult, error) {
	var tx DashTxRawResult
	err := c.Call(&tx, "getrawtransaction", txid, true)
	return &tx, extractBTCError(err)
}
