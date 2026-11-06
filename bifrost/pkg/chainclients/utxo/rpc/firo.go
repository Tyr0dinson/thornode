package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcjson"
)

const firoRescanLabel = "thornode-firo-rescanned-v1"

// EnsureWatchAddressFIRO records successful rescans in the wallet itself. A
// timeout or crash after import must not turn a partial wallet into a trusted
// zero balance on the next call. FIRO holds its wallet lock during rescan.
func (c *Client) EnsureWatchAddressFIRO(address string) error {
	var info struct {
		IsWatchOnly bool   `json:"iswatchonly"`
		Account     string `json:"account"`
	}
	err := c.Call(&info, "validateaddress", address)
	if err != nil {
		return extractBTCError(err)
	}
	if info.IsWatchOnly && info.Account == firoRescanLabel {
		return nil
	}
	// Rescans legitimately take much longer than ordinary scanner RPCs. Do
	// not submit overlapping automatic retries on an ambiguous timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	err = c.client.CallContext(ctx, nil, "importaddress", address, "", true)
	if err != nil {
		return extractBTCError(err)
	}
	return extractBTCError(c.Call(nil, "importaddress", address, firoRescanLabel, false))
}

type FiroHeader struct {
	Hash          string `json:"hash"`
	Height        int64  `json:"height"`
	Confirmations int64  `json:"confirmations"`
	ChainLock     bool   `json:"chainlock"`
}

type FiroChainInfo struct {
	Chain         string `json:"chain"`
	Blocks        int64  `json:"blocks"`
	Headers       int64  `json:"headers"`
	BestBlockHash string `json:"bestblockhash"`
	Pruned        bool   `json:"pruned"`
}

type FiroTxResult struct {
	TxResult
	Type             int   `json:"type"`
	Height           int64 `json:"height"`
	InstantLockKnown bool  `json:"-"`
}

// FIRO returns a native boolean, unlike Dash's string-valued mempool status.
// A missing/null status must not silently become an unlocked startup snapshot.
type FiroMempoolEntry struct {
	Time        int64 `json:"time"`
	InstantLock *bool `json:"instantlock"`
}

func (c *Client) GetRawMempoolVerboseFIRO() (map[string]FiroMempoolEntry, error) {
	var entries map[string]FiroMempoolEntry
	err := c.Call(&entries, "getrawmempool", true)
	if err != nil {
		return nil, extractBTCError(err)
	}
	if entries == nil {
		return nil, fmt.Errorf("missing FIRO mempool response")
	}
	return entries, nil
}

func (c *Client) GetBlockchainInfoFIRO() (*FiroChainInfo, error) {
	var info FiroChainInfo
	err := c.Call(&info, "getblockchaininfo")
	return &info, extractBTCError(err)
}

func (c *Client) GetBlockHeaderFIRO(hash string) (*FiroHeader, error) {
	var header FiroHeader
	err := c.Call(&header, "getblockheader", hash, true)
	return &header, extractBTCError(err)
}

func (c *Client) GetRawTransactionVerboseFIRO(hash string) (*FiroTxResult, error) {
	// A transparent output may come from a special/privacy transaction. Its
	// inputs are not Bitcoin inputs, but its original vout indexes and amounts
	// are still needed to attribute a later ordinary transparent deposit.
	var raw struct {
		FiroTxResult
		Inputs json.RawMessage `json:"vin"`
		Kind   *int            `json:"type"`
		Lock   *bool           `json:"instantlock"`
	}
	err := c.Call(&raw, "getrawtransaction", hash, true)
	if err != nil {
		return nil, extractBTCError(err)
	}
	if raw.Kind == nil {
		return nil, fmt.Errorf("FIRO transaction is missing its type")
	}
	raw.Type = *raw.Kind
	raw.InstantLockKnown = raw.Lock != nil
	if raw.Lock != nil {
		raw.InstantLock = *raw.Lock
	}
	if raw.Type == 0 {
		err := json.Unmarshal(raw.Inputs, &raw.Vin)
		if err != nil {
			return nil, err
		}
	}
	if raw.Txid != hash {
		return nil, fmt.Errorf("FIRO transaction ID mismatch")
	}
	return &raw.FiroTxResult, nil
}

// GetBlockVerboseTxsFIRO normalizes only ordinary transactions for the UTXO observer.
// Special/private transactions are not Bitcoin wire transactions. Inspect their
// type before decoding their inputs, and retain the original IDs and vout indexes.
func (c *Client) GetBlockVerboseTxsFIRO(hash string) (*BlockResult, error) {
	var raw struct {
		BlockResult
		Transactions []json.RawMessage `json:"tx"`
	}
	err := c.Call(&raw, "getblock", hash, 2)
	if err != nil {
		return nil, extractBTCError(err)
	}
	for _, entry := range raw.Transactions {
		var kind struct {
			Type *int `json:"type"`
		}
		err := json.Unmarshal(entry, &kind)
		if err != nil {
			return nil, err
		}
		if kind.Type == nil {
			return nil, fmt.Errorf("FIRO transaction is missing its type")
		}
		if *kind.Type != 0 {
			continue
		}
		var tx btcjson.TxRawResult
		err = json.Unmarshal(entry, &tx)
		if err != nil {
			return nil, err
		}
		raw.BlockResult.Tx = append(raw.BlockResult.Tx, tx)
	}
	return &raw.BlockResult, nil
}
