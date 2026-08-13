package keeperv1

import (
	"bytes"
	"fmt"
	"strconv"

	storetypes "cosmossdk.io/store/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func (k KVStore) setTxOut(ctx cosmos.Context, key []byte, record TxOut) {
	keeper.TxOutCacheFromContext(ctx).Disable()
	noteTxOutWindowWrite(ctx, key)
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
	k.del(ctx, k.getKeyInt64(prefixTxOut, height))
	return nil
}

// SetTxOut - write the given txout information to key value store
func (k KVStore) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if blockOut == nil || blockOut.IsEmpty() {
		return nil
	}
	k.setTxOut(ctx, k.getKeyInt64(prefixTxOut, blockOut.Height), *blockOut)
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
// if present) and returns the stored bytes undecoded. With a TxOutWindow on ctx
// that covers height, or a TxOutCache, the stored bytes (or their absence) of
// each height are shared between the reads of one EndBlock or query instead.
func (k KVStore) getTxOutBytes(ctx cosmos.Context, height int64) (key, bz []byte, present bool) {
	key = k.getKeyInt64(prefixTxOut, height)
	var ok bool
	if bz, present, ok = k.getTxOutWindowBytes(ctx, height); ok {
		return key, bz, present
	}
	cache := keeper.TxOutCacheFromContext(ctx)
	if bz, present, ok = cache.Get(height); ok {
		return key, bz, present
	}
	bz, present = readTxOutBytes(k.kvStore(ctx), cache, key, height)
	return key, bz, present
}

// readTxOutBytes reads the TxOut of height, stored under key, from store (Has,
// then Get if present) and records the result in cache, if any. Callers have
// missed cache: a live one is first filled with the chunk holding height
// (fillTxOutChunk).
func readTxOutBytes(store storetypes.KVStore, cache *keeper.TxOutCache, key []byte, height int64) (bz []byte, present bool) {
	if fillTxOutChunk(store, cache, height) {
		var ok bool
		if bz, present, ok = cache.Get(height); ok {
			return bz, present
		}
	}
	present = store.Has(key)
	if present {
		bz = store.Get(key)
	}
	cache.Set(height, bz, present)
	return bz, present
}

// txOutWindow makes getTxOutBytes' reads of the heights first..last for walks
// that read them many times over (getSortedVaultSecurity reads the window
// once per vault): each height's key is built, and the store and TxOutCache
// are looked up, once per window instead of once per read. Every read is
// still made, through the same gas-metered store and cache, unless a
// keeper.TxOutWindow on ctx (EndBlock only) serves the height.
type txOutWindow struct {
	first int64
	keys  [][]byte
	store storetypes.KVStore
	cache *keeper.TxOutCache
	// past is getTxOutWindowBytes through ctx; nil without a TxOutWindow on ctx
	past func(height int64) (bz []byte, present, ok bool)
}

// newTxOutWindow returns the window of heights first..last (empty if last <
// first) read through ctx.
func (k KVStore) newTxOutWindow(ctx cosmos.Context, first, last int64) txOutWindow {
	w := txOutWindow{first: first, store: k.kvStore(ctx), cache: keeper.TxOutCacheFromContext(ctx)}
	if keeper.TxOutWindowFromContext(ctx) != nil {
		w.past = func(height int64) ([]byte, bool, bool) { return k.getTxOutWindowBytes(ctx, height) }
	}
	if last < first {
		return w
	}
	// getKeyInt64's keys, carved from one buffer and clipped to their length;
	// none is written again, as the store may keep the keys it is given
	var digits [20]byte // len("-9223372036854775808")
	width := max(len(strconv.AppendInt(digits[:0], first, 10)), len(strconv.AppendInt(digits[:0], last, 10)))
	w.keys = make([][]byte, last-first+1)
	buf := make([]byte, 0, len(w.keys)*(len(prefixTxOut)+1+width))
	for i := range w.keys {
		start := len(buf)
		buf = append(buf, prefixTxOut...)
		buf = append(buf, '/')
		buf = strconv.AppendInt(buf, first+int64(i), 10)
		w.keys[i] = buf[start:len(buf):len(buf)]
	}
	return w
}

// get is getTxOutBytes(ctx, height) for a height of the window.
func (w txOutWindow) get(height int64) (key, bz []byte, present bool) {
	key = w.keys[height-w.first]
	var ok bool
	if w.past != nil {
		if bz, present, ok = w.past(height); ok {
			return key, bz, present
		}
	}
	if bz, present, ok = w.cache.Get(height); ok {
		return key, bz, present
	}
	bz, present = readTxOutBytes(w.store, w.cache, key, height)
	return key, bz, present
}

// txOutWalk makes GetTxOut's reads for a walk over consecutive heights
// (GetPendingOutbounds reads every height of its window, most of them empty):
// the store and TxOutCache are looked up once per walk instead of on every
// read, and the keys are carved from shared buffers. Every read is still made,
// in the same order, through the same gas-metered store and cache, unless a
// keeper.TxOutWindow on ctx (EndBlock only) serves the height.
type txOutWalk struct {
	store storetypes.KVStore
	cache *keeper.TxOutCache
	buf   []byte // the unused tail of the current key buffer
}

func (k KVStore) newTxOutWalk(ctx cosmos.Context) *txOutWalk {
	return &txOutWalk{store: k.kvStore(ctx), cache: keeper.TxOutCacheFromContext(ctx)}
}

// key is getKeyInt64(prefixTxOut, height), clipped to its length. A key is
// never written again, as the store may keep the keys it is given.
func (w *txOutWalk) key(height int64) []byte {
	const maxKey = len(prefixTxOut) + 1 + 20 // len("-9223372036854775808")
	if cap(w.buf)-len(w.buf) < maxKey {
		w.buf = make([]byte, 0, 64*maxKey)
	}
	start := len(w.buf)
	w.buf = append(w.buf, prefixTxOut...)
	w.buf = append(w.buf, '/')
	w.buf = strconv.AppendInt(w.buf, height, 10)
	return w.buf[start:len(w.buf):len(w.buf)]
}

// getTxOutWalk is GetTxOut(ctx, height) read through w, except that a height
// with no TxOut stored returns nil rather than an empty TxOut.
func (k KVStore) getTxOutWalk(ctx cosmos.Context, w *txOutWalk, height int64) (*TxOut, error) {
	key := w.key(height)
	bz, present, ok := k.getTxOutWindowBytes(ctx, height)
	if !ok {
		bz, present, ok = w.cache.Get(height)
	}
	if !ok {
		bz, present = readTxOutBytes(w.store, w.cache, key, height)
	}
	if !present {
		return nil, nil
	}
	record := NewTxOut(height)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

// fillTxOutChunk reads the stored txouts of the aligned chunk of
// keeper.TxOutCacheChunk heights holding height with one range iteration and
// records them, and the absence of all other heights of the chunk, in cache.
// It returns whether it did; only a live cache, which only API quotes attach
// (never a tx: the iteration is not the point reads' gas), fills. store is the
// gas-metered store of the read's ctx.
//
// Keys are the decimal heights, which order lexicographically as numbers only
// at equal length: a chunk of heights >= TxOutCacheChunk never spans two key
// lengths, and keys of other lengths that sort between its bounds are
// skipped.
func fillTxOutChunk(store storetypes.KVStore, cache *keeper.TxOutCache, height int64) bool {
	if height < keeper.TxOutCacheChunk {
		return false
	}
	chunk := height / keeper.TxOutCacheChunk
	if !cache.ClaimChunk(chunk) {
		return false
	}
	lo := chunk * keeper.TxOutCacheChunk
	hi := lo + keeper.TxOutCacheChunk - 1
	// getKeyInt64's keys
	prefix := append([]byte(prefixTxOut), '/')
	start := strconv.AppendInt(bytes.Clone(prefix), lo, 10)
	end := append(strconv.AppendInt(bytes.Clone(prefix), hi, 10), 0) // just past hi's key
	digits := len(start) - len(prefix)
	stored := make(map[int64][]byte)
	iter := store.Iterator(start, end)
	for ; iter.Valid(); iter.Next() {
		suffix := iter.Key()[len(prefix):]
		if len(suffix) != digits {
			continue
		}
		h, err := strconv.ParseInt(string(suffix), 10, 64)
		if err != nil || h < lo || h > hi {
			continue
		}
		stored[h] = bytes.Clone(iter.Value())
	}
	iter.Close()
	cache.Fill(lo, hi, stored)
	return true
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
	poolCache := map[common.Asset]Pool{} // Cache finalized pool copies to avoid duplicated keeper reads
	for i := range tois {
		for _, coin := range append(common.Coins{tois[i].Coin}, tois[i].MaxGas...) {
			if coin.IsRune() {
				runeValue = runeValue.Add(coin.Amount)
				continue
			}

			poolAsset := coin.Asset.GetLayer1Asset()
			pool, ok := poolCache[poolAsset]
			if !ok {
				var err error
				pool, err = k.GetPool(ctx, poolAsset)
				if err != nil {
					_ = dbError(ctx, fmt.Sprintf("unable to get pool : %s", coin.Asset), err)
					continue
				}
				rawPool := pool
				pool, err = k.finalityAdjustedPool(ctx, pool)
				if err != nil {
					ctx.Logger().Error("unable to remove active mempool swap balances from txout valuation", "asset", poolAsset, "error", err)
					pool = rawPool
				}
				poolCache[poolAsset] = pool
			}
			runeValue = runeValue.Add(pool.AssetValueInRune(coin.Amount))
		}

		if tois[i].CloutSpent != nil {
			cloutValue = cloutValue.Add(*tois[i].CloutSpent)
		}
	}

	return runeValue, cloutValue
}

// getTxOutWindowBytes serves height from the TxOutWindow on ctx covering it,
// if any. The first read loads every covered height with one store iteration:
// the same present keys and values as a Has (and Get) per height.
func (k KVStore) getTxOutWindowBytes(ctx cosmos.Context, height int64) (bz []byte, present, ok bool) {
	window := keeper.TxOutWindowFromContext(ctx).Covering(height)
	lo, hi, ok := window.Range(height)
	if !ok {
		return nil, false, false
	}
	if bz, present, ok = window.Get(height); ok {
		return bz, present, true
	}
	loStr, hiStr := strconv.FormatInt(lo, 10), strconv.FormatInt(hi, 10)
	if len(loStr) != len(hiStr) {
		// keys only sort like heights of the same length
		window.Disable()
		return nil, false, false
	}
	start := k.GetKey(prefixTxOut, loStr)
	end := append(k.GetKey(prefixTxOut, hiStr), 0)
	entries := make(map[int64][]byte)
	iter := k.kvStore(ctx).Iterator(start, end)
	for ; iter.Valid(); iter.Next() {
		// the range also holds keys of other lengths, e.g. height 2812370
		// sorts between 28123699 and 28123700
		h, canonical := txOutKeyHeight(iter.Key())
		if canonical && h >= lo && h <= hi {
			entries[h] = bytes.Clone(iter.Value())
		}
	}
	iter.Close()
	window.Load(entries)
	return window.Get(height)
}

// txOutKeyHeight parses the height of a txout store key; ok is false unless
// the key is exactly the one GetKey builds for that height.
func txOutKeyHeight(key []byte) (int64, bool) {
	suffix, found := bytes.CutPrefix(key, []byte(prefixTxOut+"/"))
	if !found {
		return 0, false
	}
	h, err := strconv.ParseInt(string(suffix), 10, 64)
	if err != nil || strconv.FormatInt(h, 10) != string(suffix) {
		return 0, false
	}
	return h, true
}

// noteTxOutWindowWrite tells the TxOutWindow on ctx about a write to a txout
// key.
func noteTxOutWindowWrite(ctx cosmos.Context, key []byte) {
	window := keeper.TxOutWindowFromContext(ctx)
	if window == nil {
		return
	}
	window.NoteWrite(txOutKeyHeight(key))
}
