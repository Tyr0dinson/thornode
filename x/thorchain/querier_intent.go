package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (qs queryServer) queryIntent(ctx cosmos.Context, req *types.QueryIntentRequest) (*types.QueryIntentResponse, error) {
	// Validate request is not nil
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// Validate required parameter
	if req.TxId == "" {
		return nil, fmt.Errorf("tx_id is required")
	}

	// Convert string to TxID
	txID := common.TxID(req.TxId)

	// Get the intent
	intent, err := qs.mgr.Keeper().GetIntent(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("intent not found: %w", err)
	}

	// Get all solve bids for this intent
	solves, err := qs.mgr.Keeper().GetIntentSolvesByIntentID(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get intent solves: %w", err)
	}

	return &types.QueryIntentResponse{
		Intent: intent,
		Solves: solves,
	}, nil
}

func (qs queryServer) queryActiveIntents(ctx cosmos.Context, _ *types.QueryActiveIntentsRequest) (*types.QueryActiveIntentsResponse, error) {
	// Get all active intents
	activeIntents, err := qs.mgr.Keeper().GetActiveIntents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active intents: %w", err)
	}

	// Fetch each intent with its solves
	var intentsWithSolves []types.IntentWithSolves
	for _, intent := range activeIntents {
		// Get all solve bids for this intent
		solves, err := qs.mgr.Keeper().GetIntentSolvesByIntentID(ctx, intent.TxID)
		if err != nil {
			// Log error but use empty solves list
			ctx.Logger().Error("failed to get intent solves", "txid", intent.TxID, "error", err)
			solves = []types.IntentSolve{}
		}

		intentsWithSolves = append(intentsWithSolves, types.IntentWithSolves{
			Intent: intent,
			Solves: solves,
		})
	}

	return &types.QueryActiveIntentsResponse{
		Intents: intentsWithSolves,
	}, nil
}

func (qs queryServer) queryIntentAuction(ctx cosmos.Context, req *types.QueryIntentAuctionRequest) (*types.QueryIntentAuctionResponse, error) {
	// Validate request is not nil
	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// Validate required parameter
	if req.TxId == "" {
		return nil, fmt.Errorf("tx_id is required")
	}

	// Convert string to TxID
	txID := common.TxID(req.TxId)

	// Get the auction
	auction, err := qs.mgr.Keeper().GetIntentAuction(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("auction not found: %w", err)
	}

	// Get all bids for this auction
	bids, err := qs.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get auction bids: %w", err)
	}

	// Sort bids from highest to lowest
	sortedBids := types.IntentAuctionBids(bids)
	sortedBids.SortByBidAmountDescending()

	return &types.QueryIntentAuctionResponse{
		Auction: auction,
		Bids:    sortedBids,
	}, nil
}

func (qs queryServer) queryActiveAuctions(ctx cosmos.Context, _ *types.QueryActiveAuctionsRequest) (*types.QueryActiveAuctionsResponse, error) {
	// Get all active auctions
	activeAuctions, err := qs.mgr.Keeper().GetActiveAuctions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active auctions: %w", err)
	}

	// Fetch each auction with its bids
	var auctionsWithBids []types.IntentAuctionWithBids
	for _, auction := range activeAuctions {
		// Get all bids for this auction
		bids, err := qs.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, auction.IntentTxID)
		if err != nil {
			// Log error but use empty bids list
			ctx.Logger().Error("failed to get auction bids", "txid", auction.IntentTxID, "error", err)
			bids = []types.IntentAuctionBid{}
		}

		// Sort bids from highest to lowest
		sortedBids := types.IntentAuctionBids(bids)
		sortedBids.SortByBidAmountDescending()

		auctionsWithBids = append(auctionsWithBids, types.IntentAuctionWithBids{
			Auction: auction,
			Bids:    sortedBids,
		})
	}

	return &types.QueryActiveAuctionsResponse{
		Auctions: auctionsWithBids,
	}, nil
}
