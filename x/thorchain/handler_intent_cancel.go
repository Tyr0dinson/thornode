package thorchain

import (
	"fmt"

	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentCancelHandler handles cancelling intents
type IntentCancelHandler struct {
	mgr Manager
}

type cancelFundingMode struct {
	allowedAsset       common.Asset
	requiresPrepaidGas bool
	gasAsset           common.Asset
}

// NewIntentCancelHandler creates a new instance of IntentCancelHandler
func NewIntentCancelHandler(mgr Manager) IntentCancelHandler {
	return IntentCancelHandler{
		mgr: mgr,
	}
}

// Run executes the intent cancel handler
func (h IntentCancelHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentCancel)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentCancel failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentCancel", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentCancelHandler) validate(ctx cosmos.Context, msg MsgIntentCancel) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Intentionally do not gate cancels on IntentsEnabled. Users should
	// be able to cancel and recover funds while the feature is disabled.
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}
	if intent.TxID.IsEmpty() {
		return se.ErrUnknownRequest.Wrapf("intent %s does not exist", msg.IntentTxID)
	}

	// Verify the intent is active
	if intent.Status != IntentActive {
		return se.ErrUnauthorized.Wrapf("intent is not active, status: %s", intent.Status.String())
	}

	// Only the source address can cancel the intent
	// Use the FromAddress from the observed tx (the actual user), not msg.Signer (the observer node)
	if !intent.SourceAddress.Equals(msg.Tx.FromAddress) {
		return se.ErrUnauthorized.Wrap("only the intent creator can cancel it")
	}

	mode, err := h.getCancelFundingMode(ctx, intent)
	if err != nil {
		return ErrInternal(err, "fail to determine cancel funding mode")
	}
	for _, coin := range msg.Tx.Coins {
		if !coin.Asset.Equals(mode.allowedAsset) {
			return se.ErrInvalidRequest.Wrapf(
				"cancel tx must only send %s for this intent, got %s",
				mode.allowedAsset, coin.Asset,
			)
		}
	}

	return nil
}

// getCancelFundingMode determines which asset a cancel tx may carry and whether
// the cancel must prepay chain gas for the outbound refund.
func (h IntentCancelHandler) getCancelFundingMode(ctx cosmos.Context, intent Intent) (cancelFundingMode, error) {
	// Native sources and assets with an available pool refund directly, so only
	// permit the source asset to avoid trapping unrelated coins in Asgard.
	if intent.Source.Asset.IsNative() || intent.Source.Asset.IsGasAsset() {
		return cancelFundingMode{allowedAsset: intent.Source.Asset}, nil
	}

	sourcePool, err := h.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	if err != nil {
		return cancelFundingMode{}, fmt.Errorf("fail to get pool for cancel funding mode: %w", err)
	}
	hasActivePool := !sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable
	if hasActivePool {
		return cancelFundingMode{allowedAsset: intent.Source.Asset}, nil
	}

	// Non-pool, non-native long-tail assets require prepaid chain gas to fund the
	// outbound refund, so only accept the chain gas asset when that gas pool is
	// available for booking.
	gasAsset := intent.Source.Asset.GetChain().GetGasAsset()
	gasPool, err := h.mgr.Keeper().GetPool(ctx, gasAsset)
	if err != nil {
		return cancelFundingMode{}, fmt.Errorf("fail to get gas pool for cancel funding mode: %w", err)
	}
	if gasPool.IsEmpty() || gasPool.Status != PoolAvailable {
		return cancelFundingMode{}, fmt.Errorf("gas pool %s is not available", gasAsset)
	}

	return cancelFundingMode{
		allowedAsset:       gasAsset,
		requiresPrepaidGas: true,
		gasAsset:           gasAsset,
	}, nil
}

func (h IntentCancelHandler) bookCancelGasIfNeeded(ctx cosmos.Context, msg MsgIntentCancel, intent Intent, mode cancelFundingMode) error {
	if !mode.requiresPrepaidGas {
		return nil
	}

	chain := intent.Source.Asset.GetChain()
	maxGasCoin, err := h.mgr.GasMgr().GetMaxGas(ctx, chain)
	if err != nil {
		return fmt.Errorf("fail to get max gas for %s: %w", chain, err)
	}

	gasFromCancel := cosmos.ZeroUint()
	for _, coin := range msg.Tx.Coins {
		if coin.Asset.Equals(mode.gasAsset) {
			gasFromCancel = gasFromCancel.Add(coin.Amount)
		}
	}

	minGasMultiplier := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentCancelMinGasMultiplier)
	if minGasMultiplier <= 0 {
		minGasMultiplier = 1
	}
	retainGasMultiplier := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentCancelRetainGasMultiplier)
	if retainGasMultiplier < 3 {
		retainGasMultiplier = 3
	}
	if retainGasMultiplier < minGasMultiplier {
		retainGasMultiplier = minGasMultiplier
	}

	minRequired := maxGasCoin.Amount.MulUint64(uint64(minGasMultiplier))
	if gasFromCancel.LT(minRequired) {
		return se.ErrInsufficientFunds.Wrapf(
			"cancel of non-pool asset requires gas: need %s %s, got %s",
			minRequired, mode.gasAsset, gasFromCancel,
		)
	}

	// Cap retained gas using the configured multiplier. If the user overpaid,
	// refund the excess immediately so they recover the obvious surplus.
	// Excess refund failure is fatal for the cancel so the whole operation
	// can roll back cleanly.
	retainCap := maxGasCoin.Amount.MulUint64(uint64(retainGasMultiplier))
	retained := gasFromCancel
	if gasFromCancel.GT(retainCap) {
		excess := common.SafeSub(gasFromCancel, retainCap)
		retained = retainCap

		excessToi := TxOutItem{
			Chain:     chain,
			ToAddress: intent.SourceAddress,
			Coin:      common.NewCoin(mode.gasAsset, excess),
			Memo:      fmt.Sprintf("REFUND:%s", intent.TxID),
			InHash:    intent.TxID,
		}
		excessOk, excessErr := h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, excessToi, cosmos.ZeroUint())
		if excessErr != nil {
			return fmt.Errorf("fail to schedule excess cancel gas refund: %w", excessErr)
		}
		if !excessOk {
			return fmt.Errorf("fail to schedule excess cancel gas refund")
		}
	}

	// Revalidate gas-pool availability at mutation time inside bookCancelGasToPool.
	// That keeps the decision logic centralized here while preserving the final
	// execution-time guard around pool mutation.
	if err := bookCancelGasToPool(ctx, h.mgr, intent.TxID, mode.gasAsset, retained); err != nil {
		return fmt.Errorf("fail to book cancel gas to pool: %w", err)
	}

	return nil
}

func (h IntentCancelHandler) handle(ctx cosmos.Context, msg MsgIntentCancel) error {
	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Re-validate intent status to guard against state changes between validate and handle
	if intent.Status != IntentActive {
		return fmt.Errorf("intent %s is no longer active, status: %s", msg.IntentTxID, intent.Status.String())
	}

	mode, err := h.getCancelFundingMode(ctx, intent)
	if err != nil {
		return fmt.Errorf("fail to determine cancel funding mode: %w", err)
	}

	// For non-native, non-pool, non-gas-asset sources, require gas tokens in the
	// cancel tx. The user-provided gas is booked into the chain gas pool
	// immediately (similar to processWinningBid), so the standard outbound gas
	// reimbursement path works unchanged.
	if err := h.bookCancelGasIfNeeded(ctx, msg, intent, mode); err != nil {
		return err
	}

	// Refund the source asset back to the source address.
	// Include any coins sent with the cancel tx (same asset) in the refund,
	// but only for non-secured/trade assets. The deposit handler skips escrowing
	// secured/trade coins for cancel messages, so including them would create
	// units from nothing.
	refundCoin := intent.Source
	if !intent.Source.Asset.IsSecuredAsset() && !intent.Source.Asset.IsTradeAsset() {
		for _, coin := range msg.Tx.Coins {
			if coin.Asset.Equals(intent.Source.Asset) && !coin.Amount.IsZero() {
				refundCoin.Amount = refundCoin.Amount.Add(coin.Amount)
			}
		}
	}

	// For RUNE, use Asgard module (where intent RUNE is escrowed)
	// For other assets, default to Asgard
	moduleName := ""
	if intent.Source.Asset.IsRune() {
		moduleName = AsgardName
	}

	// Schedule the txout for refund
	toi := TxOutItem{
		Chain:      intent.Source.Asset.GetChain(),
		ToAddress:  intent.SourceAddress,
		Coin:       refundCoin,
		Memo:       fmt.Sprintf("REFUND:%s", intent.TxID),
		InHash:     intent.TxID,
		ModuleName: moduleName,
	}

	ok, err := h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, toi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add outbound tx: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule refund txout")
	}

	ctx.Logger().Info("scheduled intent refund",
		"txid", intent.TxID,
		"asset", refundCoin.Asset,
		"amount", refundCoin.Amount,
		"to", intent.SourceAddress,
	)

	// Refund any pending solves for this intent.
	// Return the error to trigger CacheContext rollback and retry next block,
	// preventing solve records from being deleted without successful refunds.
	if err := h.mgr.IntentMgr().RefundIntentSolves(ctx, intent); err != nil {
		return fmt.Errorf("fail to refund intent solves on cancel: %w", err)
	}

	// Update intent status to cancelled
	intent.Status = IntentCancelled
	h.mgr.Keeper().SetIntent(ctx, intent)

	// Emit cancel event
	evt := NewEventIntentCancel(
		intent.TxID,
		refundCoin,
		intent.SourceAddress,
		false,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit intent cancel event: %w", err)
	}

	return nil
}

// bookCancelGasToPool adds the retained cancel gas into the chain's gas pool
// and converts its value to RUNE for the reserve, using a direct pool swap
// (same pattern as processWinningBid). After this, the pool has been credited
// so the normal outbound gas reimbursement path can charge it unchanged.
func bookCancelGasToPool(ctx cosmos.Context, mgr Manager, intentTxID common.TxID, gasAsset common.Asset, amount cosmos.Uint) error {
	gasPool, err := mgr.Keeper().GetPool(ctx, gasAsset)
	if err != nil {
		return fmt.Errorf("fail to get gas pool %s: %w", gasAsset, err)
	}
	if gasPool.IsEmpty() || gasPool.Status != PoolAvailable {
		return fmt.Errorf("gas pool %s is not available", gasAsset)
	}

	swapper, err := GetSwapper(mgr.Keeper().GetVersion())
	if err != nil {
		return fmt.Errorf("fail to get swapper: %w", err)
	}

	// Calculate swap: gas asset → RUNE
	X := gasPool.BalanceAsset
	x := amount
	Y := gasPool.BalanceRune

	slipBps := swapper.CalcSwapSlip(X, x)
	minSlipBps := cosmos.ZeroUint()
	emitRune, liquidityFee, _ := swapper.GetSwapCalc(X, x, Y, slipBps, minSlipBps)

	if emitRune.IsZero() {
		return fmt.Errorf("cancel gas booking emits zero RUNE")
	}
	if emitRune.GTE(Y) {
		return fmt.Errorf("not enough RUNE in gas pool for cancel gas booking")
	}

	// Update pool balances directly
	gasPool.BalanceAsset = gasPool.BalanceAsset.Add(x)
	gasPool.BalanceRune = common.SafeSub(gasPool.BalanceRune, emitRune)

	if err := mgr.Keeper().SetPool(ctx, gasPool); err != nil {
		return fmt.Errorf("fail to save gas pool after cancel gas booking: %w", err)
	}

	// Transfer emitted RUNE from Asgard to Reserve
	if err := mgr.Keeper().AddPoolFeeToReserve(ctx, emitRune); err != nil {
		return fmt.Errorf("fail to add cancel gas RUNE to reserve: %w", err)
	}

	// Emit swap event for the direct swap
	swapEvt := NewEventSwap(
		gasPool.Asset,
		cosmos.ZeroUint(),
		slipBps,
		liquidityFee,
		cosmos.ZeroUint(),
		common.Tx{
			ID:    intentTxID,
			Chain: common.THORChain,
			Coins: common.NewCoins(common.NewCoin(gasAsset, amount)),
		},
		common.NewCoin(common.RuneAsset(), emitRune),
		cosmos.ZeroUint(),
	)
	if err := mgr.EventMgr().EmitEvent(ctx, swapEvt); err != nil {
		ctx.Logger().Error("fail to emit cancel gas swap event", "error", err)
	}

	ctx.Logger().Info("booked cancel gas into pool for reserve",
		"intent_txid", intentTxID,
		"gas_asset", gasAsset,
		"amount", amount,
		"emit_rune", emitRune,
	)

	return nil
}
