package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GetIntentAuctionIterator returns an iterator for all intent auctions
func (k KVStore) GetIntentAuctionIterator(ctx cosmos.Context) cosmos.Iterator {
	key := k.GetKey(prefixIntentAuction, "")
	return k.getIterator(ctx, kvTypes.DbPrefix(key))
}

// GetIntentAuctionBidIterator returns an iterator for all intent auction bids
func (k KVStore) GetIntentAuctionBidIterator(ctx cosmos.Context) cosmos.Iterator {
	key := k.GetKey(prefixIntentAuctionBid, "")
	return k.getIterator(ctx, kvTypes.DbPrefix(key))
}

// -------------------- IntentAuction Methods --------------------

func (k KVStore) setIntentAuction(ctx cosmos.Context, key []byte, record types.IntentAuction) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	store.Set(key, buf)
}

func (k KVStore) getIntentAuction(ctx cosmos.Context, key []byte, record *types.IntentAuction) (bool, error) {
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

// SetIntentAuction saves an intent auction to the KV store and updates active status index
func (k KVStore) SetIntentAuction(ctx cosmos.Context, auction types.IntentAuction) {
	// Save the auction
	k.setIntentAuction(ctx, k.GetKey(prefixIntentAuction, auction.IntentTxID.String()), auction)

	// Manage active auction index using prefix keys (O(1) add/remove)
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeKey := k.GetKey(prefixActiveAuctionIndex, auction.IntentTxID.String())

	if auction.Status == types.IntentAuctionStatus_AuctionActive {
		store.Set(activeKey, []byte{1})
	} else {
		store.Delete(activeKey)
	}
}

// GetIntentAuction retrieves an intent auction from the KV store
func (k KVStore) GetIntentAuction(ctx cosmos.Context, intentTxID common.TxID) (types.IntentAuction, error) {
	record := types.IntentAuction{}
	_, err := k.getIntentAuction(ctx, k.GetKey(prefixIntentAuction, intentTxID.String()), &record)
	return record, err
}

// IntentAuctionExists checks whether an intent auction exists
func (k KVStore) IntentAuctionExists(ctx cosmos.Context, intentTxID common.TxID) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return store.Has(k.GetKey(prefixIntentAuction, intentTxID.String()))
}

// RemoveIntentAuction removes an intent auction and all associated bids
func (k KVStore) RemoveIntentAuction(ctx cosmos.Context, intentTxID common.TxID) {
	// Remove all associated bids
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	if err == nil {
		for _, bid := range bids {
			k.RemoveIntentAuctionBid(ctx, bid.BidTxID)
		}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntentAuction, intentTxID.String()))

	// Remove from active auction index (O(1) with prefix keys)
	store.Delete(k.GetKey(prefixActiveAuctionIndex, intentTxID.String()))
}

// GetActiveAuctions retrieves all active intent auctions using prefix key iteration
func (k KVStore) GetActiveAuctions(ctx cosmos.Context) ([]types.IntentAuction, error) {
	key := k.GetKey(prefixActiveAuctionIndex, "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var auctions []types.IntentAuction
	for ; iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) == 0 {
			continue
		}
		txID := common.TxID(parts[len(parts)-1])

		auction, err := k.GetIntentAuction(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("failed to get auction from active index (txid=%s): %w", txID, err)
		}
		auctions = append(auctions, auction)
	}

	return auctions, nil
}

// CleanupStaleActiveAuctions removes stale entries from the active auction index.
// This should be called from EndBlock to ensure deterministic cleanup.
func (k KVStore) CleanupStaleActiveAuctions(ctx cosmos.Context) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixActiveAuctionIndex, "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var staleKeys [][]byte
	for ; iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) == 0 {
			continue
		}
		txID := common.TxID(parts[len(parts)-1])

		if !k.IntentAuctionExists(ctx, txID) {
			ctx.Logger().Warn("removing non-existent entry from active auction index", "txid", txID)
			staleKeys = append(staleKeys, iter.Key())
			continue
		}

		auction, err := k.GetIntentAuction(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get auction from active index", "txid", txID, "error", err)
			staleKeys = append(staleKeys, iter.Key())
			continue
		}

		if auction.Status != types.IntentAuctionStatus_AuctionActive {
			ctx.Logger().Warn("removing stale entry from active auction index", "txid", txID, "status", auction.Status.String())
			staleKeys = append(staleKeys, iter.Key())
		}
	}

	for _, staleKey := range staleKeys {
		store.Delete(staleKey)
	}
}

// -------------------- IntentAuctionBid Methods --------------------

func (k KVStore) setIntentAuctionBid(ctx cosmos.Context, key []byte, record types.IntentAuctionBid) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	store.Set(key, buf)
}

func (k KVStore) getIntentAuctionBid(ctx cosmos.Context, key []byte, record *types.IntentAuctionBid) (bool, error) {
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

// SetIntentAuctionBid saves an intent auction bid and updates indices.
// If the bid already exists and indexed fields have changed, old index entries
// are cleaned up before writing new ones (consistent with SetIntentSolve).
func (k KVStore) SetIntentAuctionBid(ctx cosmos.Context, bid types.IntentAuctionBid) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// If updating an existing bid, clean up stale index entries
	if k.IntentAuctionBidExists(ctx, bid.BidTxID) {
		existing, err := k.GetIntentAuctionBid(ctx, bid.BidTxID)
		if err == nil {
			// Remove old auction index if IntentTxID changed
			if !existing.IntentTxID.Equals(bid.IntentTxID) {
				oldAuctionKey := k.GetKey(prefixIntentAuctionBidByAuction, existing.IntentTxID.String(), "/"+bid.BidTxID.String())
				store.Delete(oldAuctionKey)
				oldBidderKey := k.GetKey(prefixIntentAuctionBidByBidder, existing.IntentTxID.String(), "/"+existing.BidderAddress.String())
				store.Delete(oldBidderKey)
			} else if !existing.BidderAddress.Equals(bid.BidderAddress) {
				// Remove old bidder index if BidderAddress changed
				oldBidderKey := k.GetKey(prefixIntentAuctionBidByBidder, existing.IntentTxID.String(), "/"+existing.BidderAddress.String())
				store.Delete(oldBidderKey)
			}
		}
	}

	// Store the bid record
	k.setIntentAuctionBid(ctx, k.GetKey(prefixIntentAuctionBid, bid.BidTxID.String()), bid)

	// Index by auction (intent) ID using prefix keys (O(1) add)
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, bid.IntentTxID.String(), "/"+bid.BidTxID.String())
	store.Set(auctionIndexKey, []byte{1})

	// Index by bidder address for finding existing bids to top-up
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, bid.IntentTxID.String(), "/"+bid.BidderAddress.String())
	store.Set(bidderIndexKey, []byte(bid.BidTxID.String()))
}

// GetIntentAuctionBid retrieves an intent auction bid from the KV store
func (k KVStore) GetIntentAuctionBid(ctx cosmos.Context, bidTxID common.TxID) (types.IntentAuctionBid, error) {
	record := types.IntentAuctionBid{}
	_, err := k.getIntentAuctionBid(ctx, k.GetKey(prefixIntentAuctionBid, bidTxID.String()), &record)
	return record, err
}

// IntentAuctionBidExists checks whether an intent auction bid exists
func (k KVStore) IntentAuctionBidExists(ctx cosmos.Context, bidTxID common.TxID) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return store.Has(k.GetKey(prefixIntentAuctionBid, bidTxID.String()))
}

// RemoveIntentAuctionBid removes an intent auction bid and its indices
func (k KVStore) RemoveIntentAuctionBid(ctx cosmos.Context, bidTxID common.TxID) {
	// Check if the bid record exists before proceeding
	if !k.IntentAuctionBidExists(ctx, bidTxID) {
		return
	}

	// Get the bid record to access its indices
	bid, err := k.GetIntentAuctionBid(ctx, bidTxID)
	if err != nil {
		return
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Remove main record
	store.Delete(k.GetKey(prefixIntentAuctionBid, bidTxID.String()))

	// Remove from auction index (O(1) with prefix keys)
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, bid.IntentTxID.String(), "/"+bidTxID.String())
	store.Delete(auctionIndexKey)

	// Remove bidder index
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, bid.IntentTxID.String(), "/"+bid.BidderAddress.String())
	store.Delete(bidderIndexKey)
}

// GetIntentAuctionBidsByIntentID retrieves all auction bids for a specific intent auction using prefix key iteration
func (k KVStore) GetIntentAuctionBidsByIntentID(ctx cosmos.Context, intentTxID common.TxID) ([]types.IntentAuctionBid, error) {
	key := k.GetKey(prefixIntentAuctionBidByAuction, intentTxID.String(), "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var bids []types.IntentAuctionBid
	for ; iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) < 2 {
			continue
		}
		bidTxID := common.TxID(parts[len(parts)-1])

		bid, err := k.GetIntentAuctionBid(ctx, bidTxID)
		if err != nil {
			return nil, fmt.Errorf("failed to get intent auction bid (txid=%s): %w", bidTxID, err)
		}
		bids = append(bids, bid)
	}

	return bids, nil
}

// GetBidderBidForAuction retrieves a bidder's existing bid for a specific auction (for top-up)
func (k KVStore) GetBidderBidForAuction(ctx cosmos.Context, intentTxID common.TxID, bidder common.Address) (types.IntentAuctionBid, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, intentTxID.String(), "/"+bidder.String())

	// If the index doesn't exist, return empty bid (not found)
	if !store.Has(bidderIndexKey) {
		return types.IntentAuctionBid{}, nil
	}

	// Get the bid TxID from the index
	bidTxIDStr := string(store.Get(bidderIndexKey))
	bidTxID := common.TxID(bidTxIDStr)

	// Fetch and return the bid
	return k.GetIntentAuctionBid(ctx, bidTxID)
}
