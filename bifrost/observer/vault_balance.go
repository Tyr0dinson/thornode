package observer

import (
	"gitlab.com/thorchain/thornode/common"
)

// VaultBalance represent the account balance of an asgard vault
type VaultBalance struct {
	Chain   common.Chain   `json:"chain"`
	PubKey  common.PubKey  `json:"pub_key"`
	Address common.Address `json:"address"`
	Coins   common.Coins   `json:"coins"`
}

// IsEmpty check whether vault balance is empty or not
func (b *VaultBalance) IsEmpty() bool {
	return b.Chain.IsEmpty() && b.PubKey.IsEmpty()
}
