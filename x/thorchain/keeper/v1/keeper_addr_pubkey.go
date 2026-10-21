package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// SetAddrPubKey saves an address to pubkey mapping to kvstore
func (k KVStore) SetAddrPubKey(ctx cosmos.Context, addr common.Address, pubKey common.PubKey) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixAddrPubKey, addr.String())
	if pubKey.IsEmpty() {
		store.Delete(key)
	} else {
		// Store the pubkey string directly as bytes
		store.Set(key, []byte(pubKey.String()))
	}
}

// GetAddrPubKey retrieves a pubkey for a given address from kvstore
func (k KVStore) GetAddrPubKey(ctx cosmos.Context, addr common.Address) (common.PubKey, error) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixAddrPubKey, addr.String())
	if !store.Has(key) {
		return common.EmptyPubKey, nil
	}

	bz := store.Get(key)
	// Convert bytes back to PubKey string
	pubKeyStr := string(bz)
	pubKey, err := common.NewPubKey(pubKeyStr)
	if err != nil {
		return common.EmptyPubKey, err
	}
	return pubKey, nil
}

// HasAddrPubKey checks if an address to pubkey mapping exists
func (k KVStore) HasAddrPubKey(ctx cosmos.Context, addr common.Address) bool {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixAddrPubKey, addr.String())
	return store.Has(key)
}

// DeleteAddrPubKey removes an address to pubkey mapping from kvstore
func (k KVStore) DeleteAddrPubKey(ctx cosmos.Context, addr common.Address) {
	k.del(ctx, k.GetKey(prefixAddrPubKey, addr.String()))
}

// GetAddrPubKeyIterator returns an iterator for all address to pubkey mappings
func (k KVStore) GetAddrPubKeyIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixAddrPubKey)
}
