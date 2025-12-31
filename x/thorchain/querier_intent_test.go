package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type QuerierIntentSuite struct{}

var _ = Suite(&QuerierIntentSuite{})

func (s *QuerierIntentSuite) TestQueryIntent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	// Test: empty TxID returns error
	req := &types.QueryIntentRequest{TxId: ""}
	_, err := qs.queryIntent(ctx, req)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "tx_id is required")

	// Create an intent for testing
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent := types.Intent{
		TxID:          txID,
		SourceAddress: common.Address("thor1sender"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: common.Address("bc1qtest"),
		HighTarget:    cosmos.NewUint(20),
		LowTarget:     cosmos.NewUint(10),
		ExpiryHeight:  1000,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent)

	// Test: valid TxID returns intent
	req = &types.QueryIntentRequest{TxId: txID.String()}
	resp, err := qs.queryIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Intent.TxID.Equals(txID), Equals, true)
	c.Assert(resp.Intent.Status, Equals, types.IntentStatus_IntentActive)
	c.Assert(len(resp.Solves), Equals, 0)

	// Add a solve for this intent
	solveTxID := common.TxID("SOLVE1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    txID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   500,
	}
	mgr.Keeper().SetIntentSolve(ctx, solve)

	// Test: intent with solve returns both
	resp, err = qs.queryIntent(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Intent.TxID.Equals(txID), Equals, true)
	c.Assert(len(resp.Solves), Equals, 1)
	c.Assert(resp.Solves[0].SolveTxID.Equals(solveTxID), Equals, true)
}

func (s *QuerierIntentSuite) TestQueryActiveIntents(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	// Test: no active intents returns empty list
	resp, err := qs.queryActiveIntents(ctx, &types.QueryActiveIntentsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Intents), Equals, 0)

	// Create an active intent
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent1 := types.Intent{
		TxID:          txID1,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: common.Address("bc1qtest1"),
		HighTarget:    cosmos.NewUint(20),
		LowTarget:     cosmos.NewUint(10),
		ExpiryHeight:  1000,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent1)

	// Test: one active intent
	resp, err = qs.queryActiveIntents(ctx, &types.QueryActiveIntentsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Intents), Equals, 1)
	c.Assert(resp.Intents[0].Intent.TxID.Equals(txID1), Equals, true)
	c.Assert(len(resp.Intents[0].Solves), Equals, 0)

	// Create a second active intent
	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent2 := types.Intent{
		TxID:          txID2,
		SourceAddress: common.Address("thor1sender2"),
		Source:        common.NewCoin(common.ETHAsset, cosmos.NewUint(200)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: common.Address("bc1qtest2"),
		HighTarget:    cosmos.NewUint(30),
		LowTarget:     cosmos.NewUint(20),
		ExpiryHeight:  2000,
		Status:        types.IntentStatus_IntentActive,
	}
	mgr.Keeper().SetIntent(ctx, intent2)

	// Test: two active intents
	resp, err = qs.queryActiveIntents(ctx, &types.QueryActiveIntentsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Intents), Equals, 2)

	// Create a cancelled intent (should not be in active list)
	txID3 := common.TxID("CCCC1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent3 := types.Intent{
		TxID:          txID3,
		SourceAddress: common.Address("thor1sender3"),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(300)),
		TargetAsset:   common.ETHAsset,
		Status:        types.IntentStatus_IntentCancelled,
	}
	mgr.Keeper().SetIntent(ctx, intent3)

	// Test: still only two active intents
	resp, err = qs.queryActiveIntents(ctx, &types.QueryActiveIntentsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Intents), Equals, 2)

	// Add a solve to one of the active intents
	solveTxID := common.TxID("SOLVE1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    txID1,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   500,
	}
	mgr.Keeper().SetIntentSolve(ctx, solve)

	// Test: active intents include solves
	resp, err = qs.queryActiveIntents(ctx, &types.QueryActiveIntentsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Intents), Equals, 2)
	// Find the intent with solves
	var foundSolve bool
	for _, iws := range resp.Intents {
		if iws.Intent.TxID.Equals(txID1) {
			c.Assert(len(iws.Solves), Equals, 1)
			foundSolve = true
		}
	}
	c.Assert(foundSolve, Equals, true)
}

func (s *QuerierIntentSuite) TestQueryIntentAuction(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	// Test: empty TxID returns error
	req := &types.QueryIntentAuctionRequest{TxId: ""}
	_, err := qs.queryIntentAuction(ctx, req)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "tx_id is required")

	// Create an auction for testing
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
	mgr.Keeper().SetIntentAuction(ctx, auction)

	// Test: valid TxID returns auction
	req = &types.QueryIntentAuctionRequest{TxId: intentTxID.String()}
	resp, err := qs.queryIntentAuction(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Auction.IntentTxID.Equals(intentTxID), Equals, true)
	c.Assert(resp.Auction.Status, Equals, types.IntentAuctionStatus_AuctionActive)
	c.Assert(len(resp.Bids), Equals, 0)

	// Add bids to the auction
	bid1 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder1"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
		SubmitHeight:  150,
	}
	mgr.Keeper().SetIntentAuctionBid(ctx, bid1)

	bid2 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder2"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(700)), // Higher bid
		SubmitHeight:  160,
	}
	mgr.Keeper().SetIntentAuctionBid(ctx, bid2)

	// Test: auction with bids returns both, sorted by amount descending
	resp, err = qs.queryIntentAuction(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(resp.Auction.IntentTxID.Equals(intentTxID), Equals, true)
	c.Assert(len(resp.Bids), Equals, 2)
	// Verify bids are sorted by amount descending (highest first)
	c.Assert(resp.Bids[0].BidAmount.Amount.GT(resp.Bids[1].BidAmount.Amount), Equals, true)
}

func (s *QuerierIntentSuite) TestQueryActiveAuctions(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	// Test: no active auctions returns empty list
	resp, err := qs.queryActiveAuctions(ctx, &types.QueryActiveAuctionsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Auctions), Equals, 0)

	// Create an active auction
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction1 := types.IntentAuction{
		IntentTxID:      txID1,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
		BidAsset:        common.BTCAsset,
		MinBid:          cosmos.NewUint(10),
		StartHeight:     100,
		ExpiryHeight:    1000,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender1"),
	}
	mgr.Keeper().SetIntentAuction(ctx, auction1)

	// Test: one active auction
	resp, err = qs.queryActiveAuctions(ctx, &types.QueryActiveAuctionsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Auctions), Equals, 1)
	c.Assert(resp.Auctions[0].Auction.IntentTxID.Equals(txID1), Equals, true)
	c.Assert(len(resp.Auctions[0].Bids), Equals, 0)

	// Create a second active auction
	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction2 := types.IntentAuction{
		IntentTxID:      txID2,
		AuctionAmount:   common.NewCoin(common.ETHAsset, cosmos.NewUint(2000)),
		RemainingAmount: common.NewCoin(common.ETHAsset, cosmos.NewUint(2000)),
		BidAsset:        common.BTCAsset,
		MinBid:          cosmos.NewUint(20),
		StartHeight:     200,
		ExpiryHeight:    2000,
		Status:          types.IntentAuctionStatus_AuctionActive,
		OwnerAddress:    common.Address("thor1sender2"),
	}
	mgr.Keeper().SetIntentAuction(ctx, auction2)

	// Test: two active auctions
	resp, err = qs.queryActiveAuctions(ctx, &types.QueryActiveAuctionsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Auctions), Equals, 2)

	// Create a completed auction (should not be in active list)
	txID3 := common.TxID("CCCC1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	auction3 := types.IntentAuction{
		IntentTxID:      txID3,
		AuctionAmount:   common.NewCoin(common.BTCAsset, cosmos.NewUint(3000)),
		RemainingAmount: common.NewCoin(common.BTCAsset, cosmos.ZeroUint()),
		BidAsset:        common.ETHAsset,
		Status:          types.IntentAuctionStatus_AuctionCompleted,
		OwnerAddress:    common.Address("thor1sender3"),
	}
	mgr.Keeper().SetIntentAuction(ctx, auction3)

	// Test: still only two active auctions
	resp, err = qs.queryActiveAuctions(ctx, &types.QueryActiveAuctionsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Auctions), Equals, 2)

	// Add bids to one of the active auctions
	bid1 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    txID1,
		BidderAddress: common.Address("thor1bidder1"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(500)),
	}
	mgr.Keeper().SetIntentAuctionBid(ctx, bid1)

	bid2 := types.IntentAuctionBid{
		BidTxID:       common.TxID("BID21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"),
		IntentTxID:    txID1,
		BidderAddress: common.Address("thor1bidder2"),
		BidAmount:     common.NewCoin(common.BTCAsset, cosmos.NewUint(700)),
	}
	mgr.Keeper().SetIntentAuctionBid(ctx, bid2)

	// Test: active auctions include bids, sorted by amount descending
	resp, err = qs.queryActiveAuctions(ctx, &types.QueryActiveAuctionsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Auctions), Equals, 2)
	// Find the auction with bids
	var foundBids bool
	for _, awb := range resp.Auctions {
		if awb.Auction.IntentTxID.Equals(txID1) {
			c.Assert(len(awb.Bids), Equals, 2)
			// Verify bids are sorted by amount descending
			c.Assert(awb.Bids[0].BidAmount.Amount.GT(awb.Bids[1].BidAmount.Amount), Equals, true)
			foundBids = true
		}
	}
	c.Assert(foundBids, Equals, true)
}
