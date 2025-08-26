package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) SetExportAsset(ctx cosmos.Context, exportAsset ExportAsset) error {
	err := exportAsset.Valid()
	if err != nil {
		return err
	}

	key := k.GetKey(prefixExportAsset, exportAsset.Asset.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	buf := k.cdc.MustMarshal(&exportAsset)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}

	return nil
}

func (k KVStore) GetExportAsset(
	ctx cosmos.Context,
	commonAsset common.Asset,
) (ExportAsset, error) {
	key := k.GetKey(prefixExportAsset, commonAsset.String())

	var exportAsset ExportAsset

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return exportAsset, fmt.Errorf("export asset %s not found in store", commonAsset.String())
	}

	data := store.Get(key)

	err := k.cdc.Unmarshal(data, &exportAsset)
	if err != nil {
		wrapper := fmt.Sprintf("Unmarshal kvstore: (%T) %s", commonAsset, key)
		return exportAsset, dbError(ctx, wrapper, err)
	}

	return exportAsset, nil
}

func (k KVStore) GetExportAssetIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixExportAsset)
}
