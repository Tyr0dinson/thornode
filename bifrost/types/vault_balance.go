package types

import (
	"gitlab.com/thorchain/thornode/common"
)

type VaultBalance struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
	Coins   common.Coins   `json:"coins"`
	Txs     common.TxIDs   `json:"txs"`
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
