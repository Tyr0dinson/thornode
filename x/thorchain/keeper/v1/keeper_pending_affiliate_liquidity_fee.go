package keeperv1

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

const pendingAffiliateLiquidityFeeIndexWidth = 10

func pendingAffiliateLiquidityFeeKey(txID common.TxID, index uint32, thorname string) string {
	return fmt.Sprintf("%s/%0*d/%s", txID, pendingAffiliateLiquidityFeeIndexWidth, index, thorname)
}

func (k KVStore) pendingAffiliateLiquidityFeeTxPrefix(txID common.TxID) []byte {
	return append(k.GetKey(prefixPendingAffiliateLiqFee, txID.String()), '/')
}

// AddPendingAffiliateLiquidityFee accumulates the exact RUNE liquidity fee
// produced by one speculative swap execution. Streaming slices and both legs
// of a double swap deliberately fold into the same tx/index/thorname entry.
func (k KVStore) AddPendingAffiliateLiquidityFee(ctx cosmos.Context, txID common.TxID, index uint32, thorname string, fee cosmos.Uint) error {
	if fee.IsZero() {
		return nil
	}
	if txID.IsEmpty() || txID.Equals(common.BlankTxID) {
		return fmt.Errorf("pending affiliate liquidity fee requires a transaction ID")
	}
	thorname = strings.TrimSpace(thorname)
	if thorname == "" || strings.Contains(thorname, "/") {
		return fmt.Errorf("invalid pending affiliate liquidity fee thorname %q", thorname)
	}

	feeU64, err := safeUint64(fee)
	if err != nil {
		return fmt.Errorf("pending affiliate liquidity fee for %s: %w", thorname, err)
	}
	key := k.GetKey(prefixPendingAffiliateLiqFee, pendingAffiliateLiquidityFeeKey(txID, index, thorname))
	var current uint64
	if _, err = k.getUint64(ctx, key, &current); err != nil {
		return err
	}
	if current > math.MaxUint64-feeU64 {
		return fmt.Errorf("pending affiliate liquidity fee overflow for %s/%d/%s: current=%d fee=%d", txID, index, thorname, current, feeU64)
	}
	k.setUint64(ctx, key, current+feeU64)
	return nil
}

func decodePendingAffiliateLiquidityFee(k KVStore, rawKey, rawValue []byte) (keeper.PendingAffiliateLiquidityFee, error) {
	keyPrefix := string(prefixPendingAffiliateLiqFee) + "/"
	key := strings.TrimPrefix(string(rawKey), keyPrefix)
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("invalid pending affiliate liquidity fee key %q", rawKey)
	}
	txID, err := common.NewTxID(parts[0])
	if err != nil {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("invalid pending affiliate liquidity fee txid %q: %w", parts[0], err)
	}
	index, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("invalid pending affiliate liquidity fee index %q: %w", parts[1], err)
	}
	if parts[2] == "" {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("empty pending affiliate liquidity fee thorname")
	}
	expectedKey := k.GetKey(
		prefixPendingAffiliateLiqFee,
		pendingAffiliateLiquidityFeeKey(txID, uint32(index), parts[2]),
	)
	if !bytes.Equal(rawKey, expectedKey) {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("non-canonical pending affiliate liquidity fee key %q", rawKey)
	}
	var value ProtoUint64
	if err = k.cdc.Unmarshal(rawValue, &value); err != nil {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("fail to decode pending affiliate liquidity fee %q: %w", rawKey, err)
	}
	if value.GetValue() == 0 {
		return keeper.PendingAffiliateLiquidityFee{}, fmt.Errorf("zero pending affiliate liquidity fee %q", rawKey)
	}
	return keeper.PendingAffiliateLiquidityFee{
		TxID:     txID,
		Index:    uint32(index),
		Thorname: parts[2],
		Fee:      cosmos.NewUint(value.GetValue()),
	}, nil
}

func (k KVStore) getPendingAffiliateLiquidityFees(ctx cosmos.Context, prefix []byte) ([]keeper.PendingAffiliateLiquidityFee, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	iter := cosmos.KVStorePrefixIterator(store, prefix)
	defer iter.Close()

	out := make([]keeper.PendingAffiliateLiquidityFee, 0)
	for ; iter.Valid(); iter.Next() {
		entry, err := decodePendingAffiliateLiquidityFee(k, iter.Key(), iter.Value())
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// GetPendingAffiliateLiquidityFees returns one transaction's pending entries
// in deterministic index/thorname key order.
func (k KVStore) GetPendingAffiliateLiquidityFees(ctx cosmos.Context, txID common.TxID) ([]keeper.PendingAffiliateLiquidityFee, error) {
	if txID.IsEmpty() || txID.Equals(common.BlankTxID) {
		return nil, fmt.Errorf("pending affiliate liquidity fee lookup requires a transaction ID")
	}
	return k.getPendingAffiliateLiquidityFees(ctx, k.pendingAffiliateLiquidityFeeTxPrefix(txID))
}

// GetAllPendingAffiliateLiquidityFees returns every pending entry in
// deterministic tx/index/thorname key order. It is used by genesis export.
func (k KVStore) GetAllPendingAffiliateLiquidityFees(ctx cosmos.Context) ([]keeper.PendingAffiliateLiquidityFee, error) {
	return k.getPendingAffiliateLiquidityFees(ctx, []byte(prefixPendingAffiliateLiqFee))
}

func (k KVStore) HasPendingAffiliateLiquidityFees(ctx cosmos.Context, txID common.TxID) bool {
	if txID.IsEmpty() || txID.Equals(common.BlankTxID) {
		return false
	}
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	iter := cosmos.KVStorePrefixIterator(store, k.pendingAffiliateLiquidityFeeTxPrefix(txID))
	defer iter.Close()
	return iter.Valid()
}

// DeletePendingAffiliateLiquidityFees discards all speculative REVSHARE
// attribution owned by a transaction. Callers perform this inside the same
// cache context as the corresponding mempool rollback.
func (k KVStore) DeletePendingAffiliateLiquidityFees(ctx cosmos.Context, txID common.TxID) {
	if txID.IsEmpty() || txID.Equals(common.BlankTxID) {
		return
	}
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	iter := cosmos.KVStorePrefixIterator(store, k.pendingAffiliateLiquidityFeeTxPrefix(txID))
	defer iter.Close()

	keys := make([][]byte, 0)
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	for _, key := range keys {
		store.Delete(key)
	}
}

// CommitPendingAffiliateLiquidityFees atomically moves one finalized inbound's
// pending entries into the normal per-block REVSHARE accumulator. Deleting the
// pending keys in the same cache commit makes repeated finality observations
// exactly-once.
func (k KVStore) CommitPendingAffiliateLiquidityFees(ctx cosmos.Context, txID common.TxID) error {
	cacheCtx, commit := ctx.CacheContext()
	entries, err := k.GetPendingAffiliateLiquidityFees(cacheCtx, txID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = k.AddAffiliateLiquidityFee(cacheCtx, entry.Thorname, entry.Fee); err != nil {
			return fmt.Errorf("fail to commit pending affiliate liquidity fee for %s: %w", entry.Thorname, err)
		}
	}
	k.DeletePendingAffiliateLiquidityFees(cacheCtx, txID)
	commit()
	return nil
}
