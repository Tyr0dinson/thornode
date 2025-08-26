package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func (k KVStore) SetAssetExport(ctx cosmos.Context, export AssetExport) error {
	err := export.Valid()
	if err != nil {
		return err
	}

	key := k.GetKey(prefixAssetExport, export.Asset.Symbol.String(), export.Asset.Chain.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	buf := k.cdc.MustMarshal(&export)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}

	return nil
}

func (k KVStore) GetAssetExport(
	ctx cosmos.Context,
	asset common.Asset,
) (AssetExport, error) {
	export := NewAssetExport()

	key := k.GetKey(prefixAssetExport, asset.Symbol.String(), asset.Chain.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return export, fmt.Errorf("asset export %s not found in store", export.String())
	}

	data := store.Get(key)

	err := k.cdc.Unmarshal(data, &export)
	if err != nil {
		wrapper := fmt.Sprintf("Unmarshal kvstore: (%T) %s", export, key)
		return export, dbError(ctx, wrapper, err)
	}

	return export, nil
}

func (k KVStore) GetAssetExportIterator(ctx cosmos.Context, asset common.Asset) cosmos.Iterator {
	return k.getIterator(ctx, prefixAssetExport+types.DbPrefix("/"+asset.Symbol.String()))
}
