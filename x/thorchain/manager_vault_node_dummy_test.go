package thorchain

import (
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

type VaultNodeDummyMgr struct{}

// NewVaultNodeDummyMgr create a new instance of VaultNodeDummyMgr
func NewVaultNodeDummyMgr() *VaultNodeDummyMgr {
	return &VaultNodeDummyMgr{}
}

func (vm *VaultNodeDummyMgr) RequestValkyrieReturn(_ cosmos.Context, _ NodeAccount, _ Manager, _ constants.ConstantValues) error {
	return kaboom
}
