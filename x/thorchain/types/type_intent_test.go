package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type IntentAuctionBidsSuite struct{}

var _ = Suite(&IntentAuctionBidsSuite{})

func (s *IntentAuctionBidsSuite) TestHighestBid(c *C) {
	// Empty bids should return zero
	bids := IntentAuctionBids{}
	c.Check(bids.HighestBid().Equal(cosmos.ZeroUint()), Equals, true)

	// Single bid
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	c.Check(bids.HighestBid().Equal(cosmos.NewUint(100)), Equals, true)

	// Multiple bids - highest in middle
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(50))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	c.Check(bids.HighestBid().Equal(cosmos.NewUint(200)), Equals, true)

	// Multiple bids - highest at end
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(10))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(20))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(300))},
	}
	c.Check(bids.HighestBid().Equal(cosmos.NewUint(300)), Equals, true)

	// Multiple bids - highest at start
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(500))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(20))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	c.Check(bids.HighestBid().Equal(cosmos.NewUint(500)), Equals, true)
}

func (s *IntentAuctionBidsSuite) TestIsEmpty(c *C) {
	// Empty slice
	bids := IntentAuctionBids{}
	c.Check(bids.IsEmpty(), Equals, true)

	// Non-empty slice
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	c.Check(bids.IsEmpty(), Equals, false)

	// Multiple bids
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
	}
	c.Check(bids.IsEmpty(), Equals, false)
}

func (s *IntentAuctionBidsSuite) TestLen(c *C) {
	// Empty slice
	bids := IntentAuctionBids{}
	c.Check(bids.Len(), Equals, 0)

	// Single bid
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	c.Check(bids.Len(), Equals, 1)

	// Multiple bids
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(300))},
	}
	c.Check(bids.Len(), Equals, 3)
}

func (s *IntentAuctionBidsSuite) TestSortByBidAmountDescending(c *C) {
	// Empty slice should not panic
	bids := IntentAuctionBids{}
	bids.SortByBidAmountDescending()
	c.Check(bids.Len(), Equals, 0)

	// Single bid remains unchanged
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	bids.SortByBidAmountDescending()
	c.Check(bids[0].BidAmount.Amount.Equal(cosmos.NewUint(100)), Equals, true)

	// Multiple bids sorted descending
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(50))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(300))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(25))},
	}
	bids.SortByBidAmountDescending()
	c.Check(bids[0].BidAmount.Amount.Equal(cosmos.NewUint(300)), Equals, true)
	c.Check(bids[1].BidAmount.Amount.Equal(cosmos.NewUint(200)), Equals, true)
	c.Check(bids[2].BidAmount.Amount.Equal(cosmos.NewUint(100)), Equals, true)
	c.Check(bids[3].BidAmount.Amount.Equal(cosmos.NewUint(50)), Equals, true)
	c.Check(bids[4].BidAmount.Amount.Equal(cosmos.NewUint(25)), Equals, true)

	// Already sorted should remain sorted
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(300))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
	}
	bids.SortByBidAmountDescending()
	c.Check(bids[0].BidAmount.Amount.Equal(cosmos.NewUint(300)), Equals, true)
	c.Check(bids[1].BidAmount.Amount.Equal(cosmos.NewUint(200)), Equals, true)
	c.Check(bids[2].BidAmount.Amount.Equal(cosmos.NewUint(100)), Equals, true)

	// Reverse sorted should be sorted descending
	bids = IntentAuctionBids{
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(200))},
		{BidAmount: common.NewCoin(common.RuneAsset(), cosmos.NewUint(300))},
	}
	bids.SortByBidAmountDescending()
	c.Check(bids[0].BidAmount.Amount.Equal(cosmos.NewUint(300)), Equals, true)
	c.Check(bids[1].BidAmount.Amount.Equal(cosmos.NewUint(200)), Equals, true)
	c.Check(bids[2].BidAmount.Amount.Equal(cosmos.NewUint(100)), Equals, true)
}
