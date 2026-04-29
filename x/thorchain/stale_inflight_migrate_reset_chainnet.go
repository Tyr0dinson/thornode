//go:build chainnet
// +build chainnet

package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const chainnetStaleInFlightMigrateResetMimirKey = "ChainnetStaleInFlightMigrateReset15333"

type chainnetStaleInFlightMigrateResetTarget struct {
	txID        string
	sourceVault string
}

var chainnetStaleInFlightMigrateResetTargets = []chainnetStaleInFlightMigrateResetTarget{
	{
		txID:        "053BB7D8E9D54F96BEAC9C1723A88BD84876BEA5CCF2F876ABE0C5623263DF94",
		sourceVault: "cthorpub1zcjduepq74v3n5e4878ht2mxkpmhstwl0ak0luakwfz9g7f7cddl85sggz2qa8t6cw",
	},
	{
		txID:        "B2BF22EA4728ABBBE0531E514253BA33A53F76DF8428B9D1BF418283FE1E17AC",
		sourceVault: "cthorpub1zcjduepqlhhakuukuuy367w6akuz6ps486z6n0edzycj8s7zfglyp85927nsh78ctu",
	},
	{
		txID:        "E0E260B448817EA31381BA5A9D35D1B4AECE6B86F282F0CF464EA5749515CD5B",
		sourceVault: "cthorpub1zcjduepq80lqay5jq7dpzryuv2ga58mzqfwh8cm4dyz28jp5v87c4sx7ap6s08qeg6",
	},
}

func runChainnetStaleInFlightMigrateResetOnce(ctx cosmos.Context, mgr Manager) error {
	if mgr == nil || mgr.Keeper() == nil {
		return nil
	}

	doneAt, err := mgr.Keeper().GetMimir(ctx, chainnetStaleInFlightMigrateResetMimirKey)
	if err == nil && doneAt > 0 {
		return nil
	}

	cleared, skipped, missing, err := clearChainnetStaleInFlightMigrates(ctx, mgr)
	if err != nil {
		return err
	}

	mgr.Keeper().SetMimir(ctx, chainnetStaleInFlightMigrateResetMimirKey, ctx.BlockHeight())
	ctx.Logger().Info(
		"completed one-time chainnet stale in-flight migrate reset",
		"mimir_key", chainnetStaleInFlightMigrateResetMimirKey,
		"cleared", cleared,
		"skipped", skipped,
		"missing", missing,
	)
	return nil
}

func clearChainnetStaleInFlightMigrates(ctx cosmos.Context, mgr Manager) (int, int, int, error) {
	targets := make(map[string]string, len(chainnetStaleInFlightMigrateResetTargets))
	seen := make(map[string]bool, len(chainnetStaleInFlightMigrateResetTargets))
	for _, target := range chainnetStaleInFlightMigrateResetTargets {
		targets[target.txID] = target.sourceVault
	}

	cleared := 0
	skipped := 0
	iter := mgr.Keeper().GetInFlightMigrateIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		txID := inFlightMigrateTxIDFromKey(string(iter.Key()))
		expectedSource, ok := targets[txID]
		if !ok {
			continue
		}
		seen[txID] = true

		var source types.ProtoString
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &source); err != nil {
			return cleared, skipped, 0, err
		}
		if source.GetValue() != expectedSource {
			skipped++
			ctx.Logger().Info(
				"skipping stale in-flight migrate reset due to source mismatch",
				"tx_id", txID,
				"expected_source", expectedSource,
				"actual_source", source.GetValue(),
			)
			continue
		}

		mgr.Keeper().DeleteKey(ctx, iter.Key())
		cleared++
		ctx.Logger().Info(
			"cleared stale in-flight migrate index",
			"tx_id", txID,
			"source_vault", source.GetValue(),
		)
	}

	missing := 0
	for txID := range targets {
		if !seen[txID] {
			missing++
		}
	}
	return cleared, skipped, missing, nil
}

func inFlightMigrateTxIDFromKey(key string) string {
	txID := strings.TrimPrefix(key, "in_flight_migrate/")
	return strings.TrimLeft(txID, "/")
}
