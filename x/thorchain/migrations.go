package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	v2 "gitlab.com/thorchain/thornode/v3/x/thorchain/migrations/v2"
)

// Migrator is a struct for handling in-place store migrations.
type Migrator struct {
	mgr *Mgrs
}

// NewMigrator returns a new Migrator.
func NewMigrator(mgr *Mgrs) Migrator {
	return Migrator{mgr: mgr}
}

// Migrate1to2 migrates from version 1 to 2.
func (m Migrator) Migrate1to2(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	return v2.MigrateStore(ctx, m.mgr.storeKey)
}

// Migrate2to3 migrates from version 2 to 3.
func (m Migrator) Migrate2to3(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// refund stagenet funding wallet for user refund
	// original user tx: https://runescan.io/tx/A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52
	// refund to user from stagenet funding wallet: https://bscscan.com/tx/0xba67f3a88f8c998f29e774ffa8328e5625521e37c2db282b29a04ab3d2593f48
	stagenetWallet := "0x3021C479f7F8C9f1D5c7d8523BA5e22C0Bcb5430"
	inTxId := "A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52" // original user tx

	bscUsdt, err := common.NewAsset("BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	if err != nil {
		return err
	}
	usdtCoin := common.NewCoin(bscUsdt, cosmos.NewUint(4860737515919))
	blockHeight := ctx.BlockHeight()

	// schedule refund
	if err := unsafeAddRefundOutbound(ctx, m.mgr, inTxId, stagenetWallet, usdtCoin, blockHeight); err != nil {
		return err
	}

	// bootstrap priority addrs
	// these 4 thor addresses handle 95% of arb volume on the network
	m.mgr.Keeper().SetMimir(ctx, "PRIORITY-ADDR-thor1ukwhpglu7yh2g2rw8h7jvee2r0fv0e90nyxv6v", 1)
	m.mgr.Keeper().SetMimir(ctx, "PRIORITY-ADDR-thor17hwqt302e5f2xm4h95ma8wuggqkvfzgvsnh5z9", 1)
	m.mgr.Keeper().SetMimir(ctx, "PRIORITY-ADDR-thor14mh37ua4vkyur0l5ra297a4la6tmf95mt96a55", 1)
	m.mgr.Keeper().SetMimir(ctx, "PRIORITY-ADDR-thor166n4w5039meulfa3p6ydg60ve6ueac7tlt0jws", 1)

	// set more lax max confirmations
	m.mgr.Keeper().SetMimir(ctx, "MAXCONFIRMATIONS-BTC", 3)
	m.mgr.Keeper().SetMimir(ctx, "MAXCONFIRMATIONS-BCH", 3)
	m.mgr.Keeper().SetMimir(ctx, "MAXCONFIRMATIONS-DOGE", 10)
	m.mgr.Keeper().SetMimir(ctx, "MAXCONFIRMATIONS-LTC", 6)

	return nil
}
