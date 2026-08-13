package keeperv1

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const mempoolSwapHeightWidth = 20

func sortedMempoolSwapMapUnionKeys[V any](first, second map[string]V) []string {
	keys := append(slices.Sorted(maps.Keys(first)), slices.Sorted(maps.Keys(second))...)
	slices.Sort(keys)
	return slices.Compact(keys)
}

func mempoolSwapIdentity(txID common.TxID, index uint32) string {
	return MempoolSwap{TxId: txID, Index: index}.Cursor()
}

func mempoolSwapTimeoutIdentity(height int64, txID common.TxID, index uint32) string {
	return fmt.Sprintf("%0*d/%s", mempoolSwapHeightWidth, height, mempoolSwapIdentity(txID, index))
}

func (k KVStore) mempoolSwapKey(txID common.TxID, index uint32) []byte {
	return k.GetKey(prefixMempoolSwap, mempoolSwapIdentity(txID, index))
}

// GetMempoolSwapCanonicalKey returns the only physical primary-store key valid
// for the supplied lifecycle identity. Export uses it to fail closed rather
// than silently normalizing corrupt physical keys through genesis.
func (k KVStore) GetMempoolSwapCanonicalKey(txID common.TxID, index uint32) []byte {
	return k.mempoolSwapKey(txID, index)
}

func (k KVStore) mempoolSwapVaultIndexKey(record MempoolSwap) []byte {
	return k.GetKey(prefixMempoolSwapVault, fmt.Sprintf("%s/%s", record.VaultPubKey, mempoolSwapIdentity(record.TxId, record.Index)))
}

func (k KVStore) mempoolSwapTimeoutIndexKey(record MempoolSwap) []byte {
	return k.GetKey(prefixMempoolSwapTimeout, mempoolSwapTimeoutIdentity(record.NextRecoveryHeight, record.TxId, record.Index))
}

func newMempoolSwapPoolBalanceOverlay(asset common.Asset) MempoolSwapPoolBalanceOverlay {
	return MempoolSwapPoolBalanceOverlay{
		Asset:                asset.GetLayer1Asset(),
		BalanceRuneIncrease:  cosmos.ZeroUint(),
		BalanceRuneDecrease:  cosmos.ZeroUint(),
		BalanceAssetIncrease: cosmos.ZeroUint(),
		BalanceAssetDecrease: cosmos.ZeroUint(),
	}
}

func mempoolSwapPoolBalanceOverlayMapKey(asset common.Asset) string {
	return strings.ToUpper(asset.GetLayer1Asset().String())
}

func (k KVStore) mempoolSwapPoolOwnerKey(asset common.Asset) []byte {
	return k.GetKey(prefixMempoolSwapPoolOwner, mempoolSwapPoolBalanceOverlayMapKey(asset))
}

func (k KVStore) mempoolSwapPoolBalanceOverlayKey(asset common.Asset) []byte {
	return k.GetKey(prefixMempoolSwapPoolOverlay, mempoolSwapPoolBalanceOverlayMapKey(asset))
}

func mempoolSwapRecordPoolBalanceOverlays(record *MempoolSwap) map[string]MempoolSwapPoolBalanceOverlay {
	overlays := make(map[string]MempoolSwapPoolBalanceOverlay)
	if record == nil || record.Status == types.MempoolSwapStatus_FINALIZED {
		return overlays
	}
	for _, effect := range record.PendingEffects.Pools {
		asset := effect.Asset.GetLayer1Asset()
		key := mempoolSwapPoolBalanceOverlayMapKey(asset)
		overlay, ok := overlays[key]
		if !ok {
			overlay = newMempoolSwapPoolBalanceOverlay(asset)
		}
		overlay.BalanceRuneIncrease = overlay.BalanceRuneIncrease.Add(effect.BalanceRuneIncrease)
		overlay.BalanceRuneDecrease = overlay.BalanceRuneDecrease.Add(effect.BalanceRuneDecrease)
		overlay.BalanceAssetIncrease = overlay.BalanceAssetIncrease.Add(effect.BalanceAssetIncrease)
		overlay.BalanceAssetDecrease = overlay.BalanceAssetDecrease.Add(effect.BalanceAssetDecrease)
		overlays[key] = overlay
	}
	return overlays
}

func subtractMempoolSwapOverlayAmount(total, amount cosmos.Uint, asset common.Asset, field string) (cosmos.Uint, error) {
	if amount.GT(total) {
		return cosmos.ZeroUint(), fmt.Errorf(
			"mempool swap pool overlay underflow for %s %s: subtract %s from %s",
			asset, field, amount, total,
		)
	}
	return total.Sub(amount), nil
}

func mempoolSwapPoolBalanceOverlayEmpty(overlay MempoolSwapPoolBalanceOverlay) bool {
	return overlay.BalanceRuneIncrease.IsZero() &&
		overlay.BalanceRuneDecrease.IsZero() &&
		overlay.BalanceAssetIncrease.IsZero() &&
		overlay.BalanceAssetDecrease.IsZero()
}

func mempoolSwapPoolBalanceOverlayEqual(a, b MempoolSwapPoolBalanceOverlay) bool {
	amounts := []cosmos.Uint{
		a.BalanceRuneIncrease,
		a.BalanceRuneDecrease,
		a.BalanceAssetIncrease,
		a.BalanceAssetDecrease,
		b.BalanceRuneIncrease,
		b.BalanceRuneDecrease,
		b.BalanceAssetIncrease,
		b.BalanceAssetDecrease,
	}
	for _, amount := range amounts {
		if amount.IsNil() {
			return false
		}
	}
	return a.Asset.Equals(b.Asset) &&
		a.BalanceRuneIncrease.Equal(b.BalanceRuneIncrease) &&
		a.BalanceRuneDecrease.Equal(b.BalanceRuneDecrease) &&
		a.BalanceAssetIncrease.Equal(b.BalanceAssetIncrease) &&
		a.BalanceAssetDecrease.Equal(b.BalanceAssetDecrease)
}

// mempoolSwapRecordPoolOwnerAssets returns every L1 pool for which a lifecycle
// has recorded reversible balance or synth-supply state. Unlike the active
// aggregate overlays, ownership deliberately survives FINALIZED: the
// zero-overlay lifecycle tombstone must keep speculative successors out until
// the lifecycle is terminally deleted.
//
// Ownership is derived from gross non-zero effects rather than their net so
// opposing effects can never make the index disappear.
func mempoolSwapRecordPoolOwnerAssets(record *MempoolSwap) map[string]common.Asset {
	assets := make(map[string]common.Asset)
	if record == nil {
		return assets
	}
	for _, effect := range record.PendingEffects.Pools {
		if effect.BalanceRuneIncrease.IsZero() &&
			effect.BalanceRuneDecrease.IsZero() &&
			effect.BalanceAssetIncrease.IsZero() &&
			effect.BalanceAssetDecrease.IsZero() {
			continue
		}
		asset := effect.Asset.GetLayer1Asset()
		assets[mempoolSwapPoolBalanceOverlayMapKey(asset)] = asset
	}
	for _, effect := range record.PendingEffects.Supplies {
		if !effect.Asset.IsSyntheticAsset() ||
			(effect.SupplyIncrease.IsZero() && effect.SupplyDecrease.IsZero()) {
			continue
		}
		asset := effect.Asset.GetLayer1Asset()
		assets[mempoolSwapPoolBalanceOverlayMapKey(asset)] = asset
	}
	return assets
}

// updateMempoolSwapPoolOwners maintains one canonical lifecycle pointer for
// each pool with recorded reversible state. Runtime execution already
// serializes pool mutations; enforcing that contract at the store boundary
// prevents a recovery path from choosing one of several conflicting owners.
func (k KVStore) updateMempoolSwapPoolOwners(ctx cosmos.Context, previous, next *MempoolSwap) error {
	previousAssets := mempoolSwapRecordPoolOwnerAssets(previous)
	nextAssets := mempoolSwapRecordPoolOwnerAssets(next)
	orderedKeys := sortedMempoolSwapMapUnionKeys(previousAssets, nextAssets)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	for _, key := range orderedKeys {
		asset := previousAssets[key]
		if asset.IsEmpty() {
			asset = nextAssets[key]
		}
		indexKey := k.mempoolSwapPoolOwnerKey(asset)
		previousOwns := previous != nil
		if previousOwns {
			_, previousOwns = previousAssets[key]
		}
		nextOwns := next != nil
		if nextOwns {
			_, nextOwns = nextAssets[key]
		}

		if previousOwns {
			expected := k.mempoolSwapKey(previous.TxId, previous.Index)
			if !store.Has(indexKey) || !bytes.Equal(store.Get(indexKey), expected) {
				return fmt.Errorf("mempool swap pool owner index for %s does not point to %s", asset, previous.Cursor())
			}
			if !nextOwns {
				store.Delete(indexKey)
			}
		}

		if nextOwns {
			canonicalKey := k.mempoolSwapKey(next.TxId, next.Index)
			if store.Has(indexKey) && !bytes.Equal(store.Get(indexKey), canonicalKey) {
				return fmt.Errorf("mempool swap pool %s already has a different lifecycle owner", asset)
			}
			store.Set(indexKey, canonicalKey)
		}
	}
	return nil
}

// updateMempoolSwapPoolBalanceOverlays refreshes the per-pool aggregate by the
// difference between two canonical lifecycle values. This keeps Begin/EndBlock
// consumers at one KV lookup per pool instead of an unbounded lifecycle scan.
func (k KVStore) updateMempoolSwapPoolBalanceOverlays(ctx cosmos.Context, previous, next *MempoolSwap) error {
	previousOverlays := mempoolSwapRecordPoolBalanceOverlays(previous)
	nextOverlays := mempoolSwapRecordPoolBalanceOverlays(next)
	orderedKeys := sortedMempoolSwapMapUnionKeys(previousOverlays, nextOverlays)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	for _, key := range orderedKeys {
		asset := previousOverlays[key].Asset
		if asset.IsEmpty() {
			asset = nextOverlays[key].Asset
		}
		overlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, asset)
		if err != nil {
			return err
		}
		if previousOverlay, ok := previousOverlays[key]; ok {
			overlay.BalanceRuneIncrease, err = subtractMempoolSwapOverlayAmount(overlay.BalanceRuneIncrease, previousOverlay.BalanceRuneIncrease, asset, "rune increase")
			if err != nil {
				return err
			}
			overlay.BalanceRuneDecrease, err = subtractMempoolSwapOverlayAmount(overlay.BalanceRuneDecrease, previousOverlay.BalanceRuneDecrease, asset, "rune decrease")
			if err != nil {
				return err
			}
			overlay.BalanceAssetIncrease, err = subtractMempoolSwapOverlayAmount(overlay.BalanceAssetIncrease, previousOverlay.BalanceAssetIncrease, asset, "asset increase")
			if err != nil {
				return err
			}
			overlay.BalanceAssetDecrease, err = subtractMempoolSwapOverlayAmount(overlay.BalanceAssetDecrease, previousOverlay.BalanceAssetDecrease, asset, "asset decrease")
			if err != nil {
				return err
			}
		}
		if nextOverlay, ok := nextOverlays[key]; ok {
			overlay.BalanceRuneIncrease = overlay.BalanceRuneIncrease.Add(nextOverlay.BalanceRuneIncrease)
			overlay.BalanceRuneDecrease = overlay.BalanceRuneDecrease.Add(nextOverlay.BalanceRuneDecrease)
			overlay.BalanceAssetIncrease = overlay.BalanceAssetIncrease.Add(nextOverlay.BalanceAssetIncrease)
			overlay.BalanceAssetDecrease = overlay.BalanceAssetDecrease.Add(nextOverlay.BalanceAssetDecrease)
		}

		overlayKey := k.mempoolSwapPoolBalanceOverlayKey(asset)
		if mempoolSwapPoolBalanceOverlayEmpty(overlay) {
			store.Delete(overlayKey)
			continue
		}
		buf, err := k.cdc.Marshal(&overlay)
		if err != nil {
			return dbError(ctx, "failed to marshal mempool swap pool balance overlay", err)
		}
		store.Set(overlayKey, buf)
	}
	return nil
}

// GetMempoolSwapPoolBalanceOverlay returns the aggregate speculative mutation
// currently applied to one live pool. Missing overlays are represented by a
// fully initialized zero value.
func (k KVStore) GetMempoolSwapPoolBalanceOverlay(ctx cosmos.Context, asset common.Asset) (MempoolSwapPoolBalanceOverlay, error) {
	asset = asset.GetLayer1Asset()
	overlay := newMempoolSwapPoolBalanceOverlay(asset)
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.mempoolSwapPoolBalanceOverlayKey(asset)
	if !store.Has(key) {
		return overlay, nil
	}
	if err := k.cdc.Unmarshal(store.Get(key), &overlay); err != nil {
		return overlay, dbError(ctx, fmt.Sprintf("failed to unmarshal mempool swap pool balance overlay %s", asset), err)
	}
	if !overlay.Asset.Equals(asset) {
		return overlay, fmt.Errorf("mempool swap pool balance overlay key %s contains asset %s", asset, overlay.Asset)
	}
	amounts := []struct {
		name   string
		amount cosmos.Uint
	}{
		{"rune increase", overlay.BalanceRuneIncrease},
		{"rune decrease", overlay.BalanceRuneDecrease},
		{"asset increase", overlay.BalanceAssetIncrease},
		{"asset decrease", overlay.BalanceAssetDecrease},
	}
	for _, item := range amounts {
		if item.amount.IsNil() {
			return overlay, fmt.Errorf("mempool swap pool balance overlay %s has nil %s", asset, item.name)
		}
	}
	return overlay, nil
}

// finalityAdjustedPool removes pool-balance mutations owned by active mempool
// swaps from a local copy. It is for read-side consensus valuation only; live
// pool state remains speculative until the lifecycle finalizes or rolls back.
func (k KVStore) finalityAdjustedPool(ctx cosmos.Context, pool Pool) (Pool, error) {
	overlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, pool.Asset)
	if err != nil {
		return Pool{}, err
	}

	pool.BalanceRune = pool.BalanceRune.Add(overlay.BalanceRuneDecrease)
	if overlay.BalanceRuneIncrease.GT(pool.BalanceRune) {
		return Pool{}, fmt.Errorf(
			"mempool swap rune overlay exceeds live balance for %s: increase %s, live+decrease %s",
			pool.Asset, overlay.BalanceRuneIncrease, pool.BalanceRune,
		)
	}
	pool.BalanceRune = pool.BalanceRune.Sub(overlay.BalanceRuneIncrease)

	pool.BalanceAsset = pool.BalanceAsset.Add(overlay.BalanceAssetDecrease)
	if overlay.BalanceAssetIncrease.GT(pool.BalanceAsset) {
		return Pool{}, fmt.Errorf(
			"mempool swap asset overlay exceeds live balance for %s: increase %s, live+decrease %s",
			pool.Asset, overlay.BalanceAssetIncrease, pool.BalanceAsset,
		)
	}
	pool.BalanceAsset = pool.BalanceAsset.Sub(overlay.BalanceAssetIncrease)

	return pool, nil
}

func newMempoolSwapNativeSupplyOverlay(asset common.Asset) MempoolSwapNativeSupplyOverlay {
	return MempoolSwapNativeSupplyOverlay{
		Asset:          asset,
		SupplyIncrease: cosmos.ZeroUint(),
		SupplyDecrease: cosmos.ZeroUint(),
	}
}

func mempoolSwapNativeSupplyOverlayMapKey(asset common.Asset) string {
	return strings.ToUpper(asset.Native())
}

func (k KVStore) mempoolSwapNativeSupplyOverlayKey(asset common.Asset) []byte {
	return k.GetKey(prefixMempoolSwapSupplyOverlay, mempoolSwapNativeSupplyOverlayMapKey(asset))
}

func mempoolSwapRecordNativeSupplyOverlays(record *MempoolSwap) map[string]MempoolSwapNativeSupplyOverlay {
	overlays := make(map[string]MempoolSwapNativeSupplyOverlay)
	if record == nil || record.Status == types.MempoolSwapStatus_FINALIZED {
		return overlays
	}
	for _, effect := range record.PendingEffects.Supplies {
		key := mempoolSwapNativeSupplyOverlayMapKey(effect.Asset)
		overlay, ok := overlays[key]
		if !ok {
			overlay = newMempoolSwapNativeSupplyOverlay(effect.Asset)
		}
		overlay.SupplyIncrease = overlay.SupplyIncrease.Add(effect.SupplyIncrease)
		overlay.SupplyDecrease = overlay.SupplyDecrease.Add(effect.SupplyDecrease)
		overlays[key] = overlay
	}
	return overlays
}

func mempoolSwapNativeSupplyOverlayEmpty(overlay MempoolSwapNativeSupplyOverlay) bool {
	return overlay.SupplyIncrease.IsZero() && overlay.SupplyDecrease.IsZero()
}

func mempoolSwapNativeSupplyOverlayEqual(a, b MempoolSwapNativeSupplyOverlay) bool {
	if a.SupplyIncrease.IsNil() || a.SupplyDecrease.IsNil() ||
		b.SupplyIncrease.IsNil() || b.SupplyDecrease.IsNil() {
		return false
	}
	return a.Asset.Native() == b.Asset.Native() &&
		a.SupplyIncrease.Equal(b.SupplyIncrease) &&
		a.SupplyDecrease.Equal(b.SupplyDecrease)
}

func subtractMempoolSwapSupplyOverlayAmount(total, amount cosmos.Uint, asset common.Asset, field string) (cosmos.Uint, error) {
	if amount.GT(total) {
		return cosmos.ZeroUint(), fmt.Errorf(
			"mempool swap native supply overlay underflow for %s %s: subtract %s from %s",
			asset.Native(), field, amount, total,
		)
	}
	return total.Sub(amount), nil
}

// updateMempoolSwapNativeSupplyOverlays atomically moves the exact gross bank
// supply contribution between lifecycle states. Finalized records contribute
// nothing; a deep-reorg transition to rollback-pending restores ownership.
func (k KVStore) updateMempoolSwapNativeSupplyOverlays(ctx cosmos.Context, previous, next *MempoolSwap) error {
	previousOverlays := mempoolSwapRecordNativeSupplyOverlays(previous)
	nextOverlays := mempoolSwapRecordNativeSupplyOverlays(next)
	orderedKeys := sortedMempoolSwapMapUnionKeys(previousOverlays, nextOverlays)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	for _, key := range orderedKeys {
		asset := previousOverlays[key].Asset
		if asset.IsEmpty() {
			asset = nextOverlays[key].Asset
		}
		overlay, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, asset)
		if err != nil {
			return err
		}
		if previousOverlay, ok := previousOverlays[key]; ok {
			overlay.SupplyIncrease, err = subtractMempoolSwapSupplyOverlayAmount(
				overlay.SupplyIncrease, previousOverlay.SupplyIncrease, asset, "increase",
			)
			if err != nil {
				return err
			}
			overlay.SupplyDecrease, err = subtractMempoolSwapSupplyOverlayAmount(
				overlay.SupplyDecrease, previousOverlay.SupplyDecrease, asset, "decrease",
			)
			if err != nil {
				return err
			}
		}
		if nextOverlay, ok := nextOverlays[key]; ok {
			overlay.SupplyIncrease = overlay.SupplyIncrease.Add(nextOverlay.SupplyIncrease)
			overlay.SupplyDecrease = overlay.SupplyDecrease.Add(nextOverlay.SupplyDecrease)
		}

		overlayKey := k.mempoolSwapNativeSupplyOverlayKey(asset)
		if mempoolSwapNativeSupplyOverlayEmpty(overlay) {
			store.Delete(overlayKey)
			continue
		}
		buf, err := k.cdc.Marshal(&overlay)
		if err != nil {
			return dbError(ctx, "failed to marshal mempool swap native supply overlay", err)
		}
		store.Set(overlayKey, buf)
	}
	return nil
}

// GetMempoolSwapNativeSupplyOverlay returns the aggregate active lifecycle
// mutation for one native bank denomination. Missing keys return initialized
// zeros; corrupt keys fail closed for irreversible consumers.
func (k KVStore) GetMempoolSwapNativeSupplyOverlay(ctx cosmos.Context, asset common.Asset) (MempoolSwapNativeSupplyOverlay, error) {
	overlay := newMempoolSwapNativeSupplyOverlay(asset)
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.mempoolSwapNativeSupplyOverlayKey(asset)
	if !store.Has(key) {
		return overlay, nil
	}
	if err := k.cdc.Unmarshal(store.Get(key), &overlay); err != nil {
		return overlay, dbError(ctx, fmt.Sprintf("failed to unmarshal mempool swap native supply overlay %s", asset.Native()), err)
	}
	if overlay.Asset.Native() != asset.Native() {
		return overlay, fmt.Errorf("mempool swap native supply overlay key %s contains asset %s", asset.Native(), overlay.Asset)
	}
	if overlay.SupplyIncrease.IsNil() || overlay.SupplyDecrease.IsNil() {
		return overlay, fmt.Errorf("mempool swap native supply overlay %s has an uninitialized amount", asset.Native())
	}
	return overlay, nil
}

// GetMempoolSwapPoolOwner resolves the lifecycle which owns reversible state
// for an L1 pool, including a FINALIZED zero-overlay tombstone retained for
// deep-reorg isolation. Besides following the derived index, it verifies that
// both aggregate overlays exactly match that record's active contribution.
// Missing, stale, or conflicting ownership therefore fails closed before a
// pool consumer can act on the wrong lifecycle.
func (k KVStore) GetMempoolSwapPoolOwner(ctx cosmos.Context, asset common.Asset) (MempoolSwap, bool, error) {
	l1Asset := asset.GetLayer1Asset()
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	indexKey := k.mempoolSwapPoolOwnerKey(l1Asset)
	if !store.Has(indexKey) {
		return MempoolSwap{}, false, nil
	}

	canonicalKey := store.Get(indexKey)
	record, err := k.getMempoolSwapFromIndex(ctx, canonicalKey)
	if err != nil {
		return MempoolSwap{}, true, fmt.Errorf("invalid mempool swap pool owner index for %s: %w", l1Asset, err)
	}
	if err = record.Valid(); err != nil {
		return MempoolSwap{}, true, fmt.Errorf("invalid mempool swap pool owner for %s: %w", l1Asset, err)
	}
	if !bytes.Equal(canonicalKey, k.mempoolSwapKey(record.TxId, record.Index)) {
		return MempoolSwap{}, true, fmt.Errorf("non-canonical mempool swap pool owner index for %s", l1Asset)
	}
	ownerAssets := mempoolSwapRecordPoolOwnerAssets(&record)
	if _, ok := ownerAssets[mempoolSwapPoolBalanceOverlayMapKey(l1Asset)]; !ok {
		return MempoolSwap{}, true, fmt.Errorf("mempool swap pool owner %s has no recorded effect for %s", record.Cursor(), l1Asset)
	}

	expectedPool := newMempoolSwapPoolBalanceOverlay(l1Asset)
	if contribution, ok := mempoolSwapRecordPoolBalanceOverlays(&record)[mempoolSwapPoolBalanceOverlayMapKey(l1Asset)]; ok {
		expectedPool = contribution
	}
	storedPool, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, l1Asset)
	if err != nil {
		return MempoolSwap{}, true, err
	}
	if !mempoolSwapPoolBalanceOverlayEqual(storedPool, expectedPool) {
		return MempoolSwap{}, true, fmt.Errorf("mempool swap pool balance overlay for %s does not match owner %s", l1Asset, record.Cursor())
	}

	synthAsset := l1Asset.GetSyntheticAsset()
	expectedSupply := newMempoolSwapNativeSupplyOverlay(synthAsset)
	if contribution, ok := mempoolSwapRecordNativeSupplyOverlays(&record)[mempoolSwapNativeSupplyOverlayMapKey(synthAsset)]; ok {
		expectedSupply = contribution
	}
	storedSupply, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, synthAsset)
	if err != nil {
		return MempoolSwap{}, true, err
	}
	if !mempoolSwapNativeSupplyOverlayEqual(storedSupply, expectedSupply) {
		return MempoolSwap{}, true, fmt.Errorf("mempool swap synth supply overlay for %s does not match owner %s", l1Asset, record.Cursor())
	}
	if record.Status != types.MempoolSwapStatus_FINALIZED &&
		mempoolSwapPoolBalanceOverlayEmpty(expectedPool) &&
		mempoolSwapNativeSupplyOverlayEmpty(expectedSupply) {
		return MempoolSwap{}, true, fmt.Errorf("mempool swap pool owner %s has no reversible state for %s", record.Cursor(), l1Asset)
	}
	return record, true, nil
}

func (k KVStore) setMempoolSwap(ctx cosmos.Context, record MempoolSwap) error {
	if err := record.Valid(); err != nil {
		return err
	}

	cacheCtx, commit := ctx.CacheContext()
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(cacheCtx))
	canonicalKey := k.mempoolSwapKey(record.TxId, record.Index)

	var previous MempoolSwap
	found, err := k.getMempoolSwap(cacheCtx, canonicalKey, &previous)
	if err != nil {
		return err
	}
	if found {
		store.Delete(k.mempoolSwapVaultIndexKey(previous))
		store.Delete(k.mempoolSwapTimeoutIndexKey(previous))
	}
	var previousRecord *MempoolSwap
	if found {
		previousRecord = &previous
	}
	if err = k.updateMempoolSwapPoolBalanceOverlays(cacheCtx, previousRecord, &record); err != nil {
		return err
	}
	if err = k.updateMempoolSwapNativeSupplyOverlays(cacheCtx, previousRecord, &record); err != nil {
		return err
	}
	if err = k.updateMempoolSwapPoolOwners(cacheCtx, previousRecord, &record); err != nil {
		return err
	}

	buf, err := k.cdc.Marshal(&record)
	if err != nil {
		return dbError(cacheCtx, "failed to marshal mempool swap", err)
	}
	store.Set(canonicalKey, buf)
	// Secondary indexes contain only an immutable pointer to the canonical key,
	// never a duplicate record. This keeps the canonical lifecycle entry as the
	// single source of truth while allowing bounded vault and timeout recovery.
	store.Set(k.mempoolSwapVaultIndexKey(record), canonicalKey)
	store.Set(k.mempoolSwapTimeoutIndexKey(record), canonicalKey)
	if !found {
		k.SetMempoolSwapCount(cacheCtx, k.GetMempoolSwapCount(cacheCtx).Add(cosmos.OneUint()))
	}
	commit()
	return nil
}

func (k KVStore) getMempoolSwap(ctx cosmos.Context, key []byte, record *MempoolSwap) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(key)
	if bz == nil {
		return false, nil
	}
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("failed to unmarshal mempool swap %s", key), err)
	}
	return true, nil
}

// SetMempoolSwap writes a canonical lifecycle record and atomically refreshes
// its vault and recovery-height indexes. Reservation usage is deliberately
// maintained by the lifecycle transition which calls this method, so it can
// update the record and the aggregate counter in one outer CacheContext.
func (k KVStore) SetMempoolSwap(ctx cosmos.Context, record MempoolSwap) error {
	return k.setMempoolSwap(ctx, record)
}

// RestoreMempoolSwap restores an exported lifecycle record, reconstructs both
// secondary indexes, and derives the record count through the normal setter.
// Aggregate TOR usage is reconstructed separately from ReservedTor.
func (k KVStore) RestoreMempoolSwap(ctx cosmos.Context, record MempoolSwap) error {
	return k.setMempoolSwap(ctx, record)
}

// GetMempoolSwap returns the canonical lifecycle record for a swap identity
// and reports whether it exists.
func (k KVStore) GetMempoolSwap(ctx cosmos.Context, txID common.TxID, index uint32) (MempoolSwap, bool, error) {
	record := MempoolSwap{
		TxId:        txID,
		Index:       index,
		ReservedTor: cosmos.ZeroUint(),
	}
	found, err := k.getMempoolSwap(ctx, k.mempoolSwapKey(txID, index), &record)
	return record, found, err
}

// HasMempoolSwap reports whether a canonical lifecycle record exists.
func (k KVStore) HasMempoolSwap(ctx cosmos.Context, txID common.TxID, index uint32) bool {
	return k.has(ctx, k.mempoolSwapKey(txID, index))
}

// DeleteMempoolSwap removes a lifecycle record, both of its secondary indexes,
// and its aggregate count slot atomically. It does not modify TOR usage.
func (k KVStore) DeleteMempoolSwap(ctx cosmos.Context, txID common.TxID, index uint32) error {
	cacheCtx, commit := ctx.CacheContext()
	canonicalKey := k.mempoolSwapKey(txID, index)
	var record MempoolSwap
	found, err := k.getMempoolSwap(cacheCtx, canonicalKey, &record)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(cacheCtx))
	if err = k.updateMempoolSwapPoolBalanceOverlays(cacheCtx, &record, nil); err != nil {
		return err
	}
	if err = k.updateMempoolSwapNativeSupplyOverlays(cacheCtx, &record, nil); err != nil {
		return err
	}
	if err = k.updateMempoolSwapPoolOwners(cacheCtx, &record, nil); err != nil {
		return err
	}
	store.Delete(k.mempoolSwapVaultIndexKey(record))
	store.Delete(k.mempoolSwapTimeoutIndexKey(record))
	store.Delete(canonicalKey)
	count := k.GetMempoolSwapCount(cacheCtx)
	if count.IsZero() {
		return fmt.Errorf("mempool swap count underflow deleting %s", record.Cursor())
	}
	k.SetMempoolSwapCount(cacheCtx, count.Sub(cosmos.OneUint()))
	commit()
	return nil
}

// GetMempoolSwapIterator iterates canonical lifecycle records only.
func (k KVStore) GetMempoolSwapIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixMempoolSwap)
}

func (k KVStore) getMempoolSwapFromIndex(ctx cosmos.Context, canonicalKey []byte) (MempoolSwap, error) {
	var record MempoolSwap
	found, err := k.getMempoolSwap(ctx, canonicalKey, &record)
	if err != nil {
		return record, err
	}
	if !found {
		return record, fmt.Errorf("mempool swap index points to missing canonical record %s", canonicalKey)
	}
	return record, nil
}

// GetMempoolSwapsByVault returns at most limit lifecycle records belonging to
// a vault, in deterministic transaction-id/index key order.
func (k KVStore) GetMempoolSwapsByVault(ctx cosmos.Context, vaultPubKey common.PubKey, limit int) ([]MempoolSwap, error) {
	return k.GetMempoolSwapsByVaultAfter(ctx, vaultPubKey, "", limit)
}

// GetMempoolSwapsByVaultAfter is the cursor-aware form of
// GetMempoolSwapsByVault. cursor is the last attempted record's Cursor value;
// iteration resumes strictly after it without rescanning earlier index keys.
func (k KVStore) GetMempoolSwapsByVaultAfter(ctx cosmos.Context, vaultPubKey common.PubKey, cursor string, limit int) ([]MempoolSwap, error) {
	if vaultPubKey.IsEmpty() {
		return nil, fmt.Errorf("mempool swap vault pub key cannot be empty")
	}
	if limit <= 0 {
		return []MempoolSwap{}, nil
	}

	prefix := k.GetKey(prefixMempoolSwapVault, vaultPubKey.String()+"/")
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	start := prefix
	if cursor != "" {
		start = append(append([]byte{}, prefix...), []byte(strings.ToUpper(cursor))...)
		// Index keys are exact identities. Appending a zero byte gives the
		// lexicographically smallest key strictly after the persisted cursor.
		start = append(start, 0)
	}
	iter := store.Iterator(start, storetypes.PrefixEndBytes(prefix))
	defer iter.Close()
	records := make([]MempoolSwap, 0, limit)
	for ; iter.Valid() && len(records) < limit; iter.Next() {
		record, err := k.getMempoolSwapFromIndex(ctx, iter.Value())
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// SetMempoolSwapVaultRecovery schedules or advances a persisted, bounded
// inactive-vault recovery job. An empty cursor starts from the first record.
func (k KVStore) SetMempoolSwapVaultRecovery(ctx cosmos.Context, vaultPubKey common.PubKey, cursor string) {
	if vaultPubKey.IsEmpty() {
		ctx.Logger().Error("cannot set mempool swap vault recovery with empty pub key")
		return
	}
	k.setStrings(ctx, k.GetKey(prefixMempoolSwapVaultRecovery, vaultPubKey.String()), []string{vaultPubKey.String(), cursor})
}

// GetMempoolSwapVaultRecovery returns the last attempted lifecycle identity.
// Missing jobs return an empty cursor and no error.
func (k KVStore) GetMempoolSwapVaultRecovery(ctx cosmos.Context, vaultPubKey common.PubKey) (string, error) {
	if vaultPubKey.IsEmpty() {
		return "", fmt.Errorf("mempool swap vault pub key cannot be empty")
	}
	var job []string
	found, err := k.getStrings(ctx, k.GetKey(prefixMempoolSwapVaultRecovery, vaultPubKey.String()), &job)
	if err != nil || !found {
		return "", err
	}
	if len(job) != 2 {
		return "", fmt.Errorf("invalid mempool swap vault recovery job for %s", vaultPubKey)
	}
	return job[1], nil
}

// DeleteMempoolSwapVaultRecovery removes a completed recovery job.
func (k KVStore) DeleteMempoolSwapVaultRecovery(ctx cosmos.Context, vaultPubKey common.PubKey) {
	if vaultPubKey.IsEmpty() {
		return
	}
	k.del(ctx, k.GetKey(prefixMempoolSwapVaultRecovery, vaultPubKey.String()))
}

// GetMempoolSwapVaultRecoveries returns up to limit scheduled vaults in
// deterministic pub-key order.
func (k KVStore) GetMempoolSwapVaultRecoveries(ctx cosmos.Context, limit int) ([]common.PubKey, error) {
	if limit <= 0 {
		return []common.PubKey{}, nil
	}
	iter := k.getIterator(ctx, prefixMempoolSwapVaultRecovery)
	defer iter.Close()
	vaults := make([]common.PubKey, 0, limit)
	for ; iter.Valid() && len(vaults) < limit; iter.Next() {
		var job ProtoStrings
		if err := k.cdc.Unmarshal(iter.Value(), &job); err != nil {
			return nil, dbError(ctx, "failed to unmarshal mempool swap vault recovery job", err)
		}
		if len(job.Value) != 2 {
			return nil, fmt.Errorf("invalid mempool swap vault recovery job: %s", iter.Key())
		}
		vault, err := common.NewPubKey(job.Value[0])
		if err != nil {
			return nil, fmt.Errorf("invalid mempool swap vault recovery pub key: %w", err)
		}
		vaults = append(vaults, vault)
	}
	return vaults, nil
}

// GetMempoolSwapsDue returns at most limit lifecycle records whose recovery
// height is at or before maxHeight, ordered by height then transaction/index.
func (k KVStore) GetMempoolSwapsDue(ctx cosmos.Context, maxHeight int64, limit int) ([]MempoolSwap, error) {
	if maxHeight < 0 || limit <= 0 {
		return []MempoolSwap{}, nil
	}

	iter := k.getIterator(ctx, prefixMempoolSwapTimeout)
	defer iter.Close()
	records := make([]MempoolSwap, 0, limit)
	for ; iter.Valid() && len(records) < limit; iter.Next() {
		record, err := k.getMempoolSwapFromIndex(ctx, iter.Value())
		if err != nil {
			return nil, err
		}
		if record.NextRecoveryHeight > maxHeight {
			break
		}
		records = append(records, record)
	}
	return records, nil
}
