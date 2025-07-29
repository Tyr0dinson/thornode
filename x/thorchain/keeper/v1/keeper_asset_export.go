package keeperv1

import (
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) SetAssetExport(ctx cosmos.Context, export AssetExport) error {
	if export.Asset.IsEmpty() {
		return fmt.Errorf("cannot save an export export with empty asset")
	}

	key := k.GetKey(prefixAssetExport, export.Asset.String())

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

	key := k.GetKey(prefixAssetExport, asset.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return export, nil
	}

	data := store.Get(key)

	err := k.cdc.Unmarshal(data, &export)
	if err != nil {
		wrapper := fmt.Sprintf("Unmarshal kvstore: (%T) %s", export, key)
		return export, dbError(ctx, wrapper, err)
	}

	return export, nil
}

func (k KVStore) GetAssetExportIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixAssetExport)
}
