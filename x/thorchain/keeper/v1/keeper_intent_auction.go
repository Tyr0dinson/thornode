package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------- IntentAuction Methods --------------------

func (k KVStore) setIntentAuction(ctx cosmos.Context, key []byte, record types.IntentAuction) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
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

	// Manage active auction index - this is a single key storing all active auction intent TxIDs
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeIndexKey := k.GetKey(prefixActiveAuctionIndex, "")

	// Load current active auction index
	var activeIndex types.ActiveAuctionIndex
	if store.Has(activeIndexKey) {
		bz := store.Get(activeIndexKey)
		if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
			ctx.Logger().Error("failed to unmarshal active auction index", "error", err)
			activeIndex = types.ActiveAuctionIndex{}
		}
	}

	if auction.Status == types.IntentAuctionStatus_AuctionActive {
		// Add to active index if not already present
		found := false
		for _, txID := range activeIndex.ActiveAuctionTxIDs {
			if txID.Equals(auction.IntentTxID) {
				found = true
				break
			}
		}
		if !found {
			activeIndex.ActiveAuctionTxIDs = append(activeIndex.ActiveAuctionTxIDs, auction.IntentTxID)
		}
	} else {
		// Remove from active index if present
		newList := make([]common.TxID, 0, len(activeIndex.ActiveAuctionTxIDs))
		for _, txID := range activeIndex.ActiveAuctionTxIDs {
			if !txID.Equals(auction.IntentTxID) {
				newList = append(newList, txID)
			}
		}
		activeIndex.ActiveAuctionTxIDs = newList
	}

	// Save updated index
	buf := k.cdc.MustMarshal(&activeIndex)
	store.Set(activeIndexKey, buf)
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

// RemoveIntentAuction removes an intent auction from the KV store and updates active status index
func (k KVStore) RemoveIntentAuction(ctx cosmos.Context, intentTxID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntentAuction, intentTxID.String()))

	// Remove from active auction index
	activeIndexKey := k.GetKey(prefixActiveAuctionIndex, "")
	if !store.Has(activeIndexKey) {
		return
	}

	// Load current active auction index
	var activeIndex types.ActiveAuctionIndex
	bz := store.Get(activeIndexKey)
	if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
		ctx.Logger().Error("failed to unmarshal active auction index", "error", err)
		return
	}

	// Remove the TxID from the list
	newList := make([]common.TxID, 0, len(activeIndex.ActiveAuctionTxIDs))
	for _, id := range activeIndex.ActiveAuctionTxIDs {
		if !id.Equals(intentTxID) {
			newList = append(newList, id)
		}
	}
	activeIndex.ActiveAuctionTxIDs = newList

	// Save updated index
	buf := k.cdc.MustMarshal(&activeIndex)
	store.Set(activeIndexKey, buf)
}

// GetActiveAuctions retrieves all active intent auctions from the active status index
func (k KVStore) GetActiveAuctions(ctx cosmos.Context) ([]types.IntentAuction, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeIndexKey := k.GetKey(prefixActiveAuctionIndex, "")

	// If the index doesn't exist, return empty list
	if !store.Has(activeIndexKey) {
		return []types.IntentAuction{}, nil
	}

	// Load and unmarshal the active auction index
	var activeIndex types.ActiveAuctionIndex
	bz := store.Get(activeIndexKey)
	if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
		return nil, fmt.Errorf("failed to unmarshal active auction index: %w", err)
	}

	// Fetch all auctions from the index
	auctions := make([]types.IntentAuction, 0, len(activeIndex.ActiveAuctionTxIDs))
	for _, txID := range activeIndex.ActiveAuctionTxIDs {
		auction, err := k.GetIntentAuction(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get auction from active index", "txid", txID, "error", err)
			continue
		}
		auctions = append(auctions, auction)
	}

	return auctions, nil
}

// CleanupStaleActiveAuctions removes stale entries from the active auction index.
// This should be called from EndBlock to ensure deterministic cleanup.
func (k KVStore) CleanupStaleActiveAuctions(ctx cosmos.Context) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeIndexKey := k.GetKey(prefixActiveAuctionIndex, "")

	// If the index doesn't exist, nothing to clean up
	if !store.Has(activeIndexKey) {
		return
	}

	// Load and unmarshal the active auction index
	var activeIndex types.ActiveAuctionIndex
	bz := store.Get(activeIndexKey)
	if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
		ctx.Logger().Error("failed to unmarshal active auction index for cleanup", "error", err)
		return
	}

	// Verify each auction is actually active and filter out stale entries
	verifiedActiveTxIDs := make([]common.TxID, 0, len(activeIndex.ActiveAuctionTxIDs))
	staleEntries := false

	for _, txID := range activeIndex.ActiveAuctionTxIDs {
		// Check existence first - non-existent auctions have zero-value status (AuctionActive=0)
		if !k.IntentAuctionExists(ctx, txID) {
			ctx.Logger().Warn("removing non-existent entry from active auction index", "txid", txID)
			staleEntries = true
			continue
		}

		auction, err := k.GetIntentAuction(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get auction from active index", "txid", txID, "error", err)
			staleEntries = true
			continue
		}

		// Only keep auctions that are truly active
		if auction.Status == types.IntentAuctionStatus_AuctionActive {
			verifiedActiveTxIDs = append(verifiedActiveTxIDs, txID)
		} else {
			ctx.Logger().Warn("removing stale entry from active auction index", "txid", txID, "status", auction.Status.String())
			staleEntries = true
		}
	}

	// Update the index if any stale entries were found
	if staleEntries {
		ctx.Logger().Info("cleaned up stale entries from active auction index", "original_count", len(activeIndex.ActiveAuctionTxIDs), "verified_count", len(verifiedActiveTxIDs))
		activeIndex.ActiveAuctionTxIDs = verifiedActiveTxIDs
		buf := k.cdc.MustMarshal(&activeIndex)
		store.Set(activeIndexKey, buf)
	}
}

// -------------------- IntentAuctionBid Methods --------------------

func (k KVStore) setIntentAuctionBid(ctx cosmos.Context, key []byte, record types.IntentAuctionBid) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
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

// SetIntentAuctionBid saves an intent auction bid and updates indices
func (k KVStore) SetIntentAuctionBid(ctx cosmos.Context, bid types.IntentAuctionBid) {
	// Store the bid record
	k.setIntentAuctionBid(ctx, k.GetKey(prefixIntentAuctionBid, bid.BidTxID.String()), bid)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Index by auction (intent) ID - use IntentAuctionBidIndex with list of bid TxIDs
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, bid.IntentTxID.String())
	var index types.IntentAuctionBidIndex
	if store.Has(auctionIndexKey) {
		bz := store.Get(auctionIndexKey)
		if err := k.cdc.Unmarshal(bz, &index); err != nil {
			ctx.Logger().Error("failed to unmarshal intent auction bid index", "error", err)
			index = types.IntentAuctionBidIndex{}
		}
	}

	// Add bid TxID if not already in the index
	found := false
	for _, txID := range index.BidTxIDs {
		if txID.Equals(bid.BidTxID) {
			found = true
			break
		}
	}
	if !found {
		index.BidTxIDs = append(index.BidTxIDs, bid.BidTxID)
		buf := k.cdc.MustMarshal(&index)
		store.Set(auctionIndexKey, buf)
	}

	// Index by bidder address for finding existing bids to top-up
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, bid.IntentTxID.String(), bid.BidderAddress.String())
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

	// Update auction index - remove bid TxID from the list
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, bid.IntentTxID.String())
	if store.Has(auctionIndexKey) {
		var index types.IntentAuctionBidIndex
		bz := store.Get(auctionIndexKey)
		if err := k.cdc.Unmarshal(bz, &index); err != nil {
			ctx.Logger().Error("failed to unmarshal intent auction bid index", "error", err)
		} else {
			// Remove bid TxID from the list
			newList := make([]common.TxID, 0, len(index.BidTxIDs))
			for _, txID := range index.BidTxIDs {
				if !txID.Equals(bidTxID) {
					newList = append(newList, txID)
				}
			}
			index.BidTxIDs = newList

			// If list is empty, delete the index; otherwise update it
			if len(index.BidTxIDs) == 0 {
				store.Delete(auctionIndexKey)
			} else {
				buf := k.cdc.MustMarshal(&index)
				store.Set(auctionIndexKey, buf)
			}
		}
	}

	// Remove bidder index
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, bid.IntentTxID.String(), bid.BidderAddress.String())
	store.Delete(bidderIndexKey)
}

// GetIntentAuctionBidsByIntentID retrieves all auction bids for a specific intent auction
func (k KVStore) GetIntentAuctionBidsByIntentID(ctx cosmos.Context, intentTxID common.TxID) ([]types.IntentAuctionBid, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, intentTxID.String())

	// If the index doesn't exist, return empty list
	if !store.Has(auctionIndexKey) {
		return []types.IntentAuctionBid{}, nil
	}

	// Load and unmarshal the intent auction bid index
	var index types.IntentAuctionBidIndex
	bz := store.Get(auctionIndexKey)
	if err := k.cdc.Unmarshal(bz, &index); err != nil {
		return nil, fmt.Errorf("failed to unmarshal intent auction bid index: %w", err)
	}

	// Fetch all bid records
	bids := make([]types.IntentAuctionBid, 0, len(index.BidTxIDs))
	for _, bidTxID := range index.BidTxIDs {
		bid, err := k.GetIntentAuctionBid(ctx, bidTxID)
		if err != nil {
			ctx.Logger().Error("failed to get intent auction bid", "txid", bidTxID, "error", err)
			continue
		}
		bids = append(bids, bid)
	}

	return bids, nil
}

// GetBidderBidForAuction retrieves a bidder's existing bid for a specific auction (for top-up)
func (k KVStore) GetBidderBidForAuction(ctx cosmos.Context, intentTxID common.TxID, bidder common.Address) (types.IntentAuctionBid, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, intentTxID.String(), bidder.String())

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
