package keeperv1

import (
	"bytes"
	"fmt"
	"strings"

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
	// Walk the raw auction index so parent cleanup still works even when one of
	// the child bid records is corrupted and cannot be decoded.
	k.removeIntentAuctionBidsByIntentTxID(ctx, intentTxID)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntentAuction, intentTxID.String()))

	// Remove from active auction index (O(1) with prefix keys)
	store.Delete(k.GetKey(prefixActiveAuctionIndex, intentTxID.String()))
}

// GetActiveAuctions retrieves all active intent auctions using prefix key iteration
func (k KVStore) GetActiveAuctions(ctx cosmos.Context) ([]types.IntentAuction, error) {
	activeTxIDs := k.GetActiveAuctionTxIDs(ctx)
	auctions := make([]types.IntentAuction, 0, len(activeTxIDs))
	for _, txID := range activeTxIDs {
		auction, err := k.GetIntentAuction(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("failed to get auction from active index (txid=%s): %w", txID, err)
		}
		auctions = append(auctions, auction)
	}

	return auctions, nil
}

// GetActiveAuctionTxIDs retrieves all intent txids from the active auction index.
func (k KVStore) GetActiveAuctionTxIDs(ctx cosmos.Context) []common.TxID {
	prefix := k.GetKey(prefixActiveAuctionIndex, "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(prefix))
	defer iter.Close()

	var txIDs []common.TxID
	for ; iter.Valid(); iter.Next() {
		txIDSuffix := keySuffixAfterPrefix(iter.Key(), prefix)
		if len(txIDSuffix) == 0 {
			continue
		}
		txIDs = append(txIDs, common.TxID(string(txIDSuffix)))
	}

	return txIDs
}

// RemoveActiveAuctionIndex deletes an entry from the active auction index.
func (k KVStore) RemoveActiveAuctionIndex(ctx cosmos.Context, intentTxID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixActiveAuctionIndex, intentTxID.String()))
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
			}
			// Remove the old bidder index whenever either side of the mapping changes.
			if !existing.IntentTxID.Equals(bid.IntentTxID) || !existing.BidderAddress.Equals(bid.BidderAddress) {
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
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Sweep stale indexes even when the primary bid record is already gone.
	if !store.Has(k.GetKey(prefixIntentAuctionBid, bidTxID.String())) {
		k.removeIntentAuctionBidIndexEntriesByTxID(ctx, bidTxID)
		return
	}

	// Get the bid record to access its indices
	bid, err := k.GetIntentAuctionBid(ctx, bidTxID)
	if err != nil {
		// If the record body is corrupted, we can no longer reconstruct the
		// auction/bidder index keys from decoded fields. Remove the primary key
		// and then best-effort sweep any index entries that still reference it.
		ctx.Logger().Error("failed to decode intent auction bid during removal; deleting primary record and best-effort index entries", "bid_txid", bidTxID, "error", err)
		store.Delete(k.GetKey(prefixIntentAuctionBid, bidTxID.String()))
		k.removeIntentAuctionBidIndexEntriesByTxID(ctx, bidTxID)
		return
	}

	// Remove main record
	store.Delete(k.GetKey(prefixIntentAuctionBid, bidTxID.String()))

	// Remove from auction index (O(1) with prefix keys)
	auctionIndexKey := k.GetKey(prefixIntentAuctionBidByAuction, bid.IntentTxID.String(), "/"+bidTxID.String())
	store.Delete(auctionIndexKey)

	// Remove bidder index
	bidderIndexKey := k.GetKey(prefixIntentAuctionBidByBidder, bid.IntentTxID.String(), "/"+bid.BidderAddress.String())
	store.Delete(bidderIndexKey)
}

// removeIntentAuctionBidsByIntentTxID best-effort removes all bids referenced
// by an auction's child index, even when typed iteration fails due to
// corruption.
func (k KVStore) removeIntentAuctionBidsByIntentTxID(ctx cosmos.Context, intentTxID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefix := k.GetKey(prefixIntentAuctionBidByAuction, intentTxID.String(), "/")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(prefix))
	var bidTxIDs []common.TxID
	for ; iter.Valid(); iter.Next() {
		bidTxIDSuffix := keySuffixAfterPrefix(iter.Key(), prefix)
		if len(bidTxIDSuffix) == 0 {
			continue
		}
		bidTxIDs = append(bidTxIDs, common.TxID(string(bidTxIDSuffix)))
	}
	iter.Close()

	for _, bidTxID := range bidTxIDs {
		k.RemoveIntentAuctionBid(ctx, bidTxID)
	}

	// Delete any leftover auction-index entries for this parent after per-bid
	// cleanup so parent removal cannot strand stale child references.
	iter = k.getIterator(ctx, kvTypes.DbPrefix(prefix))
	var staleKeys [][]byte
	for ; iter.Valid(); iter.Next() {
		staleKeys = append(staleKeys, append([]byte(nil), iter.Key()...))
	}
	iter.Close()
	for _, key := range staleKeys {
		store.Delete(key)
	}
}

// removeIntentAuctionBidIndexEntriesByTxID best-effort sweeps bid index entries
// by bid txid. This is used when the primary bid record is missing or
// corrupted and its indexed fields can no longer be trusted.
func (k KVStore) removeIntentAuctionBidIndexEntriesByTxID(ctx cosmos.Context, bidTxID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bidTxIDSuffix := []byte("/" + strings.ToUpper(bidTxID.String()))

	auctionIter := k.getIterator(ctx, prefixIntentAuctionBidByAuction)
	var staleAuctionKeys [][]byte
	for ; auctionIter.Valid(); auctionIter.Next() {
		// Auction index keys end with "/<bid_txid>", so suffix matching is
		// enough to find stale references without decoding the bid record.
		if bytes.HasSuffix(auctionIter.Key(), bidTxIDSuffix) {
			staleAuctionKeys = append(staleAuctionKeys, append([]byte(nil), auctionIter.Key()...))
		}
	}
	auctionIter.Close()
	// Delete after iteration to avoid mutating the store while walking it.
	for _, key := range staleAuctionKeys {
		store.Delete(key)
	}

	bidderIter := k.getIterator(ctx, prefixIntentAuctionBidByBidder)
	var staleBidderKeys [][]byte
	for ; bidderIter.Valid(); bidderIter.Next() {
		// Bidder index values store the bid txid directly, so value matching is
		// enough to locate stale bidder references.
		if strings.EqualFold(string(bidderIter.Value()), bidTxID.String()) {
			staleBidderKeys = append(staleBidderKeys, append([]byte(nil), bidderIter.Key()...))
		}
	}
	bidderIter.Close()
	// Delete after iteration to avoid mutating the store while walking it.
	for _, key := range staleBidderKeys {
		store.Delete(key)
	}
}

// GetIntentAuctionBidsByIntentID retrieves all auction bids for a specific intent auction using prefix key iteration
func (k KVStore) GetIntentAuctionBidsByIntentID(ctx cosmos.Context, intentTxID common.TxID) ([]types.IntentAuctionBid, error) {
	prefix := k.GetKey(prefixIntentAuctionBidByAuction, intentTxID.String(), "/")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(prefix))
	defer iter.Close()

	var bids []types.IntentAuctionBid
	for ; iter.Valid(); iter.Next() {
		bidTxIDSuffix := keySuffixAfterPrefix(iter.Key(), prefix)
		if len(bidTxIDSuffix) == 0 {
			continue
		}
		bidTxID := common.TxID(string(bidTxIDSuffix))

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
