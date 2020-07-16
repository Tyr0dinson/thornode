package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var defaultStartVersion = semver.MustParse("0.0.0")

// StoreManager define the method as the entry point for store upgrade
type StoreManager interface {
	Iterator(_ cosmos.Context) error
}

// StoreMgr implement StoreManager interface
type StoreMgr struct {
	keeper           keeper.Keeper
	lastChainVersion semver.Version
}

// NewStoreMgr create a new instance of StoreMgr
func NewStoreMgr(keeper keeper.Keeper) *StoreMgr {
	return &StoreMgr{
		keeper:           keeper,
		lastChainVersion: defaultStartVersion,
	}
}

// Iterator implement StoreManager interface decide whether it need to upgrade store
func (smgr *StoreMgr) Iterator(ctx cosmos.Context) error {
	version := smgr.keeper.GetLowestActiveVersion(ctx)
	if smgr.lastChainVersion.EQ(defaultStartVersion) {
		// it knows nothing at the moment , so keep the version and return
		smgr.lastChainVersion = version
		return nil
	}
	if version.Major > constants.SWVersion.Major || version.Minor > constants.SWVersion.Minor {
		return fmt.Errorf("out of date software: have %s, network running %s", constants.SWVersion, version)
	}
	ctx.Logger().Info(fmt.Sprintf("current version:%s", version))
	// what's the chain version , what's the version of local node account
	// when the lowest active version is the same as current build version, then nothing need to migrate
	if version.EQ(smgr.lastChainVersion) {
		ctx.Logger().Info("nothing to migrate")
		return nil
	}

	// get to here , means version get bumped
	storeVer := smgr.keeper.GetStoreVersion(ctx)
	if storeVer < 0 {
		return fmt.Errorf("unable to get store version: %d", storeVer)
	}

	if uint64(storeVer) < version.Minor {
		for i := uint64(storeVer + 1); i <= version.Minor; i++ {
			if err := smgr.migrate(ctx, i); err != nil {
				return err
			}
		}
	} else {
		ctx.Logger().Debug("No store migration needed")
	}
	smgr.lastChainVersion = version
	return nil
}

func (smgr *StoreMgr) migrate(ctx cosmos.Context, i uint64) error {
	var err error
	ctx.Logger().Info("Migrating store to new version", "version", i)

	switch i {
	case uint64(2):
		err = migrateStoreV2(ctx, smgr.keeper)
	default:
		err = fmt.Errorf("unsupported store version: %d", i)
	}
	if err != nil {
		ctx.Logger().Error("fail to migrate kvstore", "error", err)
		return err
	}

	smgr.keeper.SetStoreVersion(ctx, int64(i))
	return nil
}
