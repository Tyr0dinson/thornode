package memoless

import (
	"fmt"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ChainClientIntegration provides memoless integration for chain clients
type ChainClientIntegration struct {
	detector *MemolessDetector
	logger   zerolog.Logger
	chain    common.Chain
}

// NewChainClientIntegration creates a new chain client integration for memoless processing
func NewChainClientIntegration(chain common.Chain, logger zerolog.Logger) *ChainClientIntegration {
	return &ChainClientIntegration{
		detector: NewMemolessDetector(),
		logger:   logger,
		chain:    chain,
	}
}

// ProcessTransactionBatch processes a batch of transactions for memoless detection and routing
func (ci *ChainClientIntegration) ProcessTransactionBatch(txs []*types.TxInItem) (*BatchProcessingResult, error) {
	if txs == nil || len(txs) == 0 {
		return &BatchProcessingResult{
			MemolessTransactions: []*types.TxInItem{},
			RegularTransactions:  []*types.TxInItem{},
			ProcessingMetrics:    nil,
		}, nil
	}

	// Get processing metrics
	metrics, err := ci.detector.ProcessBatchWithMetrics(txs)
	if err != nil {
		ci.logger.Error().Err(err).Msg("Failed to process transaction batch")
		return nil, fmt.Errorf("failed to process transaction batch: %w", err)
	}

	// Separate memoless and regular transactions
	memolessTransactions := make([]*types.TxInItem, 0)
	regularTransactions := make([]*types.TxInItem, 0)

	for _, tx := range txs {
		if tx == nil {
			continue
		}

		reference, isMemoless := ci.detector.DetectMemolessReference(tx.Memo)
		if isMemoless {
			// Validate the memoless transaction for this chain
			totalAmount := ci.getTotalTransactionAmount(tx)
			valid, err := ci.detector.ValidateMemolessTransaction(ci.chain, totalAmount, reference)

			if err != nil || !valid {
				ci.logger.Warn().
					Str("chain", ci.chain.String()).
					Str("memo", tx.Memo).
					Str("reference", reference).
					Str("amount", totalAmount.String()).
					Err(err).
					Msg("Memoless transaction validation failed, routing as regular transaction")
				regularTransactions = append(regularTransactions, tx)
			} else {
				ci.logger.Info().
					Str("chain", ci.chain.String()).
					Str("reference", reference).
					Str("tx_hash", tx.Tx).
					Msg("Detected valid memoless transaction")
				memolessTransactions = append(memolessTransactions, tx)
			}
		} else {
			regularTransactions = append(regularTransactions, tx)
		}
	}

	result := &BatchProcessingResult{
		MemolessTransactions: memolessTransactions,
		RegularTransactions:  regularTransactions,
		ProcessingMetrics:    metrics,
	}

	ci.logger.Info().
		Int("total_transactions", len(txs)).
		Int("memoless_count", len(memolessTransactions)).
		Int("regular_count", len(regularTransactions)).
		Int64("processing_time_ms", metrics.ProcessingTimeMs).
		Msg("Transaction batch processing completed")

	return result, nil
}

// BatchProcessingResult contains the results of processing a transaction batch
type BatchProcessingResult struct {
	MemolessTransactions []*types.TxInItem          `json:"memoless_transactions"`
	RegularTransactions  []*types.TxInItem          `json:"regular_transactions"`
	ProcessingMetrics    *MemolessProcessingMetrics `json:"processing_metrics"`
}

// IsChainSupported checks if the current chain supports memoless transactions
func (ci *ChainClientIntegration) IsChainSupported() bool {
	config, exists := ci.detector.GetChainConfig(ci.chain)
	if !exists {
		return false
	}
	return config.Supported
}

// GetChainConfiguration returns the memoless configuration for the current chain
func (ci *ChainClientIntegration) GetChainConfiguration() (*ChainMemolessConfig, error) {
	config, exists := ci.detector.GetChainConfig(ci.chain)
	if !exists {
		return nil, fmt.Errorf("chain %s not configured for memoless", ci.chain)
	}
	return config, nil
}

// getTotalTransactionAmount calculates the total amount from transaction coins
func (ci *ChainClientIntegration) getTotalTransactionAmount(tx *types.TxInItem) cosmos.Uint {
	if tx.Coins == nil || len(tx.Coins) == 0 {
		return cosmos.ZeroUint()
	}

	// For memoless validation, we typically use the primary coin amount
	// This could be enhanced to handle multi-coin transactions
	return tx.Coins[0].Amount
}

// ProcessSingleTransaction processes a single transaction for memoless detection
func (ci *ChainClientIntegration) ProcessSingleTransaction(tx *types.TxInItem) (*SingleTransactionResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction cannot be nil")
	}

	result, err := ci.detector.ProcessMemolessTransaction(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to process transaction: %w", err)
	}

	// Validate if it's a memoless transaction
	if result.IsMemoless {
		totalAmount := ci.getTotalTransactionAmount(tx)
		valid, err := ci.detector.ValidateMemolessTransaction(ci.chain, totalAmount, result.Reference)

		if err != nil || !valid {
			ci.logger.Warn().
				Str("reference", result.Reference).
				Str("validation_error", func() string {
					if err != nil {
						return err.Error()
					}
					return "validation failed"
				}()).
				Msg("Memoless transaction failed validation")

			return &SingleTransactionResult{
				Transaction:   tx,
				IsMemoless:    false,
				Reference:     "",
				ValidationErr: err,
			}, nil
		}
	}

	return &SingleTransactionResult{
		Transaction:   tx,
		IsMemoless:    result.IsMemoless,
		Reference:     result.Reference,
		ValidationErr: nil,
	}, nil
}

// SingleTransactionResult contains the result of processing a single transaction
type SingleTransactionResult struct {
	Transaction   *types.TxInItem `json:"transaction"`
	IsMemoless    bool            `json:"is_memoless"`
	Reference     string          `json:"reference"`
	ValidationErr error           `json:"validation_error,omitempty"`
}

// GetDetector returns the underlying memoless detector (for advanced usage)
func (ci *ChainClientIntegration) GetDetector() *MemolessDetector {
	return ci.detector
}
