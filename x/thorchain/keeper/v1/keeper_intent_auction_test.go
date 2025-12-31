package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperIntentAuctionSuite struct{}

var _ = Suite(&KeeperIntentAuctionSuite{})

func (KeeperIntentAuctionSuite) TestIntentAuction_SetAndGet(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction := types.IntentAuction{
		IntentTxID:      intentTxID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		MinBid:          cosmos.NewUint(10),
		StartHeight:     100,
		ExpiryHeight:    1000,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender"),
	}

	// Set auction
	k.SetIntentAuction(ctx, auction)

	// Get auction
	retrieved, err := k.GetIntentAuction(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(retrieved.IntentTxID.Equals(intentTxID), Equals, true)
	c.Check(retrieved.Status, Equals, types.IntentAuctionStatus_AuctionActive)
	c.Check(retrieved.AuctionAmount.Asset.Equals(common.DOGEAsset), Equals, true)
	c.Check(retrieved.BidAsset.Equals(common.BTCAsset), Equals, true)
	c.Check(retrieved.ExpiryHeight, Equals, int64(1000))
	c.Check(retrieved.StartHeight, Equals, int64(100))
}

func (KeeperIntentAuctionSuite) TestIntentAuction_Exists(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction := types.IntentAuction{
		IntentTxID:      intentTxID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender"),
	}

	// Check non-existence
	exists := k.IntentAuctionExists(ctx, intentTxID)
	c.Check(exists, Equals, false)

	// Set and check existence
	k.SetIntentAuction(ctx, auction)
	exists = k.IntentAuctionExists(ctx, intentTxID)
	c.Check(exists, Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuction_ActiveIndex(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create active auction
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction1 := types.IntentAuction{
		IntentTxID:      txID1,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender1"),
	}
	k.SetIntentAuction(ctx, auction1)

	// Create finalized auction (not active)
	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction2 := types.IntentAuction{
		IntentTxID:      txID2,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionCompleted,
		OwnerAddress:    common.Address("thor1sender2"),
	}
	k.SetIntentAuction(ctx, auction2)

	// Get active auctions - should only return the active one
	activeAuctions, err := k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 1)
	c.Check(activeAuctions[0].IntentTxID.Equals(txID1), Equals, true)
	c.Check(activeAuctions[0].Status, Equals, types.IntentAuctionStatus_AuctionActive)

	// Add another active auction
	txID3 := common.TxID("CCCC1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction3 := types.IntentAuction{
		IntentTxID:      txID3,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender3"),
	}
	k.SetIntentAuction(ctx, auction3)

	// Get active auctions - should return both active ones
	activeAuctions, err = k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 2)

	// Update first auction to finalized
	auction1.Status = types.IntentAuctionStatus_AuctionCompleted
	k.SetIntentAuction(ctx, auction1)

	// Get active auctions - should only return the third one now
	activeAuctions, err = k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 1)
	c.Check(activeAuctions[0].IntentTxID.Equals(txID3), Equals, true)
	c.Check(activeAuctions[0].Status, Equals, types.IntentAuctionStatus_AuctionActive)

	// Update third auction to cancelled
	auction3.Status = types.IntentAuctionStatus_AuctionCancelled
	k.SetIntentAuction(ctx, auction3)

	// Get active auctions - should return empty list
	activeAuctions, err = k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 0)
}

func (KeeperIntentAuctionSuite) TestIntentAuction_RemoveIntentAuction(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create two active auctions
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction1 := types.IntentAuction{
		IntentTxID:      txID1,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender1"),
	}
	k.SetIntentAuction(ctx, auction1)

	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction2 := types.IntentAuction{
		IntentTxID:      txID2,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender2"),
	}
	k.SetIntentAuction(ctx, auction2)

	// Verify both are in active index
	activeAuctions, err := k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 2)

	// Remove first auction
	k.RemoveIntentAuction(ctx, txID1)

	// Verify it no longer exists
	c.Check(k.IntentAuctionExists(ctx, txID1), Equals, false)

	// Verify it's removed from active index
	activeAuctions, err = k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 1)
	c.Check(activeAuctions[0].IntentTxID.Equals(txID2), Equals, true)
	c.Check(activeAuctions[0].Status, Equals, types.IntentAuctionStatus_AuctionActive)

	// Remove second auction
	k.RemoveIntentAuction(ctx, txID2)

	// Verify active index is now empty
	activeAuctions, err = k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 0)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_SetAndGet(c *C) {
	ctx, k := setupKeeperForTest(c)

	bidTxID := common.TxID("BID01234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	bid := types.IntentAuctionBid{
		BidTxID:       bidTxID,
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
		SubmitHeight:  100,
	}

	// Set bid
	k.SetIntentAuctionBid(ctx, bid)

	// Get bid
	retrieved, err := k.GetIntentAuctionBid(ctx, bidTxID)
	c.Assert(err, IsNil)
	c.Check(retrieved.BidTxID.Equals(bidTxID), Equals, true)
	c.Check(retrieved.IntentTxID.Equals(intentTxID), Equals, true)
	c.Check(retrieved.BidAmount.Amount.Equal(cosmos.NewUint(500)), Equals, true)
	c.Check(retrieved.SubmitHeight, Equals, int64(100))
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_Exists(c *C) {
	ctx, k := setupKeeperForTest(c)

	bidTxID := common.TxID("BID01234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	bid := types.IntentAuctionBid{
		BidTxID:       bidTxID,
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}

	// Check non-existence
	exists := k.IntentAuctionBidExists(ctx, bidTxID)
	c.Check(exists, Equals, false)

	// Set and check existence
	k.SetIntentAuctionBid(ctx, bid)
	exists = k.IntentAuctionBidExists(ctx, bidTxID)
	c.Check(exists, Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_GetByIntentID(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	// Create multiple bids for the same auction
	bid1 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder1"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}
	k.SetIntentAuctionBid(ctx, bid1)

	bid2 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder2"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(600)),
	}
	k.SetIntentAuctionBid(ctx, bid2)

	// Get bids by intent ID
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 2)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_GetBidderBidForAuction(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	bidder1 := common.Address("thor1bidder1")
	bidder2 := common.Address("thor1bidder2")

	bid1 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: bidder1,
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}
	k.SetIntentAuctionBid(ctx, bid1)

	bid2 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: bidder2,
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(600)),
	}
	k.SetIntentAuctionBid(ctx, bid2)

	// Get bidder1's bid for the auction
	retrievedBid, err := k.GetBidderBidForAuction(ctx, intentTxID, bidder1)
	c.Assert(err, IsNil)
	c.Check(retrievedBid.BidTxID.Equals(bid1.BidTxID), Equals, true)
	c.Check(retrievedBid.BidAmount.Amount.Equal(cosmos.NewUint(500)), Equals, true)

	// Get bidder2's bid for the auction
	retrievedBid, err = k.GetBidderBidForAuction(ctx, intentTxID, bidder2)
	c.Assert(err, IsNil)
	c.Check(retrievedBid.BidTxID.Equals(bid2.BidTxID), Equals, true)
	c.Check(retrievedBid.BidAmount.Amount.Equal(cosmos.NewUint(600)), Equals, true)

	// Get non-existent bidder's bid
	nonExistentBidder := common.Address("thor1unknown")
	retrievedBid, err = k.GetBidderBidForAuction(ctx, intentTxID, nonExistentBidder)
	c.Assert(err, IsNil)
	c.Check(retrievedBid.BidTxID.IsEmpty(), Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_Remove(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	bidder := common.Address("thor1bidder")
	bidTxID := common.TxID("BID01234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")

	bid := types.IntentAuctionBid{
		BidTxID:       bidTxID,
		IntentTxID:    intentTxID,
		BidderAddress: bidder,
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}

	// Set and verify
	k.SetIntentAuctionBid(ctx, bid)
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID), Equals, true)

	// Remove
	k.RemoveIntentAuctionBid(ctx, bidTxID)

	// Verify removed
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID), Equals, false)

	// Verify indices also removed
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 0)

	// Verify bidder index also removed
	retrievedBid, err := k.GetBidderBidForAuction(ctx, intentTxID, bidder)
	c.Assert(err, IsNil)
	c.Check(retrievedBid.BidTxID.IsEmpty(), Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_RemoveMultiple(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	// Create multiple bids
	bid1 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder1"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}
	k.SetIntentAuctionBid(ctx, bid1)

	bid2 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder2"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(600)),
	}
	k.SetIntentAuctionBid(ctx, bid2)

	// Verify both exist
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 2)

	// Remove first bid
	k.RemoveIntentAuctionBid(ctx, bid1.BidTxID)

	// Verify only second bid remains
	bids, err = k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 1)
	c.Check(bids[0].BidTxID.Equals(bid2.BidTxID), Equals, true)

	// Remove second bid
	k.RemoveIntentAuctionBid(ctx, bid2.BidTxID)

	// Verify no bids remain
	bids, err = k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 0)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_UpdateExisting(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	bidder := common.Address("thor1bidder")
	bidTxID := common.TxID("BID01234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")

	// Create initial bid
	bid := types.IntentAuctionBid{
		BidTxID:       bidTxID,
		IntentTxID:    intentTxID,
		BidderAddress: bidder,
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
		SubmitHeight:  100,
	}
	k.SetIntentAuctionBid(ctx, bid)

	// Verify initial state
	retrieved, err := k.GetIntentAuctionBid(ctx, bidTxID)
	c.Assert(err, IsNil)
	c.Check(retrieved.BidAmount.Amount.Equal(cosmos.NewUint(500)), Equals, true)

	// Update bid (simulate top-up)
	bid.BidAmount = common.NewCoin(common.BTCAsset, cosmos.NewUint(800))
	bid.SubmitHeight = 150
	k.SetIntentAuctionBid(ctx, bid)

	// Verify updated state
	retrieved, err = k.GetIntentAuctionBid(ctx, bidTxID)
	c.Assert(err, IsNil)
	c.Check(retrieved.BidAmount.Amount.Equal(cosmos.NewUint(800)), Equals, true)
	c.Check(retrieved.SubmitHeight, Equals, int64(150))

	// Verify only one bid exists in the index (no duplicate)
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 1)
}

func (KeeperIntentAuctionSuite) TestIntentAuction_GetNonExistent(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Try to get a non-existent auction
	nonExistentTxID := common.TxID("ZZZZ1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction, err := k.GetIntentAuction(ctx, nonExistentTxID)
	c.Assert(err, IsNil)
	c.Check(auction.IntentTxID.IsEmpty(), Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_GetNonExistent(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Try to get a non-existent bid
	nonExistentBidTxID := common.TxID("ZZZZ1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	bid, err := k.GetIntentAuctionBid(ctx, nonExistentBidTxID)
	c.Assert(err, IsNil)
	c.Check(bid.BidTxID.IsEmpty(), Equals, true)
}

func (KeeperIntentAuctionSuite) TestIntentAuction_GetActiveAuctionsEmpty(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Get active auctions when none exist
	activeAuctions, err := k.GetActiveAuctions(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeAuctions), Equals, 0)
}

func (KeeperIntentAuctionSuite) TestIntentAuctionBid_GetByIntentIDEmpty(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Get bids for a non-existent auction
	nonExistentIntentTxID := common.TxID("ZZZZ1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, nonExistentIntentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 0)
}
