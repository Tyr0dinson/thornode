package observer

import (
	"fmt"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemolessHandler handles memoless transaction processing in Bifrost
type MemolessHandler struct {
    bridge thorclient.ThorchainBridge
    utils  *types.MemolessUtils
    logger zerolog.Logger
    rr     refundRecorder
    // test/config hooks
    candidateProvider      func(txIn *btypes.TxInItem, reference, chainID string) []CollisionCandidate
    collisionWiringEnabled bool
}

// NewMemolessHandler creates a new memoless handler instance
func NewMemolessHandler(bridge thorclient.ThorchainBridge, logger zerolog.Logger) *MemolessHandler {
    return &MemolessHandler{
        bridge: bridge,
        utils:  types.NewMemolessUtils(),
        logger: logger.With().Str("module", "memoless_handler").Logger(),
        rr:     newInMemoryRefundRecorder(),
        collisionWiringEnabled: false,
    }
}

// ProcessMemolessTransaction processes a transaction that may be memoless
// Returns the processed TxInItem with full memo if memoless, or original if not
func (m *MemolessHandler) ProcessMemolessTransaction(txIn *btypes.TxInItem) (*btypes.TxInItem, error) {
	// Check if this transaction is memoless
	isMemoless, reference, err := m.utils.IsMemolessTransaction(txIn.Memo, txIn.Coins)
	if err != nil {
		m.logger.Debug().Err(err).Str("tx", txIn.Tx).Msg("error checking if transaction is memoless")
		return txIn, nil // Return original transaction if we can't determine memoless status
	}

	if !isMemoless {
		m.logger.Trace().Str("tx", txIn.Tx).Msg("transaction is not memoless")
		return txIn, nil // Return original transaction
	}

	m.logger.Info().
		Str("tx", txIn.Tx).
		Str("reference", reference).
		Str("amount", txIn.Coins[0].Amount.String()).
		Msg("detected memoless transaction")

    // Query THORChain for the registered memo
    registeredMemo, err := m.bridge.GetRegisteredMemo(reference)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("reference", reference).
			Str("tx", txIn.Tx).
			Msg("failed to retrieve registered memo from THORChain")
		return nil, fmt.Errorf("failed to get registered memo for reference %s: %w", reference, err)
	}

    // Validate that the registered memo is still valid (not expired)
    if err := m.validateRegisteredMemo(registeredMemo, txIn.BlockHeight); err != nil {
		m.logger.Warn().
			Err(err).
			Str("reference", reference).
			Str("tx", txIn.Tx).
			Int64("block_height", txIn.BlockHeight).
			Int64("memo_expires", registeredMemo.ExpiresAt).
			Msg("registered memo validation failed")
		return nil, fmt.Errorf("registered memo validation failed for reference %s: %w", reference, err)
	}

    // Phase 1 (scaffold): Deterministic collision wiring (no-op by default)
    // Build candidate set and compute winner using the lexicographic
    // (inbound_height, txid) rule. For now, we only include the current
    // transaction to avoid behavior changes until full wiring is ready.
    if m.shouldApplyCollisionWiring() {
        candidates := m.buildCollisionCandidates(txIn, reference, registeredMemo.ChainId)
        if m.candidateProvider != nil {
            candidates = m.candidateProvider(txIn, reference, registeredMemo.ChainId)
        }
        winner, losers := DetermineCollisionWinner(candidates)
        if len(losers) > 0 {
            // Schedule refund only if current tx is not the winner
            if !(winner.InboundHeight == txIn.BlockHeight && winner.TxID == txIn.Tx) {
            m.logger.Info().
                Str("tx", txIn.Tx).
                Str("reference", reference).
                Int64("inbound_height", txIn.BlockHeight).
                Str("winner_txid", winner.TxID).
                Msg("collision detected (scaffold); loser handling deferred to refunds phase")
            // Wire loser signal: schedule loser refund via handler seam (no-op until fully integrated)
                if _, err := m.ScheduleLoserRefund(txIn, "REF_COLLISION_LOST", reference, winner.TxID); err != nil {
                m.logger.Warn().Err(err).Msg("failed to schedule loser refund (seam)")
                }
            }
        }
    }

    // Create a new TxInItem with the full memo from THORChain
    processedTx := &btypes.TxInItem{
		BlockHeight:           txIn.BlockHeight,
		Tx:                    txIn.Tx,
		Memo:                  registeredMemo.Memo, // Replace with full memo from THORChain
		Sender:                txIn.Sender,
		To:                    txIn.To,
		Coins:                 m.adjustCoinsForMemoless(txIn.Coins, reference),
		Gas:                   txIn.Gas,
		ObservedVaultPubKey:   txIn.ObservedVaultPubKey,
		Aggregator:            txIn.Aggregator,
		AggregatorTarget:      txIn.AggregatorTarget,
		AggregatorTargetLimit: txIn.AggregatorTargetLimit,
		CommittedUnFinalised:  txIn.CommittedUnFinalised,
	}

	m.logger.Info().
		Str("tx", txIn.Tx).
		Str("reference", reference).
		Str("original_memo", txIn.Memo).
		Str("resolved_memo", registeredMemo.Memo).
		Str("adjusted_amount", processedTx.Coins[0].Amount.String()).
		Msg("successfully processed memoless transaction")

	return processedTx, nil
}

// validateRegisteredMemo validates that the registered memo is still valid
func (m *MemolessHandler) validateRegisteredMemo(memo *stypes.RegisteredMemo, currentHeight int64) error {
	// Check if memo has expired
	if memo.ExpiresAt > 0 && currentHeight > memo.ExpiresAt {
		return fmt.Errorf("memo expired at height %d, current height %d", memo.ExpiresAt, currentHeight)
	}

	// Validate that memo content is not empty
	if len(memo.Memo) == 0 {
		return fmt.Errorf("registered memo content is empty")
	}

	// Additional validation can be added here as needed
	return nil
}

// shouldApplyCollisionWiring returns whether Phase 1 collision decisioning
// should be applied. Default is false to avoid behavior changes until the
// full flow (including loser refunds) is in place.
func (m *MemolessHandler) shouldApplyCollisionWiring() bool {
    return m.collisionWiringEnabled
}

// buildCollisionCandidates constructs a minimal candidate set for the current
// transaction and reference within the scope (chain_id, vault_pubkey, ref).
// For the scaffold, only the current transaction is included.
func (m *MemolessHandler) buildCollisionCandidates(txIn *btypes.TxInItem, reference, chainID string) []CollisionCandidate {
    return []CollisionCandidate{{
        ChainID:       chainID,
        VaultPubKey:   txIn.ObservedVaultPubKey.String(),
        Reference:     reference,
        InboundHeight: txIn.BlockHeight,
        TxID:          txIn.Tx,
    }}
}

// adjustCoinsForMemoless removes the reference digits from the transaction amount
// This restores the original intended amount by removing the 5-digit reference
func (m *MemolessHandler) adjustCoinsForMemoless(coins common.Coins, reference string) common.Coins {
	if len(coins) != 1 {
		// This shouldn't happen for valid memoless transactions, but return as-is if it does
		m.logger.Warn().Int("coin_count", len(coins)).Msg("unexpected coin count for memoless transaction")
		return coins
	}

	coin := coins[0]
	originalAmount := coin.Amount

	// Extract the base amount by removing the reference digits
	amountStr := originalAmount.String()
	if len(amountStr) <= 5 {
		// Amount is too small to have a reference, return minimal amount
		adjustedAmount := common.GetUncappedShare(originalAmount, cosmos.NewUint(100000), cosmos.NewUint(1))
		m.logger.Warn().
			Str("original_amount", originalAmount.String()).
			Str("adjusted_amount", adjustedAmount.String()).
			Msg("amount too small for reference removal")

		return common.Coins{common.NewCoin(coin.Asset, adjustedAmount)}
	}

	// Remove last 5 digits (reference) to get base amount
	baseAmountStr := amountStr[:len(amountStr)-5]
	if baseAmountStr == "" {
		baseAmountStr = "1" // Minimum amount
	}

	baseAmount := cosmos.NewUintFromString(baseAmountStr)

	adjustedCoin := common.NewCoin(coin.Asset, baseAmount)

	m.logger.Debug().
		Str("original_amount", originalAmount.String()).
		Str("base_amount", baseAmount.String()).
		Str("reference", reference).
		Msg("adjusted coin amount for memoless transaction")

	return common.Coins{adjustedCoin}
}

// IsMemolessTransaction checks if a transaction is memoless without processing it
// This is a convenience method for external callers who just want to check status
func (m *MemolessHandler) IsMemolessTransaction(memo string, coins common.Coins) (bool, string) {
	isMemoless, reference, err := m.utils.IsMemolessTransaction(memo, coins)
	if err != nil {
		m.logger.Debug().Err(err).Msg("error checking memoless transaction status")
		return false, ""
	}
	return isMemoless, reference
}
