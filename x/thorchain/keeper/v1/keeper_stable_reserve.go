package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func stableReserveCreditKey(asset common.Asset, addr common.Address) string {
	return fmt.Sprintf("%s/%s", asset.GetLayer1Asset().String(), addr.String())
}

// stableReserveCreditValue encodes a stored credit as "<address>/<amount>". The
// address is kept verbatim (original case) so a hard-fork genesis export can
// recover it: KVStore.GetKey uppercases the whole store key, which corrupts
// case-sensitive base58 addresses (TRON/SOL/XRP) beyond repair, so the address
// cannot be read back from the key. "/" is a safe separator because addresses
// are [:_A-Za-z0-9] (common.alphaNumRegex) and never contain it — the same
// property stableReserveCreditKey already relies on.
//
// ponytail: a delimited string rather than a proto value. StableReserveCredit
// lives in the thorchain package, so importing it here would be circular. Swap
// for a types-package proto if this value ever needs more than address+amount.
func stableReserveCreditValue(addr common.Address, amount cosmos.Uint) string {
	return fmt.Sprintf("%s/%s", addr.String(), amount.String())
}

// parseStableReserveCreditValue splits a stored credit value into its original
// contributor address and amount. A value written before address preservation
// (a bare amount, no "/") returns an empty address.
func parseStableReserveCreditValue(value string) (common.Address, cosmos.Uint) {
	if addr, amt, ok := strings.Cut(value, "/"); ok {
		return common.Address(addr), cosmos.NewUintFromString(amt)
	}
	return common.NoAddress, cosmos.NewUintFromString(value)
}

func (k KVStore) GetStableReserveCredit(ctx cosmos.Context, asset common.Asset, addr common.Address) (cosmos.Uint, error) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixStableReserveCredit, stableReserveCreditKey(asset, addr))
	if !store.Has(key) {
		return cosmos.ZeroUint(), nil
	}
	_, amount := parseStableReserveCreditValue(string(store.Get(key)))
	return amount, nil
}

func (k KVStore) SetStableReserveCredit(ctx cosmos.Context, asset common.Asset, addr common.Address, amount cosmos.Uint) error {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixStableReserveCredit, stableReserveCreditKey(asset, addr))
	if amount.IsZero() {
		store.Delete(key)
		return nil
	}
	store.Set(key, []byte(stableReserveCreditValue(addr, amount)))
	return nil
}

// GetStableReserveCreditIterator iterates over every stored stable reserve
// credit (e.g. for hard-fork GenesisState export).
func (k KVStore) GetStableReserveCreditIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixStableReserveCredit)
}

// stableReserveDepositValueKey is the single fixed sub-key under the deposit
// prefix: the deposit value is one aggregate reserve-wide total, not per-asset
// or per-contributor.
const stableReserveDepositValueKey = "total"

// GetStableReserveDepositValue returns the aggregate USD value of all stable
// reserve deposits, in 1e8 fixed-point (1e8 = $1). Returns zero when unset.
func (k KVStore) GetStableReserveDepositValue(ctx cosmos.Context) (cosmos.Uint, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixStableReserveDeposit, stableReserveDepositValueKey)
	if !store.Has(key) {
		return cosmos.ZeroUint(), nil
	}
	return cosmos.NewUintFromString(string(store.Get(key))), nil
}

// SetStableReserveDepositValue stores the aggregate USD deposit value. A zero
// value deletes the key so Get round-trips back to zero.
func (k KVStore) SetStableReserveDepositValue(ctx cosmos.Context, value cosmos.Uint) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixStableReserveDeposit, stableReserveDepositValueKey)
	if value.IsZero() {
		store.Delete(key)
		return nil
	}
	store.Set(key, []byte(value.String()))
	return nil
}
