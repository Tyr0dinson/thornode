package types

import (
	"sort"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// IntentAuctionBids is a slice of IntentAuctionBid
type IntentAuctionBids []IntentAuctionBid

// HighestBid returns the highest bid amount from the slice.
// Returns zero if the slice is empty.
func (bids IntentAuctionBids) HighestBid() cosmos.Uint {
	highest := cosmos.ZeroUint()
	for _, bid := range bids {
		if bid.BidAmount.Amount.GT(highest) {
			highest = bid.BidAmount.Amount
		}
	}
	return highest
}

// IsEmpty returns true if there are no bids
func (bids IntentAuctionBids) IsEmpty() bool {
	return len(bids) == 0
}

// Len returns the number of bids
func (bids IntentAuctionBids) Len() int {
	return len(bids)
}

// SortByBidAmountDescending sorts bids from highest to lowest bid amount
func (bids IntentAuctionBids) SortByBidAmountDescending() {
	sort.SliceStable(bids, func(i, j int) bool {
		if bids[i].BidAmount.Amount.Equal(bids[j].BidAmount.Amount) {
			return bids[i].BidTxID.String() < bids[j].BidTxID.String()
		}
		return bids[i].BidAmount.Amount.GT(bids[j].BidAmount.Amount)
	})
}
