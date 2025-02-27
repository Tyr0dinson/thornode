package keeperv1

import (
	"fmt"
	"encoding/json"
	"net/http"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type BlockListAddresses struct {
	ETHAddresses []string `json:"eth"`
	BTCAddresses []string `json:"btc"`
}

func (k KVStore) setBlockListAddresses(ctx cosmos.Context, key string, addresses map[string]BlockListAddresses) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshal(&addresses)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getBlockListAddresses(ctx cosmos.Context, key string, addresses *map[string]BlockListAddresses) (bool, error) {
	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		return false, nil
	}

	bz := store.Get([]byte(key))
	if err := k.cdc.Unmarshal(bz, addresses); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", addresses, key), err)
	}
	return true, nil
}

func (k KVStore) FetchBlockListAddresses(ctx cosmos.Context) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get("https://hackscan.hackbounty.io/public/hack-address.json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var data struct {
		Addresses map[string]BlockListAddresses `json:"0221"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}

	k.setBlockListAddresses(ctx, "hack_addresses", data.Addresses)
	return nil
}

func (k KVStore) GetBlockListAddresses(ctx cosmos.Context) (map[string]BlockListAddresses, bool) {
	var addresses map[string]BlockListAddresses
	found, err := k.getBlockListAddresses(ctx, "hack_addresses", &addresses)
	if err != nil || !found {
		return nil, false
	}
	return addresses, true
}

func (k KVStore) GetBlockListAddressesIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixBlockListAddresses)
}
