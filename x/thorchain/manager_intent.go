package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
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

	// Get all active intent TxIDs from the active status index
	activeIntentTxIDs, err := im.mgr.Keeper().GetActiveIntents(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active intents: %w", err)
	}

	for _, intentTxID := range activeIntentTxIDs {
		// Get the intent
		intent, err := im.mgr.Keeper().GetIntent(ctx, intentTxID)
		if err != nil {
			ctx.Logger().Error("fail to get intent", "txid", intentTxID, "error", err)
			continue
		}

		// Double check it's active (should be, since it's in the active index)
		if intent.Status != IntentActive {
			continue
		}

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
		Chain:       intent.Source.Asset.GetChain(),
		ToAddress:   solve.SolverAddress,
		VaultPubKey: intent.VaultPubKey,
		Coin:        intent.Source,
		Memo:        fmt.Sprintf("OUT:%s", solve.SolveTxID),
		InHash:      solve.SolveTxID,
	}

	userToi := TxOutItem{
		Chain:       solve.TargetCoin.Asset.GetChain(),
		ToAddress:   intent.TargetAddress,
		VaultPubKey: solve.VaultPubKey,
		Coin:        solve.TargetCoin,
		Memo:        fmt.Sprintf("OUT:%s", intent.TxID),
		InHash:      intent.TxID,
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
			// TODO: Implement donate (if possible), or swap and donate for non-RUNE pool assets
			// For now, add to pool balance and liquidity fees
			feePool.BalanceAsset = feePool.BalanceAsset.Add(protocolFee.Amount)

			// Get the RUNE value of the fee for liquidity fees tracking
			runeValue := feePool.AssetValueInRune(protocolFee.Amount)
			feePool.LPUnits = feePool.LPUnits.Add(runeValue)

			if err := im.mgr.Keeper().SetPool(ctx, feePool); err != nil {
				ctx.Logger().Error("fail to update pool with fee", "error", err)
			} else {
				ctx.Logger().Info("added intent protocol fee to pool",
					"pool", feePool.Asset,
					"amount", protocolFee.Amount,
					"rune_value", runeValue,
				)
			}

			// Emit fee event
			feeEvent := NewEventFee(solve.SolveTxID, common.NewFee(common.NewCoins(protocolFee), cosmos.ZeroUint()), cosmos.ZeroUint())
			if err := im.mgr.EventMgr().EmitEvent(ctx, feeEvent); err != nil {
				ctx.Logger().Error("fail to emit fee event", "error", err)
			}
		}
	}

	// Schedule outbound for source asset to solver
	ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, solverToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add solver outbound tx: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule solver txout")
	}

	// Schedule outbound for target asset to user
	ok, err = im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, userToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add user outbound tx: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule user txout")
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

// expireIntent handles an expired intent by refunding or abandoning
func (im *IntentMgr) expireIntent(ctx cosmos.Context, intent Intent) error {
	// Determine if we should refund or abandon based on asset type
	sourcePool, _ := im.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	isPoolAsset := !sourcePool.IsEmpty()
	isNativeAsset := intent.Source.Asset.IsRune() || intent.Source.Asset.IsSecuredAsset()
	shouldRefund := isPoolAsset || isNativeAsset

	abandoned := false

	if shouldRefund {
		// Schedule the txout for refund
		toi := TxOutItem{
			Chain:       intent.Source.Asset.GetChain(),
			ToAddress:   intent.SourceAddress,
			VaultPubKey: intent.VaultPubKey,
			Coin:        intent.Source,
			Memo:        fmt.Sprintf("REFUND:%s", intent.TxID),
			InHash:      intent.TxID,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		if err != nil {
			return fmt.Errorf("fail to add outbound tx: %w", err)
		}
		if !ok {
			return fmt.Errorf("fail to schedule refund txout")
		}

		ctx.Logger().Info("scheduled expired intent refund",
			"txid", intent.TxID,
			"asset", intent.Source.Asset,
			"amount", intent.Source.Amount,
			"to", intent.SourceAddress,
		)
	} else {
		// Abandon (burn) the source asset by updating the vault
		abandoned = true

		// Get the vault and subtract the abandoned coins
		vault, err := im.mgr.Keeper().GetVault(ctx, intent.VaultPubKey)
		if err != nil {
			return fmt.Errorf("fail to get vault: %w", err)
		}

		// Subtract the abandoned amount from the vault
		vault.SubFunds(common.NewCoins(intent.Source))

		// Save the updated vault
		if err := im.mgr.Keeper().SetVault(ctx, vault); err != nil {
			return fmt.Errorf("fail to save vault: %w", err)
		}

		ctx.Logger().Info("abandoned expired intent source asset (non-pool, non-native)",
			"txid", intent.TxID,
			"asset", intent.Source.Asset,
			"amount", intent.Source.Amount,
			"vault", intent.VaultPubKey,
		)
	}

	refundCoin := intent.Source

	// Update intent status to expired
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit expire event
	evt := NewEventIntentExpire(
		intent.TxID,
		refundCoin,
		intent.SourceAddress,
		abandoned,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent expire event", "error", err)
	}

	return nil
}
