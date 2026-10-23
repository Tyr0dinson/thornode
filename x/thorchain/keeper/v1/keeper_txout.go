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
	key, bz, present := k.getTxOutBytes(ctx, height)
	if !present {
		return record, nil
	}
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

// getTxOutBytes makes GetTxOut's store reads (those of getTxOut: Has, then Get
// if present) and returns the stored bytes undecoded. With a TxOutCache on ctx
// the stored bytes (or their absence) of each height are shared between the
// reads of one query instead.
func (k KVStore) getTxOutBytes(ctx cosmos.Context, height int64) (key, bz []byte, present bool) {
	key = k.GetKey(prefixTxOut, strconv.FormatInt(height, 10))
	cache := keeper.TxOutCacheFromContext(ctx)
	if cache != nil {
		var ok bool
		if bz, present, ok = cache.Get(height); ok {
			return key, bz, present
		}
	}
	store := k.kvStore(ctx)
	present = store.Has(key)
	if present {
		bz = store.Get(key)
	}
	if cache != nil {
		cache.Set(height, bz, present)
	}
	return key, bz, present
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
