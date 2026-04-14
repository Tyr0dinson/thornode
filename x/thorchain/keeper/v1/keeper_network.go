package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setNetwork(ctx cosmos.Context, key []byte, record Network) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getNetwork(ctx cosmos.Context, key []byte, record *Network) (bool, error) {
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

// GetNetwork retrieve network data from key value store
func (k KVStore) GetNetwork(ctx cosmos.Context) (Network, error) {
	record := NewNetwork()
	_, err := k.getNetwork(ctx, k.GetKey(prefixNetwork, ""), &record)
	return record, err
}

// SetNetwork save the given network data to key value store, it will overwrite existing vault
func (k KVStore) SetNetwork(ctx cosmos.Context, data Network) error {
	k.setNetwork(ctx, k.GetKey(prefixNetwork, ""), data)
	return nil
}

func (k KVStore) GetChainIncome(ctx cosmos.Context, chain common.Chain) (cosmos.Uint, error) {
	network, err := k.GetNetwork(ctx)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	chainStr := chain.String()
	for _, ci := range network.ChainIncome {
		if ci.Chain == chainStr {
			return ci.Amount, nil
		}
	}
	return cosmos.ZeroUint(), nil
}

func (k KVStore) AddChainIncome(ctx cosmos.Context, chain common.Chain, amount cosmos.Uint) error {
	network, err := k.GetNetwork(ctx)
	if err != nil {
		return err
	}
	chainStr := chain.String()
	found := false
	for i, ci := range network.ChainIncome {
		if ci.Chain == chainStr {
			network.ChainIncome[i].Amount = network.ChainIncome[i].Amount.Add(amount)
			found = true
			break
		}
	}
	if !found {
		network.ChainIncome = append(network.ChainIncome, ChainIncome{
			Chain:  chainStr,
			Amount: amount,
		})
	}
	return k.SetNetwork(ctx, network)
}

func (k KVStore) ZeroChainIncome(ctx cosmos.Context, chain common.Chain) error {
	network, err := k.GetNetwork(ctx)
	if err != nil {
		return err
	}
	chainStr := chain.String()
	for i, ci := range network.ChainIncome {
		if ci.Chain == chainStr {
			network.ChainIncome[i].Amount = cosmos.ZeroUint()
			return k.SetNetwork(ctx, network)
		}
	}
	return nil
}

func (k KVStore) setPOL(ctx cosmos.Context, key []byte, record ProtocolOwnedLiquidity) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getPOL(ctx cosmos.Context, key []byte, record *ProtocolOwnedLiquidity) (bool, error) {
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

// GetPOL retrieve network data from key value store
func (k KVStore) GetPOL(ctx cosmos.Context) (ProtocolOwnedLiquidity, error) {
	record := NewProtocolOwnedLiquidity()
	_, err := k.getPOL(ctx, k.GetKey(prefixPOL, ""), &record)
	return record, err
}

// SetPOL save the given network data to key value store, it will overwrite existing vault
func (k KVStore) SetPOL(ctx cosmos.Context, data ProtocolOwnedLiquidity) error {
	k.setPOL(ctx, k.GetKey(prefixPOL, ""), data)
	return nil
}
