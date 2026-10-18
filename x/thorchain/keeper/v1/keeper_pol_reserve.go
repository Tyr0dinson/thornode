package keeperv1

import (
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setPOLReserveDeposit(ctx cosmos.Context, key []byte, record POLReserveDeposit) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getPOLReserveDeposit(ctx cosmos.Context, key []byte, record *POLReserveDeposit) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// GetPOLReserveDeposit retrieves the POL reserve deposit record for a given asset
func (k KVStore) GetPOLReserveDeposit(ctx cosmos.Context, asset common.Asset) (POLReserveDeposit, error) {
	record := NewPOLReserveDeposit(asset)
	_, err := k.getPOLReserveDeposit(ctx, k.GetKey(prefixPOLReserve, asset.String()), &record)
	return record, err
}

// SetPOLReserveDeposit saves the POL reserve deposit record for a given asset
func (k KVStore) SetPOLReserveDeposit(ctx cosmos.Context, data POLReserveDeposit) error {
	k.setPOLReserveDeposit(ctx, k.GetKey(prefixPOLReserve, data.Asset.String()), data)
	return nil
}

// GetPOLReserveDepositIterator returns an iterator over every stored
// POLReserveDeposit record. Used for genesis export.
func (k KVStore) GetPOLReserveDepositIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixPOLReserve)
}

// IsPOLReserveEligiblePool reports whether an asset's pool is eligible for POL
// Reserve auto-deployment. Precedence: Blacklist > Whitelist > default set.
// The default set is the union of gas assets and TOR anchor pools (any pool
// with a positive TorAnchor-{asset} mimir, matching keeper_anchors.go).
func (k KVStore) IsPOLReserveEligiblePool(ctx cosmos.Context, asset common.Asset) bool {
	key := asset.MimirString()

	// 1. Blacklist wins unconditionally.
	if v, err := k.GetMimir(ctx, fmt.Sprintf("POLReserveBlacklist-%s", key)); err == nil && v > 0 {
		return false
	}
	// 2. Whitelist force-includes the pool.
	if v, err := k.GetMimir(ctx, fmt.Sprintf("POLReserveWhitelist-%s", key)); err == nil && v > 0 {
		return true
	}
	// 3. Gas asset (BTC.BTC, ETH.ETH, etc.) is in the default set.
	if asset.IsGasAsset() {
		return true
	}
	// 4. TOR anchor pools are in the default set. Mirror the key format used by
	// KVStore.GetAnchors so operators can reuse existing TorAnchor mimirs.
	anchorKey := strings.ReplaceAll(fmt.Sprintf("TorAnchor-%s", asset.String()), ".", "-")
	if v, err := k.GetMimir(ctx, anchorKey); err == nil && v > 0 {
		return true
	}
	return false
}

// GetPOLReserveTarget returns the pool picked by the last POL Reserve snapshot.
func (k KVStore) GetPOLReserveTarget(ctx cosmos.Context) (common.Asset, bool, error) {
	var record string
	found, err := k.getString(ctx, string(k.GetKey(prefixPOLReserveTarget, "")), &record)
	if err != nil || !found {
		return common.EmptyAsset, false, err
	}
	asset, err := common.NewAsset(record)
	if err != nil {
		return common.EmptyAsset, false, err
	}
	return asset, true, nil
}

// SetPOLReserveTarget saves the pool picked by a POL Reserve snapshot.
func (k KVStore) SetPOLReserveTarget(ctx cosmos.Context, asset common.Asset) {
	k.setString(ctx, string(k.GetKey(prefixPOLReserveTarget, "")), asset.String())
}

// ClearPOLReserveTarget removes the stored POL Reserve snapshot pool.
func (k KVStore) ClearPOLReserveTarget(ctx cosmos.Context) {
	k.del(ctx, k.GetKey(prefixPOLReserveTarget, ""))
}

// GetPOLReserveDrainState returns the pool being drained and the RUNE owed to
// pol_reserve that has not been withdrawn yet. found is false when no drain is
// in progress.
func (k KVStore) GetPOLReserveDrainState(ctx cosmos.Context) (common.Asset, cosmos.Uint, bool, error) {
	var record string
	found, err := k.getString(ctx, string(k.GetKey(prefixPOLReserveDrainAsset, "")), &record)
	if err != nil || !found {
		return common.EmptyAsset, cosmos.ZeroUint(), false, err
	}
	asset, err := common.NewAsset(record)
	if err != nil {
		return common.EmptyAsset, cosmos.ZeroUint(), false, err
	}
	var owed uint64
	if _, err = k.getUint64(ctx, k.GetKey(prefixPOLReserveDrainOwed, ""), &owed); err != nil {
		return common.EmptyAsset, cosmos.ZeroUint(), false, err
	}
	return asset, cosmos.NewUint(owed), true, nil
}

// SetPOLReserveDrainState saves the pool being drained and the RUNE still owed
// to pol_reserve, writing only the values that changed.
func (k KVStore) SetPOLReserveDrainState(ctx cosmos.Context, asset common.Asset, owed cosmos.Uint) {
	current, currentOwed, found, err := k.GetPOLReserveDrainState(ctx)
	if err != nil || !found || !current.Equals(asset) {
		k.setString(ctx, string(k.GetKey(prefixPOLReserveDrainAsset, "")), asset.String())
	}
	if err == nil && found && currentOwed.Equal(owed) {
		return
	}
	if owed.IsZero() {
		k.del(ctx, k.GetKey(prefixPOLReserveDrainOwed, ""))
		return
	}
	k.setUint64(ctx, k.GetKey(prefixPOLReserveDrainOwed, ""), owed.Uint64())
}

// ClearPOLReserveDrainState removes any stored drain progress.
func (k KVStore) ClearPOLReserveDrainState(ctx cosmos.Context) {
	k.del(ctx, k.GetKey(prefixPOLReserveDrainAsset, ""))
	k.del(ctx, k.GetKey(prefixPOLReserveDrainOwed, ""))
}
