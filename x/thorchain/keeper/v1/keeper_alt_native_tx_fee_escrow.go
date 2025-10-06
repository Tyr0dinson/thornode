package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// --- Alt-Native Fee Escrow (accumulated alt-gas balances) ---

// AddAltNativeFeeEscrowBalance increments the escrow balance for the provided asset.
func (k KVStore) AddAltNativeFeeEscrowBalance(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint) error {
	if amount.IsZero() {
		return nil
	}
	asset = asset.GetLayer1Asset()
	key := k.GetKey(prefixAltNativeFeeEscrow, asset.String())
	store := k.kvStore(ctx)
	var escrow AltNativeFeeEscrow
	if store.Has(key) {
		if err := k.cdc.Unmarshal(store.Get(key), &escrow); err != nil {
			return err
		}
	}
	if escrow.Balance.IsEmpty() || !escrow.Balance.Asset.Equals(asset) {
		escrow.Balance = common.NewCoin(asset, amount)
	} else {
		escrow.Balance.Amount = escrow.Balance.Amount.Add(amount)
	}
	store.Set(key, k.cdc.MustMarshal(&escrow))
	return nil
}

// GetAltNativeFeeEscrow retrieves the escrow balance for the provided asset.
func (k KVStore) GetAltNativeFeeEscrow(ctx cosmos.Context, asset common.Asset) (AltNativeFeeEscrow, bool) {
	asset = asset.GetLayer1Asset()
	key := k.GetKey(prefixAltNativeFeeEscrow, asset.String())
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return AltNativeFeeEscrow{}, false
	}
	var escrow AltNativeFeeEscrow
	if err := k.cdc.Unmarshal(store.Get(key), &escrow); err != nil {
		return AltNativeFeeEscrow{}, false
	}
	return escrow, true
}

// RemoveAltNativeFeeEscrow deletes the escrow record for the provided asset.
func (k KVStore) RemoveAltNativeFeeEscrow(ctx cosmos.Context, asset common.Asset) {
	asset = asset.GetLayer1Asset()
	key := k.GetKey(prefixAltNativeFeeEscrow, asset.String())
	k.DeleteKey(ctx, key)
}

// GetAltNativeFeeEscrowIterator returns an iterator across all escrowed balances.
func (k KVStore) GetAltNativeFeeEscrowIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixAltNativeFeeEscrow)
}
