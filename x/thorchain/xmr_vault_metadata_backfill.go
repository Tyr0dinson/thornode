package thorchain

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type xmrVaultMetadataBackfillStats struct {
	votersScanned  int
	vaultsTouched  int
	keyImagesAdded int
	txKeyRefsAdded int
	txKeysAdded    int
}

func backfillXMRVaultMetadata(ctx cosmos.Context, mgr Manager) (xmrVaultMetadataBackfillStats, error) {
	stats := xmrVaultMetadataBackfillStats{}
	if mgr == nil {
		return stats, nil
	}

	k := mgr.Keeper()
	if k == nil {
		return stats, nil
	}

	vaultCache := make(map[string]*Vault)
	touchedVaults := make(map[string]struct{})

	processVoter := func(voter ObservedTxVoter) error {
		stats.votersScanned++
		return applyObservedVoterXMRMetadataBackfill(ctx, k, voter, vaultCache, touchedVaults, &stats)
	}

	inIter := k.GetObservedTxInVoterIterator(ctx)
	defer inIter.Close()
	for ; inIter.Valid(); inIter.Next() {
		txID, err := observedTxIDFromIteratorKey(inIter.Key())
		if err != nil {
			return stats, err
		}
		voter, err := k.GetObservedTxInVoter(ctx, txID)
		if err != nil {
			return stats, err
		}
		if err := processVoter(voter); err != nil {
			return stats, err
		}
	}

	outIter := k.GetObservedTxOutVoterIterator(ctx)
	defer outIter.Close()
	for ; outIter.Valid(); outIter.Next() {
		txID, err := observedTxIDFromIteratorKey(outIter.Key())
		if err != nil {
			return stats, err
		}
		voter, err := k.GetObservedTxOutVoter(ctx, txID)
		if err != nil {
			return stats, err
		}
		if err := processVoter(voter); err != nil {
			return stats, err
		}
	}

	if len(touchedVaults) == 0 {
		return stats, nil
	}

	keys := make([]string, 0, len(touchedVaults))
	for key := range touchedVaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		vault := vaultCache[key]
		if vault == nil {
			continue
		}
		if err := k.SetVault(ctx, *vault); err != nil {
			return stats, fmt.Errorf("fail to save XMR metadata backfill vault %s: %w", key, err)
		}
	}

	stats.vaultsTouched = len(keys)
	return stats, nil
}

func persistObservedVoterXMRMetadata(ctx cosmos.Context, k keeper.Keeper, voter ObservedTxVoter) error {
	if k == nil {
		return nil
	}

	vaultCache := make(map[string]*Vault)
	touchedVaults := make(map[string]struct{})
	if err := applyObservedVoterXMRMetadataBackfill(
		ctx,
		k,
		voter,
		vaultCache,
		touchedVaults,
		&xmrVaultMetadataBackfillStats{},
	); err != nil {
		return err
	}

	for key := range touchedVaults {
		vault := vaultCache[key]
		if vault == nil {
			continue
		}
		if err := k.SetVault(ctx, *vault); err != nil {
			return fmt.Errorf("fail to save XMR metadata backfill vault %s: %w", vault.PubKey.String(), err)
		}
	}

	return nil
}

func applyObservedVoterXMRMetadataBackfill(
	ctx cosmos.Context,
	k keeper.Keeper,
	voter ObservedTxVoter,
	vaultCache map[string]*Vault,
	touchedVaults map[string]struct{},
	stats *xmrVaultMetadataBackfillStats,
) error {
	candidates := make([]common.ObservedTx, 0, len(voter.Txs)+1)
	if !voter.Tx.IsEmpty() {
		candidates = append(candidates, voter.Tx)
	}
	candidates = append(candidates, voter.Txs...)

	var target *common.ObservedTx
	hasFinalised := false
	for i := range candidates {
		observedTx := candidates[i]
		if observedTx.IsEmpty() || !observedTx.Tx.Chain.Equals(common.XMRChain) {
			continue
		}
		if target == nil {
			target = &candidates[i]
		}
		if observedTx.IsFinal() {
			hasFinalised = true
			target = &candidates[i]
		}
	}
	if !hasFinalised || target == nil {
		return nil
	}

	observedTx := *target
	txKeys, keyImages := collectObservedVoterXMRMetadata(voter, observedTx)
	if len(txKeys) == 0 && len(keyImages) == 0 {
		return nil
	}

	vault, err := loadCachedVaultForBackfill(ctx, k, vaultCache, observedTx.ObservedPubKey)
	if err != nil {
		return err
	}
	if vault == nil || !vault.IsAsgard() {
		return nil
	}

	beforeKeyImageCount := len(vault.KeyImagesXmr)
	beforeTxKeyRefs, beforeTxKeyCount := countVaultXMRTxKeys(*vault)

	if len(txKeys) > 0 {
		ownsTxKeys, ownsErr := vaultOwnsXMRTxKeys(*vault, observedTx)
		if ownsErr != nil {
			ctx.Logger().Error("fail to determine XMR tx-key vault owner during backfill", "error", ownsErr, "txid", observedTx.Tx.ID.String(), "vault_pubkey", observedTx.ObservedPubKey.String())
		} else if ownsTxKeys {
			appendVaultXMRTxKeys(vault, observedTx.Tx.ID.String(), txKeys)
		} else if removeVaultXMRTxKeys(vault, observedTx.Tx.ID.String()) {
			ctx.Logger().Info("removed XMR tx-keys from non-spending vault during backfill", "txid", observedTx.Tx.ID.String(), "vault_pubkey", observedTx.ObservedPubKey.String())
		}
	}
	if len(keyImages) > 0 {
		appendVaultXMRKeyImages(vault, keyImages)
	}

	afterTxKeyRefs, afterTxKeyCount := countVaultXMRTxKeys(*vault)
	afterKeyImageCount := len(vault.KeyImagesXmr)

	if afterTxKeyRefs == beforeTxKeyRefs && afterTxKeyCount == beforeTxKeyCount && afterKeyImageCount == beforeKeyImageCount {
		return nil
	}

	vaultKey := observedTx.ObservedPubKey.String()
	touchedVaults[vaultKey] = struct{}{}
	stats.keyImagesAdded += afterKeyImageCount - beforeKeyImageCount
	stats.txKeyRefsAdded += afterTxKeyRefs - beforeTxKeyRefs
	stats.txKeysAdded += afterTxKeyCount - beforeTxKeyCount

	ctx.Logger().Info(
		"backfilled missing XMR vault metadata from observed voter",
		"vault_pubkey", vaultKey,
		"txid", observedTx.Tx.ID.String(),
		"added_key_images", afterKeyImageCount-beforeKeyImageCount,
		"added_tx_key_refs", afterTxKeyRefs-beforeTxKeyRefs,
		"added_tx_keys", afterTxKeyCount-beforeTxKeyCount,
	)

	return nil
}

func loadCachedVaultForBackfill(ctx cosmos.Context, k keeper.Keeper, cache map[string]*Vault, pubKey common.PubKey) (*Vault, error) {
	if pubKey.IsEmpty() {
		return nil, nil
	}

	key := pubKey.String()
	if vault, ok := cache[key]; ok {
		return vault, nil
	}

	vault, err := k.GetVault(ctx, pubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault %s for XMR metadata backfill: %w", key, err)
	}
	cache[key] = &vault
	return cache[key], nil
}

func countVaultXMRTxKeys(vault Vault) (refs int, keys int) {
	refs = len(vault.TxKeysXmr)
	for _, ref := range vault.TxKeysXmr {
		if ref == nil {
			continue
		}
		keys += len(ref.TxKeys)
	}
	return refs, keys
}

func observedTxIDFromIteratorKey(key []byte) (common.TxID, error) {
	parts := strings.Split(string(key), "/")
	if len(parts) == 0 {
		return common.TxID(""), fmt.Errorf("unexpected observed tx voter key: %q", string(key))
	}
	txID := strings.TrimSpace(parts[len(parts)-1])
	if txID == "" {
		return common.TxID(""), fmt.Errorf("empty observed tx voter key: %q", string(key))
	}
	return common.NewTxID(txID)
}
