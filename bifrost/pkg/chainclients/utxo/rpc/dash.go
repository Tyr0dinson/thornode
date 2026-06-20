package rpc

import (
	"encoding/json"
	"fmt"
	"strings"

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

type DashISLockInput struct {
	TxID string `json:"txid"`
	Vout uint32 `json:"vout"`
}

type DashISLockResult struct {
	TxID      string            `json:"txid"`
	Inputs    []DashISLockInput `json:"inputs"`
	ID        string            `json:"id,omitempty"`
	CycleHash string            `json:"cycleHash"`
	Signature string            `json:"signature"`
	Hex       string            `json:"hex"`
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

func (c *Client) GetISLocks(txids []string) ([]*DashISLockResult, error) {
	var rawLocks []json.RawMessage
	err := c.Call(&rawLocks, "getislocks", txids)
	if err != nil {
		return nil, extractBTCError(err)
	}

	// Dash returns one array entry per requested txid. Missing locks are the
	// string "None" rather than a JSON null/object, so keep nils in-place to
	// preserve the caller's txid-to-result ordering.
	locks := make([]*DashISLockResult, len(rawLocks))
	for i, raw := range rawLocks {
		var none string
		if err := json.Unmarshal(raw, &none); err == nil && strings.EqualFold(none, "None") {
			continue
		}

		var lock DashISLockResult
		if err := json.Unmarshal(raw, &lock); err != nil {
			return nil, fmt.Errorf("decode getislocks result %d: %w", i, err)
		}
		locks[i] = &lock
	}
	return locks, nil
}
