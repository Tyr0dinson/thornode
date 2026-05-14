//go:build chainnet
// +build chainnet

package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const xmrPoolBalanceReconcileMimirKey = "ChainnetXMRVaultPoolBalanceReconcile22360"

var chainnetXMRVaultBalanceTargets = map[common.PubKey]cosmos.Uint{
	common.PubKey("cthorpub1zcjduepqh5p72qfjl9rv4twt93lqlsddx4gp8pe2jl9mq033qsf76n98p25szzc9z2"): cosmos.NewUint(159549601),
	common.PubKey("cthorpub1zcjduepq5yf9tavdaru7rs5aaypppgn8hl3vflrw7nsutullerlmjx6vngasv8vhw3"): cosmos.NewUint(155614641),
}

type xmrPoolBalanceReconcileStats struct {
	activeVaults int
	oldPool      cosmos.Uint
	newPool      cosmos.Uint
	vaultChanges int
	changed      bool
}

func runChainnetXMRPoolBalanceReconcileOnce(ctx cosmos.Context, mgr Manager) error {
	if mgr == nil || mgr.Keeper() == nil {
		return nil
	}

	doneAt, err := mgr.Keeper().GetMimir(ctx, xmrPoolBalanceReconcileMimirKey)
	if err == nil && doneAt > 0 {
		return nil
	}

	stats, err := reconcileChainnetXMRPoolBalance(ctx, mgr)
	if err != nil {
		return err
	}

	mgr.Keeper().SetMimir(ctx, xmrPoolBalanceReconcileMimirKey, ctx.BlockHeight())
	ctx.Logger().Info(
		"completed one-time chainnet XMR pool balance reconcile",
		"mimir_key", xmrPoolBalanceReconcileMimirKey,
		"active_vaults", stats.activeVaults,
		"old_pool_balance_asset", stats.oldPool.String(),
		"new_pool_balance_asset", stats.newPool.String(),
		"vault_changes", stats.vaultChanges,
		"changed", stats.changed,
	)
	return nil
}

func reconcileChainnetXMRPoolBalance(ctx cosmos.Context, mgr Manager) (xmrPoolBalanceReconcileStats, error) {
	stats := xmrPoolBalanceReconcileStats{
		oldPool: cosmos.ZeroUint(),
		newPool: cosmos.ZeroUint(),
	}

	pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	if err != nil {
		return stats, err
	}
	stats.oldPool = pool.BalanceAsset

	vaults, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return stats, err
	}
	for i := range vaults {
		vault := vaults[i]
		target, ok := chainnetXMRVaultBalanceTargets[vault.PubKeyFrost]
		if !ok {
			continue
		}

		stats.activeVaults++
		stats.newPool = stats.newPool.Add(target)

		current := vault.GetCoin(common.XMRAsset).Amount
		if current.Equal(target) {
			continue
		}

		vault.SubFunds(common.NewCoins(common.NewCoin(common.XMRAsset, current)))
		vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, target)))
		if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
			return stats, err
		}
		stats.vaultChanges++
		stats.changed = true
	}

	if stats.activeVaults != len(chainnetXMRVaultBalanceTargets) {
		ctx.Logger().Info(
			"chainnet XMR vault pool reconcile did not find every target active vault",
			"found_active_targets", stats.activeVaults,
			"expected_active_targets", len(chainnetXMRVaultBalanceTargets),
		)
	}

	if pool.BalanceAsset.Equal(stats.newPool) {
		return stats, nil
	}

	pool.BalanceAsset = stats.newPool
	if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
		return stats, err
	}
	stats.changed = true
	return stats, nil
}
