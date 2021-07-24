package types

import (
	"gitlab.com/thorchain/thornode/common"
)

type VaultBalance struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
	Coins   common.Coins   `json:"coins"`
}
