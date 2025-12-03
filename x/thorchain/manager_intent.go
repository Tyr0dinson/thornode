package thorchain

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// IntentMgr handles intent matching and expiration in EndBlock
type IntentMgr struct {
	mgr Manager
}

// NewIntentManager creates a new instance of IntentMgr
func NewIntentManager(mgr Manager) IntentManager {
	return &IntentMgr{
		mgr: mgr,
	}
}

// EndBlock processes intents at the end of each block
func (im *IntentMgr) EndBlock(ctx cosmos.Context) error {
	// Process solve bids that match at this height
	if err := im.processMatches(ctx); err != nil {
		ctx.Logger().Error("fail to process intent matches", "error", err)
		return err
	}

	// Process expired intents
	if err := im.processExpirations(ctx); err != nil {
		ctx.Logger().Error("fail to process intent expirations", "error", err)
		return err
	}

	// Process active auctions (check for completion)
	if err := im.processAuctions(ctx); err != nil {
		ctx.Logger().Error("fail to process intent auctions", "error", err)
		return err
	}

	return nil
}

// processMatches executes intents that have solve bids matching at the current height
func (im *IntentMgr) processMatches(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all solve bids that match at this height
	solves, err := im.mgr.Keeper().GetIntentSolvesByMatchHeight(ctx, currentHeight)
	if err != nil {
		return fmt.Errorf("fail to get intent solves by match height: %w", err)
	}

	for _, solve := range solves {
		// Get the intent
		intent, err := im.mgr.Keeper().GetIntent(ctx, solve.IntentTxID)
		if err != nil {
			ctx.Logger().Error("fail to get intent", "txid", solve.IntentTxID, "error", err)
			continue
		}

		// Skip if intent is not active
		if intent.Status != IntentActive {
			ctx.Logger().Info("intent is not active, skipping", "txid", solve.IntentTxID, "status", intent.Status.String())
			continue
		}

		// Execute the intent
		if err := im.executeIntent(ctx, intent, solve); err != nil {
			ctx.Logger().Error("fail to execute intent", "intent_txid", solve.IntentTxID, "solve_txid", solve.SolveTxID, "error", err)
			continue
		}
	}

	return nil
}

// processExpirations handles intents that have expired
func (im *IntentMgr) processExpirations(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all active intents from the active status index
	activeIntents, err := im.mgr.Keeper().GetActiveIntents(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active intents: %w", err)
	}

	for _, intent := range activeIntents {
		// Check if expired
		if currentHeight >= intent.ExpiryHeight {
			if err := im.expireIntent(ctx, intent); err != nil {
				ctx.Logger().Error("fail to expire intent", "txid", intent.TxID, "error", err)
				continue
			}
		}
	}

	return nil
}

// executeIntent performs the actual swap and completes the intent
func (im *IntentMgr) executeIntent(ctx cosmos.Context, intent Intent, solve IntentSolve) error {
	// Determine which asset to charge the fee on (must be pool or native asset)
	sourcePool, _ := im.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	targetPool, _ := im.mgr.Keeper().GetPool(ctx, solve.TargetCoin.Asset.GetLayer1Asset())

	isSourcePoolAsset := !sourcePool.IsEmpty()
	isSourceNative := intent.Source.Asset.IsRune() || intent.Source.Asset.IsSecuredAsset()
	isTargetPoolAsset := !targetPool.IsEmpty()
	isTargetNative := solve.TargetCoin.Asset.IsRune() || solve.TargetCoin.Asset.IsSecuredAsset()

	canChargeSource := isSourcePoolAsset || isSourceNative
	canChargeTarget := isTargetPoolAsset || isTargetNative

	// Calculate protocol fee
	minFeeBps := intent.MinFeeBasisPoints
	protocolFee := common.NoCoin
	var feePool Pool

	// Prepare outbound items
	solverToi := TxOutItem{
		Chain:     intent.Source.Asset.GetChain(),
		ToAddress: solve.SolverAddress,
		Coin:      intent.Source,
		Memo:      fmt.Sprintf("OUT:%s", solve.SolveTxID),
		InHash:    solve.SolveTxID,
	}

	userToi := TxOutItem{
		Chain:     solve.TargetCoin.Asset.GetChain(),
		ToAddress: intent.TargetAddress,
		Coin:      solve.TargetCoin,
		Memo:      fmt.Sprintf("OUT:%s", intent.TxID),
		InHash:    intent.TxID,
	}

	// Determine which asset to charge fee on (prefer source, then target)
	if canChargeSource {
		// Charge fee on source asset
		feeAmount := common.GetSafeShare(cosmos.NewUint(uint64(minFeeBps)), cosmos.NewUint(10000), intent.Source.Amount)
		protocolFee = common.NewCoin(intent.Source.Asset, feeAmount)
		feePool = sourcePool

		// Subtract fee from source amount going to solver
		if feeAmount.GT(cosmos.ZeroUint()) {
			solverToi.Coin.Amount = common.SafeSub(solverToi.Coin.Amount, feeAmount)
		}
	} else if canChargeTarget {
		// Charge fee on target asset
		feeAmount := common.GetSafeShare(cosmos.NewUint(uint64(minFeeBps)), cosmos.NewUint(10000), solve.TargetCoin.Amount)
		protocolFee = common.NewCoin(solve.TargetCoin.Asset, feeAmount)
		feePool = targetPool

		// Subtract fee from target amount going to user
		if feeAmount.GT(cosmos.ZeroUint()) {
			userToi.Coin.Amount = common.SafeSub(userToi.Coin.Amount, feeAmount)
		}
	}

	// Deposit fee into pool or reserve
	if !protocolFee.IsEmpty() && protocolFee.Amount.GT(cosmos.ZeroUint()) {
		if protocolFee.Asset.IsRune() {
			// Add to reserve
			if err := im.mgr.Keeper().AddPoolFeeToReserve(ctx, protocolFee.Amount); err != nil {
				ctx.Logger().Error("fail to add fee to reserve", "error", err)
			} else {
				ctx.Logger().Info("added intent protocol fee to reserve", "amount", protocolFee.Amount)
			}

			// Emit fee event
			feeEvent := NewEventFee(solve.SolveTxID, common.NewFee(common.NewCoins(protocolFee), cosmos.ZeroUint()), cosmos.ZeroUint())
			if err := im.mgr.EventMgr().EmitEvent(ctx, feeEvent); err != nil {
				ctx.Logger().Error("fail to emit fee event", "error", err)
			}
		} else if !feePool.IsEmpty() {
			// Swap non-RUNE fee to RUNE and add to reserve
			reserveAddr, err := im.mgr.Keeper().GetModuleAddress(ReserveName)
			if err != nil {
				ctx.Logger().Error("fail to get reserve address for fee swap", "error", err)
			} else {
				feeTx := common.Tx{
					ID:          solve.SolveTxID,
					Chain:       protocolFee.Asset.GetChain(),
					FromAddress: common.NoAddress,
					ToAddress:   reserveAddr,
					Coins:       common.NewCoins(protocolFee),
					Memo:        fmt.Sprintf("=:THOR.RUNE:%s", reserveAddr),
				}

				version := types.SwapVersion_v1
				if im.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
					version = types.SwapVersion_v2
				}

				asgardAddr := im.mgr.Keeper().GetModuleAccAddress(AsgardName)
				swapMsg := NewMsgSwap(feeTx, common.RuneAsset(), reserveAddr,
					cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
					"", "", nil, MarketSwap, 1, 0, version, asgardAddr)

				if err := im.mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, im.mgr, swapMsg); err != nil {
					ctx.Logger().Error("fail to queue intent fee swap to RUNE", "error", err)
				} else {
					ctx.Logger().Info("queued intent protocol fee swap to RUNE",
						"fee_asset", protocolFee.Asset,
						"fee_amount", protocolFee.Amount,
					)
				}
			}

			// Emit fee event
			feeEvent := NewEventFee(solve.SolveTxID, common.NewFee(common.NewCoins(protocolFee), cosmos.ZeroUint()), cosmos.ZeroUint())
			if err := im.mgr.EventMgr().EmitEvent(ctx, feeEvent); err != nil {
				ctx.Logger().Error("fail to emit fee event", "error", err)
			}
		}
	}

	// Schedule outbound for source asset to solver
	solverOk, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, solverToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add solver outbound tx: %w", err)
	}

	// Schedule outbound for target asset to user
	userOk, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, userToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add user outbound tx: %w", err)
	}

	// Only mark completed if both TxOuts succeeded
	if !solverOk || !userOk {
		return fmt.Errorf("fail to schedule txouts: solver=%v, user=%v", solverOk, userOk)
	}

	// Update intent status to completed
	intent.Status = IntentCompleted
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit complete event
	evt := NewEventIntentComplete(
		intent.TxID,
		solve.SolveTxID,
		solverToi.Coin,
		userToi.Coin,
		protocolFee,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent complete event", "error", err)
	}

	ctx.Logger().Info("intent executed",
		"intent_txid", intent.TxID,
		"solve_txid", solve.SolveTxID,
		"source_amount", intent.Source.Amount,
		"target_amount", solve.TargetCoin.Amount,
		"protocol_fee", protocolFee.Amount,
		"fee_asset", protocolFee.Asset,
	)

	return nil
}

// expireIntent handles an expired intent by starting an auction
func (im *IntentMgr) expireIntent(ctx cosmos.Context, intent Intent) error {
	// Get auction parameters
	auctionBps := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionBasisPoints)
	if auctionBps <= 0 {
		auctionBps = 1000 // Default 10%
	}

	auctionDuration := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionDurationBlocks)
	if auctionDuration <= 0 {
		auctionDuration = 14400 // Default ~1 day
	}

	// Calculate auction amount (1k bps = 10% of source)
	maxBps := cosmos.NewUint(constants.MaxBasisPts)
	auctionAmount := common.GetSafeShare(cosmos.NewUint(uint64(auctionBps)), maxBps, intent.Source.Amount)
	remainingAmount := common.SafeSub(intent.Source.Amount, auctionAmount)

	// Determine bid asset (gas asset of source chain)
	sourceChain := intent.Source.Asset.GetChain()
	bidAsset := sourceChain.GetGasAsset()

	// Calculate minimum bid (3x network fee)
	minBid := calculateMinBid(ctx, im.mgr, sourceChain)

	// Create the auction
	auction := IntentAuction{
		IntentTxID:      intent.TxID,
		AuctionAmount:   common.NewCoin(intent.Source.Asset, auctionAmount),
		RemainingAmount: common.NewCoin(intent.Source.Asset, remainingAmount),
		BidAsset:        bidAsset,
		MinBid:          minBid,
		StartHeight:     ctx.BlockHeight(),
		ExpiryHeight:    ctx.BlockHeight() + auctionDuration,
		Status:          IntentAuctionStatus_AuctionActive,
		OwnerAddress:    intent.SourceAddress,
	}

	// Save the auction
	im.mgr.Keeper().SetIntentAuction(ctx, auction)

	// Refund any existing solves for this intent
	if err := im.refundIntentSolves(ctx, intent); err != nil {
		ctx.Logger().Error("fail to refund intent solves", "txid", intent.TxID, "error", err)
	}

	// Update intent status to auctioning
	intent.Status = IntentAuctioning
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit auction start event
	evt := NewEventIntentAuctionStart(
		intent.TxID,
		auction.AuctionAmount,
		auction.RemainingAmount,
		bidAsset,
		minBid,
		auction.ExpiryHeight,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent auction start event", "error", err)
	}

	ctx.Logger().Info("started intent auction",
		"intent_txid", intent.TxID,
		"auction_amount", auctionAmount,
		"remaining_amount", remainingAmount,
		"bid_asset", bidAsset,
		"min_bid", minBid,
		"expiry_height", auction.ExpiryHeight,
	)

	return nil
}

// refundIntentSolves refunds all solve bids for an intent when auction starts
func (im *IntentMgr) refundIntentSolves(ctx cosmos.Context, intent Intent) error {
	solves, err := im.mgr.Keeper().GetIntentSolvesByIntentID(ctx, intent.TxID)
	if err != nil {
		return fmt.Errorf("fail to get intent solves: %w", err)
	}

	for _, solve := range solves {
		// Schedule refund for the solve
		toi := TxOutItem{
			Chain:     solve.TargetCoin.Asset.GetChain(),
			ToAddress: solve.SolverAddress,
			Coin:      solve.TargetCoin,
			Memo:      NewRefundMemo(solve.SolveTxID).String(),
			InHash:    solve.SolveTxID,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		if err != nil {
			ctx.Logger().Error("fail to refund solve", "solve_txid", solve.SolveTxID, "error", err)
			continue
		}
		if !ok {
			ctx.Logger().Error("fail to schedule solve refund", "solve_txid", solve.SolveTxID)
			continue
		}

		ctx.Logger().Info("refunded intent solve due to auction start",
			"intent_txid", intent.TxID,
			"solve_txid", solve.SolveTxID,
			"amount", solve.TargetCoin.Amount,
		)
	}

	return nil
}

// processAuctions processes active auctions at the end of each block
func (im *IntentMgr) processAuctions(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all active auctions
	activeAuctions, err := im.mgr.Keeper().GetActiveAuctions(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active auctions: %w", err)
	}

	for _, auction := range activeAuctions {
		// Check if auction has expired
		if currentHeight >= auction.ExpiryHeight {
			if err := im.completeAuction(ctx, auction); err != nil {
				ctx.Logger().Error("fail to complete auction", "intent_txid", auction.IntentTxID, "error", err)
				continue
			}
		}
	}

	return nil
}

// completeAuction finalizes an expired auction
func (im *IntentMgr) completeAuction(ctx cosmos.Context, auction IntentAuction) error {
	// Get bids for this auction
	bids, err := im.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, auction.IntentTxID)
	if err != nil {
		return fmt.Errorf("fail to get auction bids: %w", err)
	}

	// Get the intent
	intent, err := im.mgr.Keeper().GetIntent(ctx, auction.IntentTxID)
	if err != nil {
		return fmt.Errorf("fail to get intent: %w", err)
	}

	// If no bids, cancel the auction and abandon funds
	if len(bids) == 0 {
		return im.cancelAuction(ctx, auction, intent)
	}

	// Find the highest bid
	auctionBids := IntentAuctionBids(bids)
	highestBidAmount := auctionBids.HighestBid()

	// Collect all bids with the highest amount (for deterministic tiebreaker)
	var candidates []IntentAuctionBid
	for i := range bids {
		if bids[i].BidAmount.Amount.Equal(highestBidAmount) {
			candidates = append(candidates, bids[i])
		}
	}

	if len(candidates) == 0 {
		return fmt.Errorf("no winning bid found")
	}

	// Deterministic tiebreaker: earliest SubmitHeight wins, then lowest BidTxID
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SubmitHeight != candidates[j].SubmitHeight {
			return candidates[i].SubmitHeight < candidates[j].SubmitHeight
		}
		return candidates[i].BidTxID.String() < candidates[j].BidTxID.String()
	})
	winningBid := &candidates[0]

	// Process losing bids - refund them
	for i := range bids {
		if bids[i].BidTxID.Equals(winningBid.BidTxID) {
			continue // Skip winner
		}

		// Determine module name for RUNE bids (bid RUNE is escrowed in Reserve)
		bidModuleName := ""
		if bids[i].BidAmount.Asset.IsRune() {
			bidModuleName = ReserveName
		}

		// Refund the losing bid
		toi := TxOutItem{
			Chain:      bids[i].BidAmount.Asset.GetChain(),
			ToAddress:  bids[i].BidderAddress,
			Coin:       bids[i].BidAmount,
			Memo:       NewRefundMemo(bids[i].BidTxID).String(),
			InHash:     bids[i].BidTxID,
			ModuleName: bidModuleName,
		}

		ok, refundErr := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		if refundErr != nil {
			ctx.Logger().Error("fail to refund losing bid", "bid_txid", bids[i].BidTxID, "error", refundErr)
			continue
		}
		if !ok {
			ctx.Logger().Error("fail to schedule losing bid refund", "bid_txid", bids[i].BidTxID)
			continue
		}

		ctx.Logger().Info("refunded losing auction bid",
			"intent_txid", auction.IntentTxID,
			"bid_txid", bids[i].BidTxID,
			"amount", bids[i].BidAmount.Amount,
		)
	}

	// Determine module name for RUNE outbounds (RUNE is escrowed in Reserve)
	moduleName := ""
	if auction.AuctionAmount.Asset.IsRune() {
		moduleName = ReserveName
	}

	// Send auction amount to winner
	winnerToi := TxOutItem{
		Chain:      auction.AuctionAmount.Asset.GetChain(),
		ToAddress:  winningBid.BidderAddress,
		Coin:       auction.AuctionAmount,
		Memo:       NewOutboundMemo(winningBid.BidTxID).String(),
		InHash:     winningBid.BidTxID,
		ModuleName: moduleName,
	}

	ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, winnerToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to send auction amount to winner: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule winner txout")
	}

	// Send remaining amount to owner
	ownerToi := TxOutItem{
		Chain:      auction.RemainingAmount.Asset.GetChain(),
		ToAddress:  auction.OwnerAddress,
		Coin:       auction.RemainingAmount,
		Memo:       NewRefundMemo(auction.IntentTxID).String(),
		InHash:     auction.IntentTxID,
		ModuleName: moduleName,
	}

	ok, err = im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, ownerToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to send remaining amount to owner: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule owner refund txout")
	}

	// Process winning bid: swap to RUNE and add to reserve
	if err := im.processWinningBid(ctx, *winningBid); err != nil {
		ctx.Logger().Error("fail to process winning bid", "error", err)
	}

	// Update auction status
	auction.Status = IntentAuctionStatus_AuctionCompleted
	im.mgr.Keeper().SetIntentAuction(ctx, auction)

	// Update intent status to expired (auction completed)
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit auction complete event
	evt := NewEventIntentAuctionComplete(
		auction.IntentTxID,
		winningBid.BidTxID,
		winningBid.BidderAddress,
		winningBid.BidAmount,
		auction.AuctionAmount,
		auction.RemainingAmount,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit auction complete event", "error", err)
	}

	ctx.Logger().Info("auction completed",
		"intent_txid", auction.IntentTxID,
		"winner", winningBid.BidderAddress,
		"winning_bid", winningBid.BidAmount.Amount,
		"auction_amount", auction.AuctionAmount.Amount,
		"owner_refund", auction.RemainingAmount.Amount,
	)

	return nil
}

// processWinningBid handles the winning bid - swap to RUNE and add to reserve
func (im *IntentMgr) processWinningBid(ctx cosmos.Context, bid IntentAuctionBid) error {
	// If the bid is already in RUNE, it's already in Reserve (sent there when bid was placed)
	// No additional action needed - just log it
	if bid.BidAmount.Asset.IsRune() {
		ctx.Logger().Info("winning bid RUNE already in reserve", "amount", bid.BidAmount.Amount)
		return nil
	}

	// For non-RUNE assets, create a swap to RUNE destined for the reserve
	reserveAddr, err := im.mgr.Keeper().GetModuleAddress(ReserveName)
	if err != nil {
		return fmt.Errorf("fail to get reserve module address: %w", err)
	}

	// Create a synthetic transaction for the swap
	swapTx := common.Tx{
		ID:          bid.BidTxID,
		Chain:       bid.BidAmount.Asset.GetChain(),
		FromAddress: bid.BidderAddress,
		ToAddress:   reserveAddr,
		Coins:       common.NewCoins(bid.BidAmount),
		Gas:         common.Gas{common.NewCoin(bid.BidAmount.Asset, cosmos.OneUint())},
		Memo:        fmt.Sprintf("=:THOR.RUNE:%s", reserveAddr),
	}

	// Determine swap version based on configuration
	version := types.SwapVersion_v1
	if im.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		version = types.SwapVersion_v2
	}

	// Get Asgard module address for signer
	asgardAddr := im.mgr.Keeper().GetModuleAccAddress(AsgardName)

	// Create the swap message
	swapMsg := NewMsgSwap(
		swapTx,
		common.RuneAsset(),
		reserveAddr,
		cosmos.ZeroUint(), // No slip limit
		common.NoAddress,  // No affiliate
		cosmos.ZeroUint(), // No affiliate fee
		"",                // No aggregator
		"", nil,           // No dex target
		MarketSwap,
		1, 0, // No streaming
		version,
		asgardAddr,
	)

	// Add swap to the advanced swap queue - this ensures proper outbound handling
	if err := im.mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, im.mgr, swapMsg); err != nil {
		return fmt.Errorf("fail to add winning bid swap to queue: %w", err)
	}

	ctx.Logger().Info("queued winning bid swap to RUNE for reserve",
		"bid_asset", bid.BidAmount.Asset,
		"bid_amount", bid.BidAmount.Amount,
	)

	return nil
}

// cancelAuction cancels an auction with no bids
// For native assets (secured, trade, RUNE), refund to owner
// For external chain assets, abandon funds from vault
func (im *IntentMgr) cancelAuction(ctx cosmos.Context, auction IntentAuction, intent Intent) error {
	sourceAsset := auction.AuctionAmount.Asset
	totalAmount := auction.AuctionAmount.Amount.Add(auction.RemainingAmount.Amount)
	totalCoin := common.NewCoin(sourceAsset, totalAmount)

	// Check if this is a native asset (RUNE, secured, or trade asset)
	// These don't live in vaults - they're in user bank accounts as cosmos coins
	isNativeAsset := sourceAsset.IsRune() || sourceAsset.IsSecuredAsset() || sourceAsset.IsTradeAsset()

	abandoned := false

	if isNativeAsset {
		// Refund native assets to the owner via txout
		// For RUNE, use Reserve module (where intent RUNE is escrowed)
		// For secured/trade assets, default to Asgard (they don't actually use module)
		moduleName := ""
		if sourceAsset.IsRune() {
			moduleName = ReserveName
		}

		toi := TxOutItem{
			Chain:      sourceAsset.GetChain(),
			ToAddress:  auction.OwnerAddress,
			Coin:       totalCoin,
			Memo:       fmt.Sprintf("REFUND:%s", auction.IntentTxID),
			InHash:     auction.IntentTxID,
			ModuleName: moduleName,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		if err != nil {
			return fmt.Errorf("fail to schedule refund for cancelled auction: %w", err)
		}
		if !ok {
			return fmt.Errorf("fail to add refund txout for cancelled auction")
		}

		ctx.Logger().Info("scheduled refund for cancelled auction",
			"intent_txid", auction.IntentTxID,
			"amount", totalAmount,
			"to", auction.OwnerAddress,
		)
	} else {
		// Abandon external chain assets from vault
		vaults, err := im.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return fmt.Errorf("fail to get active vaults: %w", err)
		}
		if len(vaults) == 0 {
			return fmt.Errorf("no active vaults available")
		}

		// Find vault with sufficient balance
		var selectedVault *Vault
		var closestDiff cosmos.Uint
		for i := range vaults {
			vaultBalance := vaults[i].GetCoin(sourceAsset).Amount

			if vaultBalance.GTE(totalAmount) {
				diff := common.SafeSub(vaultBalance, totalAmount)

				// Select this vault if it's the first valid one, has a closer balance,
				// or has the same balance but lower PubKey (deterministic tiebreaker)
				if selectedVault == nil || diff.LT(closestDiff) ||
					(diff.Equal(closestDiff) && vaults[i].PubKey.String() < selectedVault.PubKey.String()) {
					selectedVault = &vaults[i]
					closestDiff = diff
				}
			}
		}

		if selectedVault == nil {
			return fmt.Errorf("no vault found with sufficient balance for asset %s (need %s)", sourceAsset, totalAmount)
		}

		// Subtract the abandoned amount from the vault
		selectedVault.SubFunds(common.NewCoins(totalCoin))

		// Save the updated vault
		if err := im.mgr.Keeper().SetVault(ctx, *selectedVault); err != nil {
			return fmt.Errorf("fail to save vault: %w", err)
		}

		abandoned = true

		ctx.Logger().Info("cancelled auction with no bids, funds abandoned",
			"intent_txid", auction.IntentTxID,
			"amount", totalAmount,
			"vault", selectedVault.PubKey,
		)
	}

	// Update auction status
	auction.Status = IntentAuctionStatus_AuctionCancelled
	im.mgr.Keeper().SetIntentAuction(ctx, auction)

	// Update intent status
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit cancel event
	evt := NewEventIntentAuctionCancel(auction.IntentTxID, abandoned)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit auction cancel event", "error", err)
	}

	return nil
}
