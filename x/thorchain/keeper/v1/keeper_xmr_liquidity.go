package keeperv1

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func (k KVStore) GetXMRLiquidityReceipts(ctx cosmos.Context, txID common.TxID) ([]kvTypes.XMRLiquidityReceipt, error) {
	var receipts []kvTypes.XMRLiquidityReceipt
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if data := store.Get(k.GetKey(prefixXMRLiquidityReceipt, txID.String())); data != nil {
		if err := json.Unmarshal(data, &receipts); err != nil {
			return nil, fmt.Errorf("invalid XMR LP receipt: %w", err)
		}
	}
	return receipts, nil
}

func (k KVStore) SetXMRLiquidityReceipts(ctx cosmos.Context, txID common.TxID, receipts []kvTypes.XMRLiquidityReceipt) error {
	data, err := json.Marshal(receipts)
	if err != nil {
		return err
	}
	runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx)).Set(k.GetKey(prefixXMRLiquidityReceipt, txID.String()), data)
	return nil
}

func (k KVStore) GetXMRLiquidityTxIDs(ctx cosmos.Context, owner common.Address) ([]common.TxID, error) {
	var ids []string
	_, err := k.getStrings(ctx, k.GetKey(prefixXMRLiquidityIndex, hex.EncodeToString([]byte(owner))), &ids)
	if err != nil {
		return nil, err
	}
	result := make([]common.TxID, len(ids))
	for i, id := range ids {
		result[i], err = common.NewTxID(id)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (k KVStore) SetXMRLiquidityTxIDs(ctx cosmos.Context, owner common.Address, txIDs []common.TxID) error {
	// GetKey uppercases keys; encode address bytes first to preserve Monero case.
	key := k.GetKey(prefixXMRLiquidityIndex, hex.EncodeToString([]byte(owner)))
	if len(txIDs) == 0 {
		k.del(ctx, key)
		return nil
	}
	ids := make([]string, len(txIDs))
	for i, id := range txIDs {
		ids[i] = id.String()
	}
	k.setStrings(ctx, key, ids)
	return nil
}

// CancelXMRErrataSwaps removes every admitted index for an orphaned inbound.
// Prefix iteration is bounded to this tx ID, including sparse affiliate indices.
// Removal is not settlement: neither held output nor phantom input is paid out.
func (k KVStore) CancelXMRErrataSwaps(ctx cosmos.Context, txID common.TxID) (cosmos.Uint, error) {
	cacheCtx, commit := ctx.CacheContext()
	unexecuted := cosmos.ZeroUint()
	streamCounted := false
	for _, prefix := range []kvTypes.DbPrefix{prefixSwapQueueItem, prefixAdvSwapQueueItem} {
		iter := k.getIterator(cacheCtx, kvTypes.DbPrefix(k.GetKey(prefix, txID.String()+"-")))
		var indices []int
		for ; iter.Valid(); iter.Next() {
			key := string(iter.Key())
			index, err := strconv.Atoi(key[strings.LastIndex(key, "-")+1:])
			if err != nil {
				iter.Close()
				return cosmos.ZeroUint(), fmt.Errorf("invalid orphan swap index: %w", err)
			}
			var msg MsgSwap
			if err := k.cdc.Unmarshal(iter.Value(), &msg); err != nil {
				iter.Close()
				return cosmos.ZeroUint(), err
			}
			if prefix == prefixAdvSwapQueueItem {
				if msg.State == nil {
					iter.Close()
					return cosmos.ZeroUint(), fmt.Errorf("missing orphan advanced swap state")
				}
				unexecuted = unexecuted.Add(common.SafeSub(msg.State.Deposit, msg.State.In))
			} else if msg.IsLegacyStreaming() {
				if !streamCounted {
					stream := msg.GetStreamingSwap()
					if k.StreamingSwapExists(cacheCtx, txID) {
						var err error
						stream, err = k.GetStreamingSwap(cacheCtx, txID)
						if err != nil {
							iter.Close()
							return cosmos.ZeroUint(), err
						}
					}
					unexecuted = unexecuted.Add(common.SafeSub(stream.Deposit, stream.In))
					streamCounted = true
				}
			} else if len(msg.Tx.Coins) == 1 {
				unexecuted = unexecuted.Add(msg.Tx.Coins[0].Amount)
			}
			indices = append(indices, index)
		}
		iter.Close()
		for _, index := range indices {
			if prefix == prefixAdvSwapQueueItem {
				if err := k.RemoveAdvSwapQueueItem(cacheCtx, txID, index); err != nil {
					return cosmos.ZeroUint(), err
				}
			} else {
				k.RemoveSwapQueueItem(cacheCtx, txID, index)
			}
		}
	}
	k.RemoveStreamingSwap(cacheCtx, txID)
	// Stale TTL references are harmless: their item is gone, so expiry cannot
	// settle/refund it and removes the reference at its original height.
	commit()
	return unexecuted, nil
}
