//go:build chainnet
// +build chainnet

package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	chainnetXMRD26MigrateRepairMimirKey = "ChainnetXMRD26MigrateRepair"
	chainnetXMRD26MigrateTxID           = "D26DFB9FA3040AD07D695A4415B2B26A61BD7E4724729BBAD63681FB0A28ECFA"
	chainnetXMRD26SourceVault           = "cthorpub1zcjduepqg2800hkdq7z8fjp6j386s70wj6j7mz2mclz42pe9crantvlrpr6q0vdrs8"
	chainnetXMRD26QueuedAmount          = 49981460
)

type chainnetXMRD26MigrateRepairStats struct {
	oldSourceVaultBalance cosmos.Uint
	newSourceVaultBalance cosmos.Uint
	oldPoolBalance        cosmos.Uint
	newPoolBalance        cosmos.Uint
	txOutItemsRemoved     int
	txOutBlocksTouched    int
	inFlightCleared       int
}

func runChainnetXMRD26MigrateRepairOnce(ctx cosmos.Context, mgr Manager) error {
	if mgr == nil || mgr.Keeper() == nil {
		return nil
	}

	doneAt, err := mgr.Keeper().GetMimir(ctx, chainnetXMRD26MigrateRepairMimirKey)
	if err == nil && doneAt > 0 {
		return nil
	}

	stats, err := repairChainnetXMRD26Migrate(ctx, mgr)
	if err != nil {
		return err
	}

	mgr.Keeper().SetMimir(ctx, chainnetXMRD26MigrateRepairMimirKey, ctx.BlockHeight())
	ctx.Logger().Info(
		"completed one-time chainnet XMR D26 migrate repair",
		"mimir_key", chainnetXMRD26MigrateRepairMimirKey,
		"old_source_vault_balance", stats.oldSourceVaultBalance.String(),
		"new_source_vault_balance", stats.newSourceVaultBalance.String(),
		"old_pool_balance", stats.oldPoolBalance.String(),
		"new_pool_balance", stats.newPoolBalance.String(),
		"txout_items_removed", stats.txOutItemsRemoved,
		"txout_blocks_touched", stats.txOutBlocksTouched,
		"in_flight_cleared", stats.inFlightCleared,
	)
	return nil
}

func repairChainnetXMRD26Migrate(ctx cosmos.Context, mgr Manager) (chainnetXMRD26MigrateRepairStats, error) {
	stats := chainnetXMRD26MigrateRepairStats{}

	sourceVaultPubKey := common.PubKey(chainnetXMRD26SourceVault)
	sourceVault, err := mgr.Keeper().GetVault(ctx, sourceVaultPubKey)
	if err != nil {
		return stats, err
	}
	stats.oldSourceVaultBalance = sourceVault.GetCoin(common.XMRAsset).Amount
	if !stats.oldSourceVaultBalance.IsZero() {
		sourceVault.SubFunds(common.NewCoins(common.NewCoin(common.XMRAsset, stats.oldSourceVaultBalance)))
		if err := mgr.Keeper().SetVault(ctx, sourceVault); err != nil {
			return stats, err
		}
	}
	stats.newSourceVaultBalance = sourceVault.GetCoin(common.XMRAsset).Amount

	pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	if err != nil {
		return stats, err
	}
	stats.oldPoolBalance = pool.BalanceAsset
	activeVaultTotal, err := chainnetXMRActiveVaultAssetTotal(ctx, mgr)
	if err != nil {
		return stats, err
	}
	if !pool.BalanceAsset.Equal(activeVaultTotal) {
		pool.BalanceAsset = activeVaultTotal
		if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
			return stats, err
		}
	}
	stats.newPoolBalance = pool.BalanceAsset

	removed, touched, err := clearChainnetXMRD26MigrateTxOuts(ctx, mgr)
	if err != nil {
		return stats, err
	}
	stats.txOutItemsRemoved = removed
	stats.txOutBlocksTouched = touched

	cleared, err := clearChainnetXMRD26InFlightMigrate(ctx, mgr)
	if err != nil {
		return stats, err
	}
	stats.inFlightCleared = cleared

	return stats, nil
}

func chainnetXMRActiveVaultAssetTotal(ctx cosmos.Context, mgr Manager) (cosmos.Uint, error) {
	vaults, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	total := cosmos.ZeroUint()
	for i := range vaults {
		total = total.Add(vaults[i].GetCoin(common.XMRAsset).Amount)
	}
	return total, nil
}

func clearChainnetXMRD26MigrateTxOuts(ctx cosmos.Context, mgr Manager) (int, int, error) {
	removed := 0
	touched := 0
	iter := mgr.Keeper().GetTxOutIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var txOut TxOut
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &txOut); err != nil {
			return removed, touched, err
		}

		filtered := txOut.TxArray[:0]
		blockRemoved := 0
		for _, item := range txOut.TxArray {
			if chainnetXMRD26MigrateTxOutItem(item) {
				blockRemoved++
				continue
			}
			filtered = append(filtered, item)
		}
		if blockRemoved == 0 {
			continue
		}

		txOut.TxArray = filtered
		if len(txOut.TxArray) == 0 {
			if err := mgr.Keeper().ClearTxOut(ctx, txOut.Height); err != nil {
				return removed, touched, err
			}
		} else if err := mgr.Keeper().SetTxOut(ctx, &txOut); err != nil {
			return removed, touched, err
		}
		removed += blockRemoved
		touched++
		ctx.Logger().Info(
			"removed stale chainnet XMR D26 migrate txout item",
			"height", txOut.Height,
			"removed", blockRemoved,
		)
	}
	return removed, touched, nil
}

func chainnetXMRD26MigrateTxOutItem(item TxOutItem) bool {
	if !item.Chain.Equals(common.XMRChain) {
		return false
	}
	if item.VaultPubKeyFrost.String() != chainnetXMRD26SourceVault {
		return false
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(item.Memo)), "MIGRATE:") {
		return false
	}
	if !item.Coin.Asset.Equals(common.XMRAsset) {
		return false
	}
	return item.Coin.Amount.Equal(cosmos.NewUint(chainnetXMRD26QueuedAmount))
}

func clearChainnetXMRD26InFlightMigrate(ctx cosmos.Context, mgr Manager) (int, error) {
	cleared := 0
	iter := mgr.Keeper().GetInFlightMigrateIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		txID := inFlightMigrateTxIDFromKey(string(iter.Key()))
		if txID != chainnetXMRD26MigrateTxID {
			continue
		}

		var source types.ProtoString
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &source); err != nil {
			return cleared, err
		}
		if source.GetValue() != chainnetXMRD26SourceVault {
			ctx.Logger().Info(
				"skipping chainnet XMR D26 in-flight migrate clear due to source mismatch",
				"tx_id", txID,
				"expected_source", chainnetXMRD26SourceVault,
				"actual_source", source.GetValue(),
			)
			continue
		}
		mgr.Keeper().DeleteKey(ctx, iter.Key())
		cleared++
	}
	return cleared, nil
}
