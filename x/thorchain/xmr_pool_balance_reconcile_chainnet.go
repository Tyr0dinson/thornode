//go:build chainnet
// +build chainnet

package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const xmrPoolBalanceReconcileMimirKey = "ChainnetXMRPoolBalanceReconcile4818"

type xmrPoolBalanceReconcileStats struct {
	activeVaults int
	oldBalance   cosmos.Uint
	newBalance   cosmos.Uint
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
		"old_balance_asset", stats.oldBalance.String(),
		"new_balance_asset", stats.newBalance.String(),
		"changed", stats.changed,
	)
	return nil
}

func reconcileChainnetXMRPoolBalance(ctx cosmos.Context, mgr Manager) (xmrPoolBalanceReconcileStats, error) {
	stats := xmrPoolBalanceReconcileStats{
		oldBalance: cosmos.ZeroUint(),
		newBalance: cosmos.ZeroUint(),
	}

	pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	if err != nil {
		return stats, err
	}
	stats.oldBalance = pool.BalanceAsset

	vaults, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return stats, err
	}
	for _, vault := range vaults {
		if !vault.HasFunds() {
			continue
		}
		xmrCoin := vault.GetCoin(common.XMRAsset)
		if xmrCoin.IsEmpty() {
			continue
		}
		stats.activeVaults++
		stats.newBalance = stats.newBalance.Add(xmrCoin.Amount)
	}

	if pool.BalanceAsset.Equal(stats.newBalance) {
		return stats, nil
	}

	pool.BalanceAsset = stats.newBalance
	if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
		return stats, err
	}
	stats.changed = true
	return stats, nil
}
