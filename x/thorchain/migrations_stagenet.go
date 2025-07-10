//go:build stagenet
// +build stagenet

package thorchain

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	v2 "gitlab.com/thorchain/thornode/v3/x/thorchain/migrations/v2"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
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
	return nil
}

// Migrate3to4 migrates from version 3 to 4 - adds BASE chain and router to currently
// active vault.
func (m Migrator) Migrate3to4(ctx sdk.Context) error {
	// active vault missing base
	vaultPubkey, err := common.NewPubKey("sthorpub1addwnpepqw354sg3dj8xffqjyznnmqh7rzfv528vr5wdsh3644485hzll6a2zf203v6")
	if err != nil {
		return fmt.Errorf("fail to parse vault pubkey: %w", err)
	}

	// base chain router
	baseRouterAddr, err := common.NewAddress("0xe36dcbf3c0284f756935811d9b9e80829d39bdc5")
	if err != nil {
		return fmt.Errorf("fail to parse base router address: %w", err)
	}

	// get vault
	vault, err := m.mgr.Keeper().GetVault(ctx, vaultPubkey)
	if err != nil {
		return fmt.Errorf("fail to get vault: %w", err)
	}

	// add base chain and router
	vault.Chains = append(vault.Chains, common.BASEChain.String())
	vault.Routers = append(vault.Routers, types.ChainContract{
		Chain:  common.BASEChain,
		Router: baseRouterAddr,
	})

	// store updated vault
	err = m.mgr.Keeper().SetVault(ctx, vault)
	if err != nil {
		return fmt.Errorf("fail to set vault: %w", err)
	}

	return nil
}

// Migrate3to4 migrates from version 4 to 5
func (m Migrator) Migrate4to5(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	totalTCYCoin := common.NewCoin(common.TCY, cosmos.NewUint(210_000_000_00000000))
	err := m.mgr.Keeper().MintToModule(ctx, ModuleName, totalTCYCoin)
	if err != nil {
		return err
	}

	// Claims 1_800_000_00000000
	claimingModuleCoin := common.NewCoin(common.TCY, totalTCYCoin.Amount.Sub(cosmos.NewUint(1_800_000_00000000)))
	err = m.mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(claimingModuleCoin))
	if err != nil {
		return err
	}

	// 210M minus claims: 1_800_000_00000000
	treasuryCoin := common.NewCoin(common.TCY, totalTCYCoin.Amount.Sub(claimingModuleCoin.Amount))
	treasuryAddress, err := common.NewAddress("sthor1hjpct8pd9d48vyqltaqunltwx9twm57l9e8tjr")
	if err != nil {
		return err
	}

	treasuryAccAddress, err := treasuryAddress.AccAddress()
	if err != nil {
		return err
	}

	err = m.mgr.Keeper().SendFromModuleToAccount(ctx, TCYClaimingName, treasuryAccAddress, common.NewCoins(treasuryCoin))
	if err != nil {
		return err
	}

	err = setTCYClaims(ctx, m.mgr)
	if err != nil {
		return err
	}

	return m.ClearObsoleteMimirs(ctx)
}

// Migrate5to6 migrates from version 5 to 6.
func (m Migrator) Migrate5to6(ctx sdk.Context) error {
	return nil
}

// Migrate6to7 migrates from version 6 to 7.
func (m Migrator) Migrate6to7(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ ERC20 Stuck Fund Recovery ------------------------------

	ctx.Logger().Info("Starting ERC20 stuck fund recovery migration")

	erc20Count := 0
	for i, erc20Recovery := range migration6to7erc20s {
		asset, err := common.NewAsset(erc20Recovery.asset)
		if err != nil {
			ctx.Logger().Error("invalid asset in ERC20 recovery",
				"error", err,
				"asset", erc20Recovery.asset,
				"index", i)
			continue
		}

		erc20Count++
		ctx.Logger().Info("Processing ERC20 stuck fund recovery",
			"index", i,
			"original_tx", erc20Recovery.originalTxHash,
			"user_address", erc20Recovery.userAddress,
			"recovery_address", erc20Recovery.recoveryAddress,
			"asset", erc20Recovery.asset,
			"amount", erc20Recovery.amount,
			"description", erc20Recovery.description)

		// Create the coin
		amount := cosmos.NewUintFromString(erc20Recovery.amount)
		coin := common.NewCoin(asset, amount)

		// Use original transaction hash as the fake inbound TX ID for audit trail
		fakeInTxID := erc20Recovery.originalTxHash

		// Schedule refund outbound to 9R recovery address using current active vault
		if err := unsafeAddRefundOutbound(ctx, m.mgr, fakeInTxID, erc20Recovery.recoveryAddress, coin, ctx.BlockHeight()); err != nil {
			ctx.Logger().Error("failed to schedule ERC20 stuck fund recovery",
				"error", err,
				"original_tx", erc20Recovery.originalTxHash,
				"user_address", erc20Recovery.userAddress,
				"recovery_address", erc20Recovery.recoveryAddress,
				"coin", coin.String(),
				"index", i)
			return fmt.Errorf("failed to recover ERC20 stuck funds for tx %s: %w", erc20Recovery.originalTxHash, err)
		}

		ctx.Logger().Info("Successfully scheduled ERC20 stuck fund recovery",
			"original_tx", erc20Recovery.originalTxHash,
			"user_address", erc20Recovery.userAddress,
			"recovery_address", erc20Recovery.recoveryAddress,
			"coin", coin.String(),
			"fake_in_tx", fakeInTxID)
	}

	ctx.Logger().Info("Completed ERC20 stuck fund recovery migration",
		"total_erc20_recovered", erc20Count)

	return nil
}
