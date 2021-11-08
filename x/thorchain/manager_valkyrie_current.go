package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

// ValkyrieMgrV65 is an implementation of ValkyrieManager
type ValkyrieMgrV74 struct {
	keeper keeper.Keeper
}

// newValkyrieMgrV74 create a new instance of ValkyrieMgrV74 which implement ValkyrieManager interface
func newValkyrieMgrV74(keeper keeper.Keeper) *ValkyrieMgrV74 {
	return &ValkyrieMgrV74{
		keeper: keeper,
	}
}

func (vmgr ValkyrieMgrV74) Fund(ctx cosmos.Context, mgr Manager, constAccessor constants.ConstantValues, na NodeAccount) error {
	if na.Type != NodeTypeVault {
		return nil
	}

	valkyrieVault, err := vmgr.keeper.GetVault(ctx, na.PubKeySet.Secp256k1)
	if err != nil {

		if valkyrieVault.Type != types.VaultType_ValkyrieVault {
			return fmt.Errorf("Failed to Fund: Vault is not a Valkyrie Vault")
		}

	}

	return nil
}
