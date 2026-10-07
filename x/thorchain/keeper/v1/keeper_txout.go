package keeperv1

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func (k KVStore) setTxOut(ctx cosmos.Context, key []byte, record TxOut) {
	keeper.TxOutCacheFromContext(ctx).Disable()
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getTxOut(ctx cosmos.Context, key []byte, record *TxOut) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// AppendTxOut - append the given item to txOut
func (k KVStore) AppendTxOut(ctx cosmos.Context, height int64, item TxOutItem) error {
	block, err := k.GetTxOut(ctx, height)
	if err != nil {
		return err
	}
	block.TxArray = append(block.TxArray, item)
	return k.SetTxOut(ctx, block)
}

// ClearTxOut - remove the txout of the given height from key value  store
func (k KVStore) ClearTxOut(ctx cosmos.Context, height int64) error {
	k.del(ctx, k.GetKey(prefixTxOut, strconv.FormatInt(height, 10)))
	return nil
}

// SetTxOut - write the given txout information to key value store
func (k KVStore) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if blockOut == nil || blockOut.IsEmpty() {
		return nil
	}
	k.setTxOut(ctx, k.GetKey(prefixTxOut, strconv.FormatInt(blockOut.Height, 10)), *blockOut)
	return nil
}

// GetTxOutIterator iterate tx out
func (k KVStore) GetTxOutIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixTxOut)
}

// GetTxOut - write the given txout information to key value store
func (k KVStore) GetTxOut(ctx cosmos.Context, height int64) (*TxOut, error) {
	record := NewTxOut(height)
	cache := keeper.TxOutCacheFromContext(ctx)
	if cache == nil {
		_, err := k.getTxOut(ctx, k.GetKey(prefixTxOut, strconv.FormatInt(height, 10)), record)
		return record, err
	}

	// same store reads and decode as getTxOut, sharing the stored bytes (or
	// their absence) of each height between the reads of one query
	bz, present, ok := cache.Get(height)
	if !ok {
		store := k.kvStore(ctx)
		key := k.GetKey(prefixTxOut, strconv.FormatInt(height, 10))
		present = store.Has(key)
		if present {
			bz = store.Get(key)
		}
		cache.Set(height, bz, present)
	}
	if !present {
		return record, nil
	}
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		key := k.GetKey(prefixTxOut, strconv.FormatInt(height, 10))
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

func (k KVStore) GetTxOutValue(ctx cosmos.Context, height int64) (cosmos.Uint, cosmos.Uint, error) {
	txout, err := k.GetTxOut(ctx, height)
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}
	runeValue, cloutValue := k.GetTOIsValue(ctx, txout.TxArray...)
	return runeValue, cloutValue, nil
}

func (k KVStore) GetTOIsValue(ctx cosmos.Context, tois ...TxOutItem) (cosmos.Uint, cosmos.Uint) {
	runeValue := cosmos.ZeroUint()
	cloutValue := cosmos.ZeroUint()
	poolCache := map[common.Asset]Pool{} // Cache the pools to avoid duplicated GetPool calls
	for i := range tois {
		for _, coin := range append(common.Coins{tois[i].Coin}, tois[i].MaxGas...) {
			if coin.IsRune() {
				runeValue = runeValue.Add(coin.Amount)
				continue
			}

			pool, ok := poolCache[coin.Asset]
			if !ok {
				var err error
				pool, err = k.GetPool(ctx, coin.Asset.GetLayer1Asset())
				if err != nil {
					_ = dbError(ctx, fmt.Sprintf("unable to get pool : %s", coin.Asset), err)
					continue
				}
				poolCache[coin.Asset] = pool
			}
			runeValue = runeValue.Add(pool.AssetValueInRune(coin.Amount))
		}

		if tois[i].CloutSpent != nil {
			cloutValue = cloutValue.Add(*tois[i].CloutSpent)
		}
	}

	return runeValue, cloutValue
}
