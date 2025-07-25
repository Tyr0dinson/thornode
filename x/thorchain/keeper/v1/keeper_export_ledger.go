package keeperv1

import (
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) SetExportLedger(ctx cosmos.Context, ledger ExportLedger) error {
	if ledger.Asset.IsEmpty() {
		return fmt.Errorf("cannot save an export ledger with empty asset")
	}

	key := k.GetKey(prefixExportLedger, ledger.Asset.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	buf := k.cdc.MustMarshal(&ledger)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}

	return nil
}

func (k KVStore) GetExportLedger(
	ctx cosmos.Context,
	asset common.Asset,
) (ExportLedger, error) {
	ledger := NewExportLedger()

	key := k.GetKey(prefixExportLedger, asset.String())

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return ledger, nil
	}

	data := store.Get(key)

	err := k.cdc.Unmarshal(data, &ledger)
	if err != nil {
		wrapper := fmt.Sprintf("Unmarshal kvstore: (%T) %s", ledger, key)
		return ledger, dbError(ctx, wrapper, err)
	}

	return ledger, nil
}

func (k KVStore) GetExportLedgerIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixExportLedger)
}
