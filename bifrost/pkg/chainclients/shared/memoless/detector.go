package memoless

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// MemolessDetector handles detection and processing of memoless transactions in Bifrost
type MemolessDetector struct {
	// Compiled regex for 5-digit reference detection (thread-safe)
	referencePattern *regexp.Regexp
	// Pattern for REF: prefix detection (thread-safe)
	prefixPattern *regexp.Regexp

	// Chain-specific configuration cache
	chainConfigCache map[common.Chain]*ChainMemolessConfig
	configLock       sync.RWMutex

	// Performance metrics
	totalProcessed int64
	metricsLock    sync.Mutex
}

// ChainMemolessConfig holds memoless configuration for a specific chain
type ChainMemolessConfig struct {
	Supported        bool        `json:"supported"`
	MinAmount        cosmos.Uint `json:"min_amount"`
	DecimalPlaces    int         `json:"decimal_places"`
	HasPrecisionLoss bool        `json:"has_precision_loss"`
	GasMultiplier    float64     `json:"gas_multiplier"`
}

// MemolessTransactionResult contains the result of memoless transaction processing
type MemolessTransactionResult struct {
	Reference    string `json:"reference"`
	OriginalMemo string `json:"original_memo"`
	IsMemoless   bool   `json:"is_memoless"`
	ChainID      string `json:"chain_id"`
}

// MemolessProcessingMetrics contains performance metrics for batch processing
type MemolessProcessingMetrics struct {
	TotalProcessed   int64 `json:"total_processed"`
	MemolessCount    int64 `json:"memoless_count"`
	RegularCount     int64 `json:"regular_count"`
	ProcessingTimeMs int64 `json:"processing_time_ms"`
}

// NewMemolessDetector creates a new memoless detector with compiled regex patterns and chain configs
func NewMemolessDetector() *MemolessDetector {
	detector := &MemolessDetector{
		// Matches exactly 5 digits (thread-safe)
		referencePattern: regexp.MustCompile(`^[0-9]{5}$`),
		// Matches REF: prefix followed by 5 digits (thread-safe)
		prefixPattern: regexp.MustCompile(`^REF:([0-9]{5})$`),
		// Initialize chain config cache
		chainConfigCache: make(map[common.Chain]*ChainMemolessConfig),
		totalProcessed:   0,
	}

	// Initialize default chain configurations
	detector.initializeChainConfigs()

	return detector
}

// initializeChainConfigs sets up default memoless configurations for supported chains
func (d *MemolessDetector) initializeChainConfigs() {
	d.configLock.Lock()
	defer d.configLock.Unlock()

	// Bitcoin - primary memoless target
	d.chainConfigCache[common.BTCChain] = &ChainMemolessConfig{
		Supported:        true,
		MinAmount:        cosmos.NewUint(100000), // 0.001 BTC (100,000 satoshis)
		DecimalPlaces:    8,
		HasPrecisionLoss: false,
		GasMultiplier:    1.0,
	}

	// Ethereum - supported with higher minimum
	d.chainConfigCache[common.ETHChain] = &ChainMemolessConfig{
		Supported:        true,
		MinAmount:        cosmos.NewUint(1000000000000000), // 0.001 ETH (1e15 wei)
		DecimalPlaces:    18,
		HasPrecisionLoss: false,
		GasMultiplier:    1.2, // Higher gas for Ethereum
	}

	// Bitcoin Cash - supported like Bitcoin
	d.chainConfigCache[common.BCHChain] = &ChainMemolessConfig{
		Supported:        true,
		MinAmount:        cosmos.NewUint(100000), // 0.001 BCH
		DecimalPlaces:    8,
		HasPrecisionLoss: false,
		GasMultiplier:    1.0,
	}

	// Litecoin - supported like Bitcoin
	d.chainConfigCache[common.LTCChain] = &ChainMemolessConfig{
		Supported:        true,
		MinAmount:        cosmos.NewUint(100000), // 0.001 LTC
		DecimalPlaces:    8,
		HasPrecisionLoss: false,
		GasMultiplier:    1.0,
	}

	// Dogecoin - not supported due to precision issues
	d.chainConfigCache[common.DOGEChain] = &ChainMemolessConfig{
		Supported:        false,
		MinAmount:        cosmos.ZeroUint(),
		DecimalPlaces:    8,
		HasPrecisionLoss: true, // Precision loss prevents safe reference encoding
		GasMultiplier:    1.0,
	}
}

// DetectMemolessReference detects 5-digit memoless references in transaction memos
func (d *MemolessDetector) DetectMemolessReference(memo string) (string, bool) {
	if memo == "" {
		return "", false
	}

	// Check for exact 5-digit match
	if d.referencePattern.MatchString(memo) {
		return memo, true
	}

	// Check for REF: prefix pattern
	if matches := d.prefixPattern.FindStringSubmatch(memo); matches != nil {
		return matches[1], true // Return the captured 5-digit group
	}

	// Check for reference in last 5 digits of longer numeric strings
	// Only process if it looks like an amount (8+ digits for BTC satoshis)
	if len(memo) >= 8 {
		// Try to parse as number and extract last 5 digits
		if num, err := strconv.ParseUint(memo, 10, 64); err == nil {
			// Extract last 5 digits
			reference := fmt.Sprintf("%05d", num%100000)
			// Validate it's actually a valid reference (10000-99999)
			if refNum, err := strconv.ParseUint(reference, 10, 32); err == nil && refNum >= 10000 {
				return reference, true
			}
		}
	}

	return "", false
}

// ProcessMemolessTransaction processes a TxInItem for memoless transaction detection
func (d *MemolessDetector) ProcessMemolessTransaction(txItem *types.TxInItem) (*MemolessTransactionResult, error) {
	if txItem == nil {
		return nil, fmt.Errorf("txItem cannot be nil")
	}

	reference, isMemoless := d.DetectMemolessReference(txItem.Memo)

	return &MemolessTransactionResult{
		Reference:    reference,
		OriginalMemo: txItem.Memo,
		IsMemoless:   isMemoless,
		ChainID:      "", // Will be set by chain-specific logic
	}, nil
}

// RouteMemolessTransactions routes transactions based on memoless detection
func (d *MemolessDetector) RouteMemolessTransactions(transactions []*types.TxInItem) (memolessCount, regularCount int, err error) {
	if transactions == nil {
		return 0, 0, fmt.Errorf("transactions slice cannot be nil")
	}

	memolessCount = 0
	regularCount = 0

	for _, tx := range transactions {
		if tx == nil {
			continue
		}

		_, isMemoless := d.DetectMemolessReference(tx.Memo)
		if isMemoless {
			memolessCount++
		} else {
			regularCount++
		}
	}

	return memolessCount, regularCount, nil
}

// ValidateMemolessTransaction validates a memoless transaction using cached chain configurations
func (d *MemolessDetector) ValidateMemolessTransaction(chain common.Chain, amount cosmos.Uint, reference string) (bool, error) {
	// Validate reference format
	if !d.referencePattern.MatchString(reference) {
		return false, fmt.Errorf("invalid reference format: %s", reference)
	}

	// Get chain configuration with read lock
	d.configLock.RLock()
	config, exists := d.chainConfigCache[chain]
	d.configLock.RUnlock()

	if !exists {
		return false, fmt.Errorf("chain %s not configured for memoless", chain)
	}

	// Check if chain supports memoless
	if !config.Supported {
		if config.HasPrecisionLoss {
			return false, fmt.Errorf("chain %s not supported for memoless due to precision issues", chain)
		}
		return false, fmt.Errorf("chain %s not supported for memoless", chain)
	}

	// Validate minimum amount
	if amount.LT(config.MinAmount) {
		return false, fmt.Errorf("amount %s too small for memoless on %s (minimum: %s)",
			amount.String(), chain, config.MinAmount.String())
	}

	return true, nil
}

// GetChainConfig returns the memoless configuration for a specific chain
func (d *MemolessDetector) GetChainConfig(chain common.Chain) (*ChainMemolessConfig, bool) {
	d.configLock.RLock()
	defer d.configLock.RUnlock()

	config, exists := d.chainConfigCache[chain]
	if !exists {
		return nil, false
	}

	// Return a copy to prevent external modifications
	configCopy := *config
	return &configCopy, true
}

// ProcessBatchWithMetrics processes a batch of transactions with performance metrics and global tracking
func (d *MemolessDetector) ProcessBatchWithMetrics(transactions []*types.TxInItem) (*MemolessProcessingMetrics, error) {
	if transactions == nil {
		return nil, fmt.Errorf("transactions slice cannot be nil")
	}

	startTime := time.Now()

	memolessCount, regularCount, err := d.RouteMemolessTransactions(transactions)
	if err != nil {
		return nil, fmt.Errorf("failed to route transactions: %w", err)
	}

	processingTime := time.Since(startTime)

	// Update global metrics with thread safety
	d.metricsLock.Lock()
	d.totalProcessed += int64(len(transactions))
	d.metricsLock.Unlock()

	// Ensure minimum 1ms processing time for metrics consistency
	processingTimeMs := processingTime.Milliseconds()
	if processingTimeMs == 0 {
		processingTimeMs = 1
	}

	return &MemolessProcessingMetrics{
		TotalProcessed:   int64(len(transactions)),
		MemolessCount:    int64(memolessCount),
		RegularCount:     int64(regularCount),
		ProcessingTimeMs: processingTimeMs,
	}, nil
}

// GetGlobalMetrics returns the total processing metrics across all batches
func (d *MemolessDetector) GetGlobalMetrics() int64 {
	d.metricsLock.Lock()
	defer d.metricsLock.Unlock()
	return d.totalProcessed
}

// ResetGlobalMetrics resets the global processing counters
func (d *MemolessDetector) ResetGlobalMetrics() {
	d.metricsLock.Lock()
	defer d.metricsLock.Unlock()
	d.totalProcessed = 0
}
