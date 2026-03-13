package thorchain

import (
	"fmt"

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

	// Validate coins array before accessing
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in bid transaction")
	}

	// Reject zero-amount bids
	if msg.Tx.Coins[0].Amount.IsZero() {
		return se.ErrInvalidRequest.Wrap("bid amount cannot be zero")
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

		// Check if we're in the overtime window (guard against underflow when overtimeBlocks > ExpiryHeight)
		overtimeBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionOvertimeBlocks)
		inOvertimeWindow := overtimeBlocks > 0 && auction.ExpiryHeight >= overtimeBlocks && ctx.BlockHeight() >= auction.ExpiryHeight-overtimeBlocks

		// During overtime window, bid must be >= highestBid + 1x gas to be valid
		if inOvertimeWindow {
			gasFee, err := calculateGasFee(ctx, h.mgr, auction.BidAsset.GetChain())
			if err != nil {
				return ErrInternal(err, "fail to calculate gas fee for overtime validation")
			}
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

	// Re-validate auction state to guard against state changes between validate and handle
	if auction.Status != IntentAuctionStatus_AuctionActive {
		return se.ErrUnauthorized.Wrapf("auction is no longer active, status: %s", auction.Status.String())
	}
	if ctx.BlockHeight() >= auction.ExpiryHeight {
		return se.ErrUnauthorized.Wrapf("auction has expired at height %d", auction.ExpiryHeight)
	}

	// Validate coins array before accessing
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in bid transaction")
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
		// Top-up: update existing bid amount but preserve original SubmitHeight
		// to maintain tiebreaker fairness (earlier bidders keep priority).
		// Note: The top-up transaction's TxID (msg.Tx.ID) is recorded in the emitted
		// event for auditability. The bid record retains the original BidTxID since
		// refunds use the bidder's address (not InHash-based routing).
		bid = existingBid
		bid.BidAmount = common.NewCoin(auction.BidAsset, bid.BidAmount.Amount.Add(bidCoin.Amount))
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

	// Get current bids to determine the previous highest bid (before this bid).
	// Exclude the current bidder's own bid so that top-ups by the highest bidder
	// don't trivially trigger overtime extension.
	currentBids, err := h.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get current bids for overtime calculation")
	}
	previousHighest := cosmos.ZeroUint()
	for _, b := range currentBids {
		if b.BidderAddress.Equals(msg.Tx.FromAddress) {
			continue // Skip the current bidder's own previous bid
		}
		if b.BidAmount.Amount.GT(previousHighest) {
			previousHighest = b.BidAmount.Amount
		}
	}

	// Save the bid
	h.mgr.Keeper().SetIntentAuctionBid(ctx, bid)

	// Check if we should extend the auction due to overtime (guard against underflow)
	overtimeBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionOvertimeBlocks)
	inOvertimeWindow := overtimeBlocks > 0 && auction.ExpiryHeight >= overtimeBlocks && ctx.BlockHeight() >= auction.ExpiryHeight-overtimeBlocks

	if inOvertimeWindow {
		gasFee, err := calculateGasFee(ctx, h.mgr, auction.BidAsset.GetChain())
		if err != nil {
			return ErrInternal(err, "fail to calculate gas fee for overtime extension")
		}
		minOvertimeBid := previousHighest.Add(gasFee)

		// If this bid qualifies for overtime extension (new highest >= previous + 1x gas)
		if bid.BidAmount.Amount.GTE(minOvertimeBid) {
			newExpiry := ctx.BlockHeight() + overtimeBlocks

			// Enforce hard cap on total overtime to prevent indefinite stalling
			maxOvertimeBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionMaxOvertimeBlocks)
			if maxOvertimeBlocks > 0 {
				// Get the original auction duration to calculate max allowed expiry
				auctionDuration := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionDurationBlocks)
				maxExpiry := auction.StartHeight + auctionDuration + maxOvertimeBlocks
				if newExpiry > maxExpiry {
					newExpiry = maxExpiry
				}
			}

			if newExpiry > auction.ExpiryHeight {
				auction.ExpiryHeight = newExpiry
				h.mgr.Keeper().SetIntentAuction(ctx, auction)
				ctx.Logger().Info("auction overtime extended",
					"intent_txid", msg.IntentTxID,
					"new_expiry", auction.ExpiryHeight,
				)
			}
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
		return fmt.Errorf("fail to emit intent auction bid event: %w", err)
	}

	return nil
}

// calculateMinBid calculates the minimum bid for an auction
// It is 3x the network fee for the source chain
func calculateMinBid(ctx cosmos.Context, mgr Manager, chain common.Chain) (cosmos.Uint, error) {
	// Get the network fee for the chain
	networkFee, err := mgr.Keeper().GetNetworkFee(ctx, chain)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	// Calculate network fee: TransactionSize * TransactionFeeRate
	fee := cosmos.NewUint(networkFee.TransactionSize).MulUint64(networkFee.TransactionFeeRate)

	// Multiply by the configured multiplier (default 3x)
	multiplier := mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionMinBidMultiplier)
	if multiplier <= 0 {
		multiplier = 3 // Default to 3x if not set
	}

	return fee.MulUint64(uint64(multiplier)), nil
}

// calculateGasFee returns 1x network gas fee for the chain.
// Returns a minimum floor of 1 if the calculated fee is zero, to prevent
// the overtime anti-sniping threshold from becoming ineffective.
func calculateGasFee(ctx cosmos.Context, mgr Manager, chain common.Chain) (cosmos.Uint, error) {
	networkFee, err := mgr.Keeper().GetNetworkFee(ctx, chain)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	fee := cosmos.NewUint(networkFee.TransactionSize).MulUint64(networkFee.TransactionFeeRate)
	if fee.IsZero() {
		fee = cosmos.OneUint()
	}
	return fee, nil
}

// calculateSolveEqualityTolerance returns the amount difference still treated as
// equal when comparing solves for the same intent. The tolerance must be in the
// target asset's own denomination, so for non-gas targets we convert the chain
// gas fee through RUNE when both pools are available. If pricing is unavailable,
// we fall back to exact-amount comparison.
func calculateSolveEqualityTolerance(ctx cosmos.Context, mgr Manager, targetAsset common.Asset) (cosmos.Uint, error) {
	increment, _, err := calculateSolveBeatIncrement(ctx, mgr, targetAsset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	// When we cannot price the gas fee into the target asset, treat only exact
	// amounts as equal rather than mixing denominations.
	if increment.Equal(cosmos.OneUint()) && !targetAsset.Equals(targetAsset.GetChain().GetGasAsset()) {
		return cosmos.ZeroUint(), nil
	}
	return increment, nil
}

// calculateSolveBeatIncrement returns the minimum amount a new solve should add
// on top of the current best quote, expressed in the target asset. When the
// target is not the chain gas asset, the gas fee is converted gas asset -> RUNE
// -> target asset using spot pool pricing if both pools are available.
func calculateSolveBeatIncrement(ctx cosmos.Context, mgr Manager, targetAsset common.Asset) (cosmos.Uint, common.Asset, error) {
	gasAsset := targetAsset.GetChain().GetGasAsset()
	gasFee, err := calculateGasFee(ctx, mgr, targetAsset.GetChain())
	if err != nil {
		return cosmos.ZeroUint(), common.EmptyAsset, err
	}
	if targetAsset.Equals(gasAsset) {
		return gasFee, targetAsset, nil
	}

	// Price the chain gas fee into the target asset using the existing spot pools:
	// gas asset -> RUNE -> target asset.
	gasPool, err := mgr.Keeper().GetPool(ctx, gasAsset.GetLayer1Asset())
	if err != nil {
		return cosmos.ZeroUint(), common.EmptyAsset, err
	}
	targetPool, err := mgr.Keeper().GetPool(ctx, targetAsset.GetLayer1Asset())
	if err != nil {
		return cosmos.ZeroUint(), common.EmptyAsset, err
	}
	if gasPool.IsEmpty() || gasPool.Status != PoolAvailable || targetPool.IsEmpty() || targetPool.Status != PoolAvailable {
		// Without both pools we cannot compare like-for-like units, so fall back to
		// the smallest possible step in the target asset.
		return cosmos.OneUint(), targetAsset, nil
	}

	runeValue := gasPool.AssetValueInRune(gasFee)
	if runeValue.IsZero() {
		return cosmos.OneUint(), targetAsset, nil
	}

	targetValue := targetPool.RuneValueInAsset(runeValue)
	if targetValue.IsZero() {
		// Keep the increment strictly positive so quote output and "beat current best"
		// checks never claim that a zero-difference bid is sufficient.
		targetValue = cosmos.OneUint()
	}
	return targetValue, targetAsset, nil
}
