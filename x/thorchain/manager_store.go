package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// StoreManager define the method as the entry point for store upgrade
type StoreManager interface {
	Iterator(_ cosmos.Context) error
}

// StoreMgr implement StoreManager interface
type StoreMgr struct {
	mgr *Mgrs
}

// newStoreMgr create a new instance of StoreMgr
func newStoreMgr(mgr *Mgrs) *StoreMgr {
	return &StoreMgr{
		mgr: mgr,
	}
}

// Iterator implement StoreManager interface decide whether it need to upgrade store
func (smgr *StoreMgr) Iterator(ctx cosmos.Context) error {
	version := smgr.mgr.GetVersion()

	if version.LT(semver.MustParse("1.90.0")) {
		version = smgr.mgr.Keeper().GetLowestActiveVersion(ctx) // TODO remove me on hard fork
	}

	if version.Major > constants.SWVersion.Major || version.Minor > constants.SWVersion.Minor {
		return fmt.Errorf("out of date software: have %s, network running %s", constants.SWVersion, version)
	}

	storeVer := smgr.mgr.Keeper().GetStoreVersion(ctx)
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
	return nil
}

func (smgr *StoreMgr) migrate(ctx cosmos.Context, i uint64) error {
	ctx.Logger().Info("Migrating store to new version", "version", i)
	// add the logic to migrate store here when it is needed

	switch i {
	case 84:
		migrateStoreV84(ctx, smgr.mgr)
	case 85:
		migrateStoreV85(ctx, smgr.mgr)
	case 86:
		migrateStoreV86(ctx, smgr.mgr)
	case 87:
		migrateStoreV87(ctx, smgr.mgr)
	case 88:
		migrateStoreV88(ctx, smgr.mgr)
	case 92:
		migrateStoreV92(ctx, smgr.mgr)
		migrateStoreV92_USDCBalance(ctx, smgr.mgr)
	case 94:
		migrateStoreV94(ctx, smgr.mgr)
	case 95:
		migrateStoreV95(ctx, smgr.mgr)
	case 102:
		migrateStoreV102(ctx, smgr.mgr)
	case 103:
		migrateStoreV103(ctx, smgr.mgr)
	case 106:
		migrateStoreV106(ctx, smgr.mgr)
	case 108:
		migrateStoreV108(ctx, smgr.mgr)
	case 109:
		migrateStoreV109(ctx, smgr.mgr)
	case 110:
		migrateStoreV110(ctx, smgr.mgr)
	case 111:
		migrateStoreV111(ctx, smgr.mgr)
	case 113:
		migrateStoreV113(ctx, smgr.mgr)
	case 114:
		migrateStoreV114(ctx, smgr.mgr)
	case 116:
		migrateStoreV116(ctx, smgr.mgr)
	case 117:
		migrateStoreV117(ctx, smgr.mgr)
	case 121:
		migrateStoreV121(ctx, smgr.mgr)
	case 122:
		migrateStoreV122(ctx, smgr.mgr)
	case 123:
		migrateStoreV123(ctx, smgr.mgr)
	case 124:
		migrateStoreV124(ctx, smgr.mgr)
	case 125:
		migrateStoreV125(ctx, smgr.mgr)
	case 126:
		migrateStoreV126(ctx, smgr.mgr)
	case 128:
		migrateStoreV128(ctx, smgr.mgr)
	case 129:
		migrateStoreV129(ctx, smgr.mgr)
	case 131:
		migrateStoreV131(ctx, smgr.mgr)
	case 132:
		migrateStoreV132(ctx, smgr.mgr)
	case 133:
		migrateStoreV133(ctx, smgr.mgr)
	case 134:
		migrateStoreV134(ctx, smgr.mgr)
	}

	smgr.mgr.Keeper().SetStoreVersion(ctx, int64(i))
	return nil
}

func migrateStoreV84(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV85(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV87(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV92(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV92_USDCBalance(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV94(ctx cosmos.Context, mgr *Mgrs) {}

func migrateStoreV95(ctx cosmos.Context, mgr *Mgrs) {}
