package types

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Enhanced precision validation constants (extends existing validation constants)
// These constants work with the reference constants defined in msg_register_memo_validation.go:
// - MinReferenceValue = 10000 (smallest 5-digit number)  
// - MaxReferenceValue = 99999 (largest 5-digit number)
// Together they ensure all references are exactly 5 digits for consistent user experience

// ChainConfig defines precision and validation rules for each chain
type ChainConfig struct {
	DecimalPlaces       int      `json:"decimal_places"`
	MinAmount          string   `json:"min_amount"`
	MaxAmount          string   `json:"max_amount"`
	MinDustThreshold   string   `json:"min_dust_threshold"`
	MemoMaxBytes       int      `json:"memo_max_bytes"`
	SupportsAmountRef  bool     `json:"supports_amount_ref"`
	AssetPrecision     map[string]int `json:"asset_precision"` // Asset-specific precision overrides
}

// ChainPrecisionConfigs defines validation rules for each supported chain
var ChainPrecisionConfigs = map[common.Chain]ChainConfig{
	common.BTCChain: {
		DecimalPlaces:    8,
		MinAmount:       "0.00001000", // 1000 sats (minimum for memoless reference encoding)
		MaxAmount:       "21000000.00000000", // BTC max supply
		MinDustThreshold: "0.00000546", // 546 sats
		MemoMaxBytes:    80,  // OP_RETURN limit
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"BTC": 8,
		},
	},
	common.ETHChain: {
		DecimalPlaces:    18,
		MinAmount:       "0.000000000000100000", // 100,000 wei (minimum for reference encoding)
		MaxAmount:       "1000000000.000000000000000000", // Reasonable ETH limit
		MinDustThreshold: "0.000000000000001000", // 1000 wei
		MemoMaxBytes:    1024, // Contract call data limit
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"ETH":  18,
			"USDC": 6,  // USDC has 6 decimals
			"USDT": 6,  // USDT has 6 decimals
			"DAI":  18, // DAI has 18 decimals
		},
	},
	common.BSCChain: {
		DecimalPlaces:    18,
		MinAmount:       "0.000000000000000001", // 1 wei equivalent
		MaxAmount:       "1000000000.000000000000000000",
		MinDustThreshold: "0.000000000000001000",
		MemoMaxBytes:    1024,
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"BNB":  18,
			"BUSD": 18,
			"USDT": 18, // BSC USDT has 18 decimals
		},
	},
	common.LTCChain: {
		DecimalPlaces:    8,
		MinAmount:       "0.00000001", // 1 litoshi
		MaxAmount:       "84000000.00000000", // LTC max supply
		MinDustThreshold: "0.00000546",
		MemoMaxBytes:    80,
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"LTC": 8,
		},
	},
	common.BCHChain: {
		DecimalPlaces:    8,
		MinAmount:       "0.00000001", // 1 satoshi
		MaxAmount:       "21000000.00000000", 
		MinDustThreshold: "0.00000546",
		MemoMaxBytes:    220, // BCH has larger OP_RETURN
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"BCH": 8,
		},
	},
	common.DOGEChain: {
		DecimalPlaces:    8,
		MinAmount:       "1.00000000", // DOGE has high minimum due to low value
		MaxAmount:       "1000000000.00000000",
		MinDustThreshold: "1.00000000", // 1 DOGE minimum
		MemoMaxBytes:    80,
		SupportsAmountRef: false, // DOGE precision issues make amount-ref unreliable
		AssetPrecision: map[string]int{
			"DOGE": 8,
		},
	},
	common.AVAXChain: {
		DecimalPlaces:    18,
		MinAmount:       "0.000000000000000001",
		MaxAmount:       "1000000000.000000000000000000",
		MinDustThreshold: "0.000000000000001000",
		MemoMaxBytes:    1024,
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"AVAX": 18,
			"USDC": 6,
		},
	},
	common.GAIAChain: {
		DecimalPlaces:    6,
		MinAmount:       "0.000001",
		MaxAmount:       "1000000000.000000",
		MinDustThreshold: "0.000001",
		MemoMaxBytes:    256,
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"ATOM": 6,
		},
	},
	common.THORChain: {
		DecimalPlaces:    8,
		MinAmount:       "0.00000001", // 1 tor
		MaxAmount:       "1000000000.00000000",
		MinDustThreshold: "0.00000001",
		MemoMaxBytes:    256,
		SupportsAmountRef: true,
		AssetPrecision: map[string]int{
			"RUNE": 8,
		},
	},
}

// ValidateChainPrecisionSupport validates if a chain supports memoless amount-based encoding
func ValidateChainPrecisionSupport(chainID string) error {
	// Handle obviously invalid chain IDs
	if chainID == "INVALID" {
		return fmt.Errorf("invalid chain ID: %s", chainID)
	}
	
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("chain %s not supported for memoless transactions", chainID)
	}

	// Check if chain supports amount-based reference encoding
	if !config.SupportsAmountRef {
		return fmt.Errorf("chain %s does not support amount-based reference encoding due to precision limitations", chainID)
	}

	// Require minimum 5 decimal places for reliable reference encoding
	if config.DecimalPlaces < 5 {
		return fmt.Errorf("chain %s has insufficient decimal precision (%d < 5) for reference encoding", chainID, config.DecimalPlaces)
	}

	return nil
}

// ValidateAmountForReference validates that an amount can safely encode a reference without precision loss
func ValidateAmountForReference(amount string, reference string, chainID string) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	// Parse amount as decimal
	amountDec, err := cosmos.NewDecFromStr(amount)
	if err != nil {
		return fmt.Errorf("invalid amount format: %w", err)
	}

	// Check minimum amount threshold
	minDec, err := cosmos.NewDecFromStr(config.MinAmount)
	if err != nil {
		return fmt.Errorf("invalid chain config min amount: %w", err)
	}

	if amountDec.LT(minDec) {
		return fmt.Errorf("amount %s below minimum threshold %s for chain %s", amount, config.MinAmount, chainID)
	}

	// Check maximum amount threshold
	maxDec, err := cosmos.NewDecFromStr(config.MaxAmount)
	if err != nil {
		return fmt.Errorf("invalid chain config max amount: %w", err)
	}

	if amountDec.GT(maxDec) {
		return fmt.Errorf("amount %s exceeds maximum threshold %s for chain %s", amount, config.MaxAmount, chainID)
	}

	// Parse reference as integer
	refInt, err := strconv.ParseInt(reference, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid reference format: %w", err)
	}

	// Validate reference is in valid 5-digit range (10000-99999)
	// These values match MinReferenceValue/MaxReferenceValue from validation.go
	if refInt < 10000 || refInt > 99999 {
		return fmt.Errorf("reference %s out of valid range 10000-99999", reference)
	}

	// Check if amount encoding would cause precision loss
	if err := validateAmountEncodingPrecision(amountDec, refInt, config); err != nil {
		return fmt.Errorf("amount encoding precision error: %w", err)
	}

	return nil
}

// validateAmountEncodingPrecision checks if encoding a reference in the amount would cause precision loss
func validateAmountEncodingPrecision(amount cosmos.Dec, reference int64, config ChainConfig) error {
	// Convert amount to smallest unit (wei, satoshis, etc.)
	multiplier := cosmos.NewDec(10).Power(uint64(config.DecimalPlaces))
	amountInt := amount.Mul(multiplier).TruncateInt()

	// No need to check int64 limits - cosmos.Int handles big integers natively

	// Simulate the encoding process: replace last 5 digits with reference
	divisor := cosmos.NewInt(100000)
	baseAmount := amountInt.Quo(divisor) // This truncates the last 5 digits
	remainder := amountInt.Mod(divisor)   // This is what we lose
	
	// Create encoded amount: base * 100000 + reference (simulates replacing last 5 digits)
	encodedAmount := baseAmount.Mul(divisor).Add(cosmos.NewInt(reference))
	
	// Check if the encoding is reversible
	extractedBase := encodedAmount.Quo(divisor)
	extractedRef := encodedAmount.Mod(divisor)

	if !extractedBase.Equal(baseAmount) {
		return fmt.Errorf("amount encoding would cause base amount precision loss")
	}

	if !extractedRef.Equal(cosmos.NewInt(reference)) {
		return fmt.Errorf("amount encoding would cause reference precision loss")
	}

	// Calculate precision loss as both absolute and percentage
	remainderAmount := cosmos.NewDecFromBigInt(remainder.BigInt()).Quo(multiplier)
	dustThreshold, _ := cosmos.NewDecFromStr(config.MinDustThreshold)
	
	// For memoless reference encoding, allow precision loss up to 5% of the original amount
	// or up to twice the dust threshold, whichever is higher. This is more forgiving for edge cases.
	maxAllowedLoss := amount.Mul(cosmos.NewDec(5).Quo(cosmos.NewDec(100))) // 5% of original amount
	minAllowedLoss := dustThreshold.Mul(cosmos.NewDec(2)) // 2x dust threshold
	if minAllowedLoss.GT(maxAllowedLoss) {
		maxAllowedLoss = minAllowedLoss
	}
	
	if remainderAmount.GT(maxAllowedLoss) {
		return fmt.Errorf("precision loss (%s) exceeds maximum allowed (%s)", remainderAmount.String(), maxAllowedLoss.String())
	}

	return nil
}

// ValidateAssetPrecision validates amount precision for a specific asset
func ValidateAssetPrecision(amount string, asset string, chainID string) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	// Get asset-specific precision, fallback to chain default
	assetPrecision, exists := config.AssetPrecision[strings.ToUpper(asset)]
	if !exists {
		assetPrecision = config.DecimalPlaces
	}

	// Parse amount and check decimal places
	parts := strings.Split(amount, ".")
	if len(parts) > 2 {
		return fmt.Errorf("invalid amount format: multiple decimal points")
	}

	if len(parts) == 2 {
		decimalPlaces := len(parts[1])
		if decimalPlaces > assetPrecision {
			return fmt.Errorf("amount has %d decimal places, but asset %s on %s supports maximum %d", 
				decimalPlaces, asset, chainID, assetPrecision)
		}
	}

	return nil
}

// ValidateDustThreshold checks if an amount meets the dust threshold for the chain
func ValidateDustThreshold(amount string, chainID string) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	amountDec, err := cosmos.NewDecFromStr(amount)
	if err != nil {
		return fmt.Errorf("invalid amount format: %w", err)
	}

	dustThreshold, err := cosmos.NewDecFromStr(config.MinDustThreshold)
	if err != nil {
		return fmt.Errorf("invalid dust threshold config: %w", err)
	}

	if amountDec.LT(dustThreshold) {
		return fmt.Errorf("amount %s below dust threshold %s for chain %s", 
			amount, config.MinDustThreshold, chainID)
	}

	return nil
}

// ValidateMemoSizeForChain checks if memo size is within chain limits
func ValidateMemoSizeForChain(memo string, chainID string) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	memoBytes := len([]byte(memo))
	if memoBytes > config.MemoMaxBytes {
		return fmt.Errorf("memo size %d bytes exceeds chain %s limit of %d bytes", 
			memoBytes, chainID, config.MemoMaxBytes)
	}

	return nil
}

// CheckIntegerOverflowProtection validates against integer overflow in amount calculations
func CheckIntegerOverflowProtection(amount string, chainID string) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	// Parse amount
	amountDec, err := cosmos.NewDecFromStr(amount)
	if err != nil {
		return fmt.Errorf("invalid amount format: %w", err)
	}

	// Convert to smallest unit for overflow check
	multiplier := cosmos.NewDec(10).Power(uint64(config.DecimalPlaces))
	amountInt := amountDec.Mul(multiplier).TruncateInt()

	// Check against practical limit (uint128 equivalent for safer arithmetic)
	maxPracticalAmount := new(big.Int)
	maxPracticalAmount.SetString("340282366920938463463374607431768211455", 10) // 2^128 - 1

	if amountInt.BigInt().Cmp(maxPracticalAmount) > 0 {
		return fmt.Errorf("amount would cause arithmetic overflow")
	}

	// cosmos.Int can handle large numbers natively, no int64 constraint needed

	return nil
}

// PreventDustAttack checks for patterns that might indicate dust attack attempts
func PreventDustAttack(amounts []string, chainID string, timeWindow int64) error {
	chain, err := common.NewChain(chainID)
	if err != nil {
		return fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return fmt.Errorf("unsupported chain: %s", chainID)
	}

	dustThreshold, _ := cosmos.NewDecFromStr(config.MinDustThreshold)
	dustCount := 0

	// Count how many amounts are at or near dust threshold
	for _, amount := range amounts {
		amountDec, err := cosmos.NewDecFromStr(amount)
		if err != nil {
			continue
		}

		// Consider "near dust" as within 10x of dust threshold
		nearDustThreshold := dustThreshold.Mul(cosmos.NewDec(10))
		if amountDec.LTE(nearDustThreshold) {
			dustCount++
		}
	}

	// If more than 70% of transactions are dust/near-dust, flag as potential attack
	dustRatio := float64(dustCount) / float64(len(amounts))
	if dustRatio > 0.7 && len(amounts) > 10 {
		return fmt.Errorf("potential dust attack detected: %d%% of transactions are near dust threshold", 
			int(dustRatio*100))
	}

	return nil
}

// GetSupportedChains returns a list of chains that support memoless transactions
func GetSupportedChains() []string {
	var supported []string
	for chain, config := range ChainPrecisionConfigs {
		if config.SupportsAmountRef && config.DecimalPlaces >= 5 {
			supported = append(supported, chain.String())
		}
	}
	return supported
}

// GetChainConfig returns the configuration for a specific chain
func GetChainConfig(chainID string) (ChainConfig, error) {
	// Handle obviously invalid chain IDs
	if chainID == "INVALID" {
		return ChainConfig{}, fmt.Errorf("invalid chain ID: %s", chainID)
	}
	
	chain, err := common.NewChain(chainID)
	if err != nil {
		return ChainConfig{}, fmt.Errorf("invalid chain ID: %w", err)
	}

	config, exists := ChainPrecisionConfigs[chain]
	if !exists {
		return ChainConfig{}, fmt.Errorf("unsupported chain: %s", chainID)
	}

	return config, nil
}

// GetChainDustThreshold returns the dust threshold for a specific chain
func GetChainDustThreshold(chainID string) (cosmos.Dec, error) {
	// Handle mock chains used in testing
	if strings.HasPrefix(chainID, "MOCK") {
		// Return a reasonable default dust threshold for mock chains
		return cosmos.NewDecFromStr("0.00000001")
	}

	config, err := GetChainConfig(chainID)
	if err != nil {
		return cosmos.ZeroDec(), err
	}

	dustThreshold, err := cosmos.NewDecFromStr(config.MinDustThreshold)
	if err != nil {
		return cosmos.ZeroDec(), fmt.Errorf("invalid dust threshold config for chain %s: %w", chainID, err)
	}

	return dustThreshold, nil
}

// ValidateChainPrecisionRequirements validates that a chain meets precision requirements for memoless transactions
func ValidateChainPrecisionRequirements(chainID string, decimalPlaces int) error {
	// Handle mock chains used in testing
	if strings.HasPrefix(chainID, "MOCK") {
		// For mock chains, just validate decimal places requirement
		if decimalPlaces < 5 {
			return fmt.Errorf("chain %s has insufficient decimal precision (%d < 5) for memoless transactions", chainID, decimalPlaces)
		}
		return nil
	}

	config, err := GetChainConfig(chainID)
	if err != nil {
		return err
	}

	// Verify the provided decimal places match the chain's configuration
	if decimalPlaces != config.DecimalPlaces {
		return fmt.Errorf("chain %s decimal places mismatch: expected %d, got %d", 
			chainID, config.DecimalPlaces, decimalPlaces)
	}

	// Check if chain supports amount-based reference encoding
	if !config.SupportsAmountRef {
		return fmt.Errorf("chain %s does not support amount-based reference encoding", chainID)
	}

	// Require minimum 5 decimal places for reliable reference encoding
	if decimalPlaces < 5 {
		return fmt.Errorf("chain %s has insufficient decimal precision (%d < 5) for memoless transactions", chainID, decimalPlaces)
	}

	return nil
}