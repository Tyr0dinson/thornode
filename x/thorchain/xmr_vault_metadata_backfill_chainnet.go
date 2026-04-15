//go:build chainnet
// +build chainnet

package thorchain

import "gitlab.com/thorchain/thornode/v3/common/cosmos"

const xmrVaultMetadataBackfillMimirKey = "XMRVaultMetadataBackfillV1"

func runChainnetXMRVaultMetadataBackfillOnce(ctx cosmos.Context, mgr Manager) error {
	if mgr == nil || mgr.Keeper() == nil {
		return nil
	}

	doneAt, err := mgr.Keeper().GetMimir(ctx, xmrVaultMetadataBackfillMimirKey)
	if err == nil && doneAt > 0 {
		return nil
	}

	stats, err := backfillXMRVaultMetadata(ctx, mgr)
	if err != nil {
		return err
	}

	mgr.Keeper().SetMimir(ctx, xmrVaultMetadataBackfillMimirKey, ctx.BlockHeight())
	ctx.Logger().Info(
		"completed one-time chainnet XMR vault metadata backfill",
		"mimir_key", xmrVaultMetadataBackfillMimirKey,
		"voters_scanned", stats.votersScanned,
		"vaults_touched", stats.vaultsTouched,
		"key_images_added", stats.keyImagesAdded,
		"tx_key_refs_added", stats.txKeyRefsAdded,
		"tx_keys_added", stats.txKeysAdded,
	)
	return nil
}
