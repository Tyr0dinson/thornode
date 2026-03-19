package thorchain

import (
	"fmt"
	"math"

	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentSolveHandler handles solving intents
type IntentSolveHandler struct {
	mgr Manager
}

// NewIntentSolveHandler creates a new instance of IntentSolveHandler
func NewIntentSolveHandler(mgr Manager) IntentSolveHandler {
	return IntentSolveHandler{
		mgr: mgr,
	}
}

// Run executes the intent solve handler
func (h IntentSolveHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentSolve)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentSolve failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentSolve", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentSolveHandler) validate(ctx cosmos.Context, msg MsgIntentSolve) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if intents are enabled
	if h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentsEnabled) <= 0 {
		return se.ErrUnauthorized.Wrap("intents feature is disabled")
	}

	// Check if intent exists
	if !h.mgr.Keeper().IntentExists(ctx, msg.IntentTxID) {
		return se.ErrUnknownRequest.Wrapf("intent %s does not exist", msg.IntentTxID)
	}

	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Verify the intent is active (rejects auctioning, completed, or expired intents)
	if intent.Status != IntentActive {
		return se.ErrUnauthorized.Wrapf("intent is not active, status: %s", intent.Status.String())
	}

	// Reject intents that reference unsupported THOR-native asset classes before
	// solve validation proceeds, so solvers do not interact with an intent that
	// can never be escrowed/executed correctly.
	if isUnsupportedIntentNativeAsset(intent.Source.Asset) {
		return se.ErrInvalidRequest.Wrap(unsupportedIntentNativeAssetError(intent.Source.Asset))
	}
	if isUnsupportedIntentNativeAsset(intent.TargetAsset) {
		return se.ErrInvalidRequest.Wrap(unsupportedIntentNativeAssetError(intent.TargetAsset))
	}

	// Check if intent has expired
	if ctx.BlockHeight() >= intent.ExpiryHeight {
		return se.ErrUnauthorized.Wrapf("intent has expired at height %d", intent.ExpiryHeight)
	}

	// Reject duplicate solve IDs to prevent overwriting existing solve records
	if h.mgr.Keeper().IntentSolveExists(ctx, msg.Tx.ID) {
		return se.ErrInvalidRequest.Wrapf("solve with txid %s already exists", msg.Tx.ID)
	}

	// Validate solve transaction has exactly one coin (the target asset)
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in solve transaction")
	}
	if len(msg.Tx.Coins) > 1 {
		return se.ErrInvalidRequest.Wrap("solve can only provide one coin type")
	}

	// Verify the coin is the target asset
	if !msg.Tx.Coins[0].Asset.Equals(intent.TargetAsset) {
		return se.ErrInvalidRequest.Wrapf("solve asset %s does not match intent target asset %s", msg.Tx.Coins[0].Asset, intent.TargetAsset)
	}

	// Prevent self-solving: the intent creator cannot solve their own intent
	if intent.SourceAddress.Equals(msg.Tx.FromAddress) {
		return se.ErrUnauthorized.Wrap("intent creator cannot solve their own intent")
	}

	// Enforce maximum number of solves per intent to prevent DoS
	maxSolvesPerIntent := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentMaxSolvesPerIntent)
	if maxSolvesPerIntent > 0 {
		solves, err := h.mgr.Keeper().GetIntentSolvesByIntentID(ctx, msg.IntentTxID)
		if err != nil {
			return ErrInternal(err, "fail to get existing solves")
		}
		if int64(len(solves)) >= maxSolvesPerIntent {
			return se.ErrInvalidRequest.Wrapf("maximum number of solves (%d) reached for this intent", maxSolvesPerIntent)
		}
	}

	// Verify the offered amount meets minimum (at least low target)
	offeredAmount := msg.Tx.Coins[0].Amount
	if offeredAmount.LT(intent.LowTarget) {
		return se.ErrInvalidRequest.Wrapf("offered amount %s is less than low target %s", offeredAmount, intent.LowTarget)
	}

	// Determine the destination address for the solver
	// For native assets (RUNE, secured, trade), use THORChain
	// For L1 assets, use the source asset's chain
	sourceChain := intent.Source.Asset.GetChain()
	if intent.Source.Asset.IsSecuredAsset() || intent.Source.Asset.IsTradeAsset() {
		sourceChain = common.THORChain
	}

	// Validate destination address if provided
	if !msg.DestinationAddress.IsEmpty() {
		if !msg.DestinationAddress.IsChain(sourceChain) {
			return se.ErrInvalidRequest.Wrapf("destination address %s is not valid for chain %s", msg.DestinationAddress, sourceChain)
		}
	} else {
		// Destination address is required for cross-chain solves
		solverChain := msg.Tx.FromAddress.GetChain()
		if !solverChain.Equals(sourceChain) {
			return se.ErrInvalidRequest.Wrapf("destination address is required when solving cross-chain (solver chain %s != source chain %s)", solverChain, sourceChain)
		}
	}

	return nil
}

func (h IntentSolveHandler) handle(ctx cosmos.Context, msg MsgIntentSolve) error {
	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Re-validate intent status to guard against state changes between validate and handle
	if intent.Status != IntentActive {
		return fmt.Errorf("intent %s is no longer active, status: %s", msg.IntentTxID, intent.Status.String())
	}

	// Get the solver's target coin
	targetCoin := msg.Tx.Coins[0]

	// Escrow assets by withdrawing them from the solver
	// This burns the coins/reduces units and decreases pool depth
	// When the intent executes, the txout system will call Deposit() to send to user
	// When refunding, the txout system will call Deposit() to restore to solver
	switch {
	case targetCoin.Asset.IsSecuredAsset():
		solverAddr, err := msg.Tx.FromAddress.AccAddress()
		if err != nil {
			return fmt.Errorf("fail to get solver acc address: %w", err)
		}
		_, err = h.mgr.SecuredAssetManager().Withdraw(ctx, targetCoin.Asset, targetCoin.Amount, solverAddr, common.NoAddress, msg.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to escrow secured asset for intent solve: %w", err)
		}
		ctx.Logger().Info("escrowed secured asset for intent solve", "asset", targetCoin.Asset, "amount", targetCoin.Amount)
	case targetCoin.Asset.IsTradeAsset():
		solverAddr, err := msg.Tx.FromAddress.AccAddress()
		if err != nil {
			return fmt.Errorf("fail to get solver acc address: %w", err)
		}
		_, err = h.mgr.TradeAccountManager().Withdrawal(ctx, targetCoin.Asset, targetCoin.Amount, solverAddr, common.NoAddress, msg.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to escrow trade asset for intent solve: %w", err)
		}
		ctx.Logger().Info("escrowed trade asset for intent solve", "asset", targetCoin.Asset, "amount", targetCoin.Amount)
	case targetCoin.Asset.IsRune():
		// Transfer RUNE to Asgard module for escrow
		solverAddr, err := msg.Tx.FromAddress.AccAddress()
		if err != nil {
			return fmt.Errorf("fail to get solver acc address: %w", err)
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), targetCoin.Amount))
		if err := h.mgr.Keeper().SendFromAccountToModule(ctx, solverAddr, AsgardName, coins); err != nil {
			return fmt.Errorf("fail to escrow RUNE for intent solve: %w", err)
		}
		ctx.Logger().Info("escrowed RUNE for intent solve", "amount", targetCoin.Amount)
	default:
		// L1 assets observed on-chain via bifrost don't require explicit escrow.
		// However, if a native non-RUNE asset arrives via MsgDeposit,
		// the deposit handler skips SendFromAccountToModule for TxIntentSolve, so the
		// coins would not be escrowed. Reject this case to prevent a silent no-escrow path.
		if msg.Tx.Chain.Equals(common.THORChain) {
			return fmt.Errorf("unsupported native asset for intent solve escrow: %s", targetCoin.Asset)
		}
	}

	// Get the offered amount
	offeredAmount := targetCoin.Amount

	// Calculate match height using linear interpolation
	// match_height = current_height + ((high - offered) * slide_blocks / (high - low))
	currentHeight := ctx.BlockHeight()
	slideBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentPriceSlideBlocks)
	if slideBlocks <= 0 {
		slideBlocks = 600 // Default to ~1 hour if misconfigured
	}

	var matchHeight int64
	switch {
	case offeredAmount.GTE(intent.HighTarget):
		// Offer meets or exceeds high target - execute immediately
		matchHeight = currentHeight
	case intent.HighTarget.Equal(intent.LowTarget):
		// High and low are equal - any valid offer executes immediately
		matchHeight = currentHeight
	default:
		// Calculate interpolated match height
		// Using: match_height = current_height + ((high - offered) * slide_blocks / (high - low))
		highMinusOffered := common.SafeSub(intent.HighTarget, offeredAmount)
		highMinusLow := common.SafeSub(intent.HighTarget, intent.LowTarget)

		// Guard against division by zero (SafeSub could produce zero in edge cases)
		if highMinusLow.IsZero() {
			matchHeight = currentHeight
		} else {
			// Calculate the delay: (high - offered) * slide_blocks / (high - low)
			delay := highMinusOffered.Mul(cosmos.NewUint(uint64(slideBlocks)))
			delay = delay.Quo(highMinusLow)

			// Guard against int64 overflow when converting delay.
			// Both checks are needed: the first rejects delays that don't fit in int64,
			// the second prevents currentHeight + delay from wrapping negative.
			remainingBlocks := intent.ExpiryHeight - currentHeight
			if delay.GT(cosmos.NewUint(uint64(math.MaxInt64))) || (remainingBlocks >= 0 && delay.GT(cosmos.NewUint(uint64(remainingBlocks)))) {
				matchHeight = intent.ExpiryHeight
			} else {
				matchHeight = currentHeight + int64(delay.Uint64())
			}

			// Cap match height at expiry height
			if matchHeight > intent.ExpiryHeight {
				matchHeight = intent.ExpiryHeight
			}
		}
	}

	// Determine destination address - use provided address or fall back to solver's from address
	destAddr := msg.DestinationAddress
	if destAddr.IsEmpty() {
		destAddr = msg.Tx.FromAddress
	}

	// Save the solve bid for matching in EndBlock
	solve := IntentSolve{
		SolveTxID:          msg.Tx.ID,
		IntentTxID:         msg.IntentTxID,
		SolverAddress:      msg.Tx.FromAddress,
		TargetCoin:         targetCoin,
		SubmitHeight:       ctx.BlockHeight(),
		MatchHeight:        matchHeight,
		DestinationAddress: destAddr,
	}
	h.mgr.Keeper().SetIntentSolve(ctx, solve)

	ctx.Logger().Info("intent solve bid scheduled",
		"intent_txid", msg.IntentTxID,
		"solve_txid", msg.Tx.ID,
		"match_height", matchHeight,
		"offered_amount", offeredAmount,
	)

	// Emit solve event
	evt := NewEventIntentSolve(
		msg.Tx.ID,
		msg.IntentTxID,
		msg.Tx.FromAddress,
		targetCoin.Amount,
		matchHeight,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit intent solve event: %w", err)
	}

	return nil
}
