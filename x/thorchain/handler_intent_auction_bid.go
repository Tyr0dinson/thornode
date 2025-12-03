package thorchain

import (
	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentAuctionBidHandler handles bidding on intent auctions
type IntentAuctionBidHandler struct {
	mgr Manager
}

// NewIntentAuctionBidHandler creates a new instance of IntentAuctionBidHandler
func NewIntentAuctionBidHandler(mgr Manager) IntentAuctionBidHandler {
	return IntentAuctionBidHandler{
		mgr: mgr,
	}
}

// Run executes the intent auction bid handler
func (h IntentAuctionBidHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentAuctionBid)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentAuctionBid failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentAuctionBid", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentAuctionBidHandler) validate(ctx cosmos.Context, msg MsgIntentAuctionBid) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if intents are enabled
	if h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentsEnabled) <= 0 {
		return se.ErrUnauthorized.Wrap("intents feature is disabled")
	}

	// Check if auction exists
	if !h.mgr.Keeper().IntentAuctionExists(ctx, msg.IntentTxID) {
		return se.ErrUnknownRequest.Wrapf("auction for intent %s does not exist", msg.IntentTxID)
	}

	// Get the auction
	auction, err := h.mgr.Keeper().GetIntentAuction(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get auction")
	}

	// Verify the auction is active
	if auction.Status != IntentAuctionStatus_AuctionActive {
		return se.ErrUnauthorized.Wrapf("auction is not active, status: %s", auction.Status.String())
	}

	// Check if auction has expired
	if ctx.BlockHeight() >= auction.ExpiryHeight {
		return se.ErrUnauthorized.Wrapf("auction has expired at height %d", auction.ExpiryHeight)
	}

	// Validate bid transaction has exactly one coin
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in bid transaction")
	}
	if len(msg.Tx.Coins) > 1 {
		return se.ErrInvalidRequest.Wrap("bid can only provide one coin type")
	}

	// Verify the coin is the expected bid asset (gas asset of source chain)
	if !msg.Tx.Coins[0].Asset.Equals(auction.BidAsset) {
		return se.ErrInvalidRequest.Wrapf("bid asset %s does not match expected asset %s", msg.Tx.Coins[0].Asset, auction.BidAsset)
	}

	// Calculate the effective bid amount (existing bid + new bid if top-up)
	bidCoin := msg.Tx.Coins[0]
	bidAmount := bidCoin.Amount
	existingBid, err := h.mgr.Keeper().GetBidderBidForAuction(ctx, msg.IntentTxID, msg.Tx.FromAddress)
	if err != nil {
		return ErrInternal(err, "fail to get existing bid")
	}
	if !existingBid.BidTxID.IsEmpty() {
		// Top-up: add new amount to existing bid
		bidAmount = existingBid.BidAmount.Amount.Add(bidCoin.Amount)
	}

	// Get the current highest bid
	currentBids, err := h.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get current bids")
	}

	// Use the IntentAuctionBids type to find highest bid
	bids := IntentAuctionBids(currentBids)
	highestBid := bids.HighestBid()

	// First bid must meet minimum bid requirement
	if highestBid.IsZero() {
		if bidAmount.LT(auction.MinBid) {
			return se.ErrInvalidRequest.Wrapf("bid amount %s is less than minimum bid %s", bidAmount, auction.MinBid)
		}
	} else {
		// Subsequent bids must beat the current highest bid
		if bidAmount.LTE(highestBid) {
			return se.ErrInvalidRequest.Wrapf("bid amount %s must be greater than current highest bid %s", bidAmount, highestBid)
		}

		// Check if we're in the overtime window
		overtimeBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionOvertimeBlocks)
		inOvertimeWindow := ctx.BlockHeight() >= auction.ExpiryHeight-overtimeBlocks

		// During overtime window, bid must be >= highestBid + 1x gas to be valid
		if inOvertimeWindow {
			gasFee := calculateGasFee(ctx, h.mgr, auction.BidAsset.GetChain())
			minOvertimeBid := highestBid.Add(gasFee)
			if bidAmount.LT(minOvertimeBid) {
				return se.ErrInvalidRequest.Wrapf(
					"in overtime window: bid amount %s must be at least %s (current highest %s + 1x gas %s)",
					bidAmount, minOvertimeBid, highestBid, gasFee)
			}
		}
	}

	return nil
}

func (h IntentAuctionBidHandler) handle(ctx cosmos.Context, msg MsgIntentAuctionBid) error {
	// Get the auction
	auction, err := h.mgr.Keeper().GetIntentAuction(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get auction")
	}

	// Get the bid coin
	bidCoin := msg.Tx.Coins[0]

	// Check if this is a top-up of an existing bid
	existingBid, err := h.mgr.Keeper().GetBidderBidForAuction(ctx, msg.IntentTxID, msg.Tx.FromAddress)
	if err != nil {
		return ErrInternal(err, "fail to get existing bid")
	}

	var bid IntentAuctionBid
	isTopUp := !existingBid.BidTxID.IsEmpty()
	if isTopUp {
		// Top-up: update existing bid
		bid = existingBid
		bid.BidAmount = common.NewCoin(auction.BidAsset, bid.BidAmount.Amount.Add(bidCoin.Amount))
		// Keep the original BidTxID but update submit height
		bid.SubmitHeight = ctx.BlockHeight()
	} else {
		// New bid
		bid = IntentAuctionBid{
			BidTxID:       msg.Tx.ID,
			IntentTxID:    msg.IntentTxID,
			BidderAddress: msg.Tx.FromAddress,
			BidAmount:     bidCoin,
			SubmitHeight:  ctx.BlockHeight(),
		}
	}

	// Get current bids to determine the previous highest bid (before this bid)
	currentBids, err := h.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, msg.IntentTxID)
	if err != nil {
		ctx.Logger().Error("fail to get current bids", "error", err)
	}
	previousHighest := IntentAuctionBids(currentBids).HighestBid()

	// Save the bid
	h.mgr.Keeper().SetIntentAuctionBid(ctx, bid)

	// Check if we should extend the auction due to overtime
	overtimeBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionOvertimeBlocks)
	inOvertimeWindow := ctx.BlockHeight() >= auction.ExpiryHeight-overtimeBlocks

	if inOvertimeWindow && overtimeBlocks > 0 {
		gasFee := calculateGasFee(ctx, h.mgr, auction.BidAsset.GetChain())
		minOvertimeBid := previousHighest.Add(gasFee)

		// If this bid qualifies for overtime extension (new highest >= previous + 1x gas)
		if bid.BidAmount.Amount.GTE(minOvertimeBid) {
			auction.ExpiryHeight = ctx.BlockHeight() + overtimeBlocks
			h.mgr.Keeper().SetIntentAuction(ctx, auction)
			ctx.Logger().Info("auction overtime extended",
				"intent_txid", msg.IntentTxID,
				"new_expiry", auction.ExpiryHeight,
			)
		}
	}

	ctx.Logger().Info("intent auction bid placed",
		"intent_txid", msg.IntentTxID,
		"bid_txid", msg.Tx.ID,
		"bidder", msg.Tx.FromAddress,
		"bid_amount", bid.BidAmount,
		"is_topup", isTopUp,
	)

	// Emit bid event
	evt := NewEventIntentAuctionBid(
		msg.Tx.ID,
		msg.IntentTxID,
		msg.Tx.FromAddress,
		bid.BidAmount.Amount,
		isTopUp,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent auction bid event", "error", err)
	}

	return nil
}

// calculateMinBid calculates the minimum bid for an auction
// It is 3x the network fee for the source chain
func calculateMinBid(ctx cosmos.Context, mgr Manager, chain common.Chain) cosmos.Uint {
	// Get the network fee for the chain
	networkFee, err := mgr.Keeper().GetNetworkFee(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get network fee", "chain", chain, "error", err)
		return cosmos.ZeroUint()
	}

	// Calculate network fee: TransactionSize * TransactionFeeRate
	fee := cosmos.NewUint(networkFee.TransactionSize * networkFee.TransactionFeeRate)

	// Multiply by the configured multiplier (default 3x)
	multiplier := mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionMinBidMultiplier)
	if multiplier <= 0 {
		multiplier = 3 // Default to 3x if not set
	}

	return fee.MulUint64(uint64(multiplier))
}

// calculateGasFee returns 1x network gas fee for the chain
func calculateGasFee(ctx cosmos.Context, mgr Manager, chain common.Chain) cosmos.Uint {
	networkFee, err := mgr.Keeper().GetNetworkFee(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get network fee", "chain", chain, "error", err)
		return cosmos.ZeroUint()
	}
	return cosmos.NewUint(networkFee.TransactionSize * networkFee.TransactionFeeRate)
}
