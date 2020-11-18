package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// StoreManager define the method as the entry point for store upgrade
type StoreManager interface {
	Iterator(_ cosmos.Context) error
}

// StoreMgr implement StoreManager interface
type StoreMgr struct {
	keeper keeper.Keeper
}

// NewStoreMgr create a new instance of StoreMgr
func NewStoreMgr(keeper keeper.Keeper) *StoreMgr {
	return &StoreMgr{keeper: keeper}
}

// Iterator implement StoreManager interface decide whether it need to upgrade store
func (smgr *StoreMgr) Iterator(ctx cosmos.Context) error {
	version := smgr.keeper.GetLowestActiveVersion(ctx)

	if version.Major > constants.SWVersion.Major || version.Minor > constants.SWVersion.Minor {
		return fmt.Errorf("out of date software: have %s, network running %s", constants.SWVersion, version)
	}

	storeVer := smgr.keeper.GetStoreVersion(ctx)
	if storeVer < 0 {
		return fmt.Errorf("unable to get store version: %d", storeVer)
	}
	constantAccessor := constants.GetConstantValues(version)
	if uint64(storeVer) < version.Minor {
		for i := uint64(storeVer + 1); i <= version.Minor; i++ {
			if err := smgr.migrate(ctx, i, constantAccessor); err != nil {
				return err
			}
		}
	} else {
		ctx.Logger().Debug("No store migration needed")
	}
	return nil
}

func (smgr *StoreMgr) migrate(ctx cosmos.Context, i uint64, constantAccessor constants.ConstantValues) error {
	ctx.Logger().Info("Migrating store to new version", "version", i)
	// add the logic to migrate store here when it is needed
	switch i {
	case 8:
		if err := fixPoolAsset(ctx, smgr.keeper, constantAccessor); err != nil {
			ctx.Logger().Error("fail to update pool asset", "error", err)
		}
	case 12:
		// https://gitlab.com/thorchain/thornode/-/merge_requests/1203
		vaultData, err := smgr.keeper.GetVaultData(ctx)
		if err != nil {
			ctx.Logger().Error("fail to get vault data", "error", err)
			return err
		}

		// this address will only exist on choasnet, thus on other environment, it will fail to parse the address
		// given that , if the address fail to parse , the migration should be skipped
		attackerAddr, err := cosmos.AccAddressFromBech32("thor1706lhut7y6r4h6jjrcjyr7z6jxkjghf37nkfjn")
		if err != nil {
			ctx.Logger().Error("fail to acc address", "error", err)
			break
		}

		attacker, err := smgr.keeper.GetNodeAccount(ctx, attackerAddr)
		if err != nil {
			ctx.Logger().Error("fail to get attacker node account", "error", err)
			return err
		}

		// check if attacker exists. This is so this modification doesn't
		// happen on testnet or other environments
		if !attacker.IsEmpty() {
			stolen := cosmos.NewUint(34777 * common.One)
			stolen = stolen.Sub(attacker.Bond)
			vaultData.TotalReserve = vaultData.TotalReserve.Sub(stolen)
			if err := smgr.keeper.SetVaultData(ctx, vaultData); err != nil {
				ctx.Logger().Error("fail to set vault data", "error", err)
				return err
			}

			attacker.Bond = cosmos.ZeroUint()
			if err := smgr.keeper.SetNodeAccount(ctx, attacker); err != nil {
				ctx.Logger().Error("fail to set attacker node account", "error", err)
				return err
			}
		}
	case 17:
		smgr.updateVaultV17(ctx)
	}
	smgr.keeper.SetStoreVersion(ctx, int64(i))
	return nil
}

// updateVaultV17 this is a specific change target for multichain test
// Due to a bug related to BTC confirmation counting , the vault migration left some BTC behind, refer to the following link for more detail
// http://34.209.210.60:1317/thorchain/vault/BTC/tb1qcawdece9vufus5vuw9zzd7hm7wp320kvjt3vsq
// the root cause is because internal tx has not been excude from confirmation counting , which has been fixed in develop branch
// a while ago.  This method will set the vault to retiring again, so the left over BTC will be migrated to the vault
// as soon as multichain testnet get upgrade to 0.17.0 , it will
func (smgr *StoreMgr) updateVaultV17(ctx cosmos.Context) {
	// this is only for testnet-multichain
	pubKey, err := common.NewPubKey("tthorpub1addwnpepqgdpgwuqnm00xpm7jnu7veqqm0ykf0qpdk93y79ykraneqng34r8qepgfnx")
	if err != nil {
		ctx.Logger().Error("fail to parse pubkey", "error", err)
		return
	}

	vault, err := smgr.keeper.GetVault(ctx, pubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return
	}
	vault.Status = RetiringVault
	if err := smgr.keeper.SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to save vault", "error", err)
	}
}
