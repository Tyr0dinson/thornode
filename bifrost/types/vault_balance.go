package types

import (
	"gitlab.com/thorchain/thornode/common"
)

const (
	MaxCacheItem = 1024
)

type VaultBalance struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
	Coins   common.Coins   `json:"coins"`
	Txs     common.TxIDs   `json:"txs"`
}

// AddTxID add a tx id to the vault balance
func (vb *VaultBalance) AddTxID(txID common.TxID) {
	vb.Txs = append(vb.Txs, txID)
	if len(vb.Txs) > MaxCacheItem {
		vb.Txs = vb.Txs[len(vb.Txs)-MaxCacheItem:]
	}
}

// HasTxID check whether the txid already apply to current vault
func (vb VaultBalance) HasTxID(txID common.TxID) bool {
	for _, item := range vb.Txs {
		if item.Equals(txID) {
			return true
		}
	}
	return false
}
