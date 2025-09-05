package keeperv1

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// Key prefix for chain configuration storage
const (
	prefixMemolessChainConfig types.DbPrefix = "memoless_chain_config/"
)

// ChainConfig defines memoless configuration for a specific chain
type ChainConfig struct {
	ChainID          string      `json:"chain_id"`
	DecimalPlaces    int         `json:"decimal_places"`
	MinAmount        cosmos.Uint `json:"min_amount"`
	MaxAmount        cosmos.Uint `json:"max_amount"`
	DustThreshold    cosmos.Uint `json:"dust_threshold"`
	SupportsMemoless bool        `json:"supports_memoless"`
	PrecisionLoss    bool        `json:"precision_loss"`
	GasMultiplier    cosmos.Dec  `json:"gas_multiplier"`
}

// GetDefaultChainConfigs returns the default chain configurations for memoless protocol
func GetDefaultChainConfigs() map[string]ChainConfig {
	return map[string]ChainConfig{
		"BTC": {
			ChainID:          "BTC",
			DecimalPlaces:    8,
			MinAmount:        cosmos.NewUint(100000),           // 0.001 BTC
			MaxAmount:        cosmos.NewUint(2100000000000000), // 21M BTC
			DustThreshold:    cosmos.NewUint(546),
			SupportsMemoless: true,
			PrecisionLoss:    false,
			GasMultiplier:    cosmos.NewDec(1),
		},
		"ETH": {
			ChainID:          "ETH",
			DecimalPlaces:    18,
			MinAmount:        cosmos.NewUint(1000000000000000), // 0.001 ETH
			MaxAmount:        cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935"),
			DustThreshold:    cosmos.NewUint(1000000000000000),
			SupportsMemoless: true,
			PrecisionLoss:    false,
			GasMultiplier:    cosmos.NewDec(1),
		},
		"DOGE": {
			ChainID:          "DOGE",
			DecimalPlaces:    8,
			MinAmount:        cosmos.NewUint(100000000),         // 1 DOGE
			MaxAmount:        cosmos.NewUint(21000000000000000), // 210B DOGE
			DustThreshold:    cosmos.NewUint(1000000),
			SupportsMemoless: false, // Precision issues prevent support
			PrecisionLoss:    true,
			GasMultiplier:    cosmos.NewDec(1),
		},
		"BCH": {
			ChainID:          "BCH",
			DecimalPlaces:    8,
			MinAmount:        cosmos.NewUint(100000),           // 0.001 BCH
			MaxAmount:        cosmos.NewUint(2100000000000000), // 21M BCH
			DustThreshold:    cosmos.NewUint(546),
			SupportsMemoless: true,
			PrecisionLoss:    false,
			GasMultiplier:    cosmos.NewDec(1),
		},
		"LTC": {
			ChainID:          "LTC",
			DecimalPlaces:    8,
			MinAmount:        cosmos.NewUint(100000),           // 0.001 LTC
			MaxAmount:        cosmos.NewUint(8400000000000000), // 84M LTC
			DustThreshold:    cosmos.NewUint(546),
			SupportsMemoless: true,
			PrecisionLoss:    false,
			GasMultiplier:    cosmos.NewDec(1),
		},
		"BNB": {
			ChainID:          "BNB",
			DecimalPlaces:    8,
			MinAmount:        cosmos.NewUint(100000),            // 0.001 BNB
			MaxAmount:        cosmos.NewUint(20000000000000000), // 200M BNB
			DustThreshold:    cosmos.NewUint(1),
			SupportsMemoless: true,
			PrecisionLoss:    false,
			GasMultiplier:    cosmos.NewDec(1),
		},
	}
}

// SetChainConfig stores a chain configuration
func (k KVStore) SetChainConfig(ctx cosmos.Context, config ChainConfig) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemolessChainConfig, config.ChainID)

	buf, err := json.Marshal(config)
	if err != nil {
		return dbError(ctx, "SetChainConfig", fmt.Errorf("could not marshal chain config: %w", err))
	}
	store.Set(key, buf)

	return nil
}

// GetChainConfig retrieves a chain configuration
func (k KVStore) GetChainConfig(ctx cosmos.Context, chainID string) (ChainConfig, error) {
	config := ChainConfig{}
	key := k.GetKey(prefixMemolessChainConfig, chainID)
	ok, err := k.getChainConfig(ctx, key, &config)
	if err != nil {
		return config, err
	}
	if !ok {
		// Return default configuration if not found
		defaultConfigs := GetDefaultChainConfigs()
		if defaultConfig, exists := defaultConfigs[chainID]; exists {
			return defaultConfig, nil
		}
		return config, fmt.Errorf("chain configuration not found: %s", chainID)
	}
	return config, nil
}

// getChainConfig is a helper method for retrieving chain configuration data
func (k KVStore) getChainConfig(ctx cosmos.Context, key []byte, config *ChainConfig) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return false, nil
	}
	bz := store.Get(key)
	if err := json.Unmarshal(bz, config); err != nil {
		return false, dbError(ctx, "getChainConfig", err)
	}
	return true, nil
}

// HasChainConfig checks if a chain configuration exists
func (k KVStore) HasChainConfig(ctx cosmos.Context, chainID string) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemolessChainConfig, chainID)
	return store.Has(key)
}

// DeleteChainConfig removes a chain configuration
func (k KVStore) DeleteChainConfig(ctx cosmos.Context, chainID string) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemolessChainConfig, chainID)
	store.Delete(key)
	return nil
}

// ValidateChainForMemoless validates if a chain supports memoless transactions
func (k KVStore) ValidateChainForMemoless(ctx cosmos.Context, chainID string) error {
	config, err := k.GetChainConfig(ctx, chainID)
	if err != nil {
		return fmt.Errorf("failed to get chain config for %s: %w", chainID, err)
	}

	if !config.SupportsMemoless {
		return fmt.Errorf("chain %s does not support memoless protocol", chainID)
	}

	if config.PrecisionLoss {
		return fmt.Errorf("chain %s has precision loss issues, memoless not supported", chainID)
	}

	if config.DecimalPlaces < 5 {
		return fmt.Errorf("chain %s has insufficient decimal places (%d) for 5-digit reference encoding", chainID, config.DecimalPlaces)
	}

	return nil
}

// ValidateAmountForChain validates if an amount is suitable for memoless on a specific chain
func (k KVStore) ValidateAmountForChain(ctx cosmos.Context, chainID string, amount cosmos.Uint) error {
	config, err := k.GetChainConfig(ctx, chainID)
	if err != nil {
		return fmt.Errorf("failed to get chain config for %s: %w", chainID, err)
	}

	// Check minimum amount
	if amount.LT(config.MinAmount) {
		return fmt.Errorf("amount %s below minimum %s for chain %s", amount.String(), config.MinAmount.String(), chainID)
	}

	// Check maximum amount
	if amount.GT(config.MaxAmount) {
		return fmt.Errorf("amount %s exceeds maximum %s for chain %s", amount.String(), config.MaxAmount.String(), chainID)
	}

	// Check dust threshold
	if amount.LT(config.DustThreshold) {
		return fmt.Errorf("amount %s below dust threshold %s for chain %s", amount.String(), config.DustThreshold.String(), chainID)
	}

	// Check if amount can accommodate 5-digit reference without precision loss
	if err := k.ValidateAmountForReference(ctx, chainID, amount); err != nil {
		return err
	}

	return nil
}

// ValidateAmountForReference validates if an amount can accommodate reference encoding
func (k KVStore) ValidateAmountForReference(ctx cosmos.Context, chainID string, amount cosmos.Uint) error {
	config, err := k.GetChainConfig(ctx, chainID)
	if err != nil {
		return fmt.Errorf("failed to get chain config for %s: %w", chainID, err)
	}

	// Calculate minimum amount required for 5-digit reference (99999 in last 5 digits)
	minRequiredAmount := cosmos.NewUint(99999) // Maximum reference value

	// For chains with more than 5 decimal places, we need to ensure
	// the amount is large enough that adding 5 digits doesn't cause precision loss
	decimalDifference := config.DecimalPlaces - 5
	if decimalDifference > 0 {
		// Amount must be at least 6 digits to safely encode 5-digit reference
		minBaseAmount := cosmos.NewUint(100000) // 6 digits minimum
		for i := 0; i < decimalDifference; i++ {
			minBaseAmount = minBaseAmount.MulUint64(10)
		}
		if minBaseAmount.GT(minRequiredAmount) {
			minRequiredAmount = minBaseAmount
		}
	}

	if amount.LT(minRequiredAmount) {
		return fmt.Errorf("amount %s too small for 5-digit reference encoding on chain %s (minimum: %s)",
			amount.String(), chainID, minRequiredAmount.String())
	}

	return nil
}

// GetChainConfigIterator returns an iterator for all chain configurations
func (k KVStore) GetChainConfigIterator(ctx cosmos.Context) cosmos.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemolessChainConfig, "")
	return cosmos.KVStorePrefixIterator(store, key)
}

// InitializeDefaultChainConfigs initializes default chain configurations if they don't exist
func (k KVStore) InitializeDefaultChainConfigs(ctx cosmos.Context) error {
	defaultConfigs := GetDefaultChainConfigs()

	// iterate deterministically over default configs
	keys := make([]string, 0, len(defaultConfigs))
	for chainID := range defaultConfigs {
		keys = append(keys, chainID)
	}
	sort.Strings(keys)
	for _, chainID := range keys {
		config := defaultConfigs[chainID]
		if !k.HasChainConfig(ctx, chainID) {
			if err := k.SetChainConfig(ctx, config); err != nil {
				ctx.Logger().Error("Failed to initialize default config", "chain", chainID, "error", err)
				return err
			}
			ctx.Logger().Info("Initialized default chain config", "chain", chainID)
		}
	}

	return nil
}

// GetSupportedChains returns a list of chains that support memoless protocol
func (k KVStore) GetSupportedChains(ctx cosmos.Context) ([]string, error) {
	supportedChains := make([]string, 0)
	iterator := k.GetChainConfigIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var config ChainConfig
		if err := json.Unmarshal(iterator.Value(), &config); err != nil {
			continue // Skip invalid entries
		}

		if config.SupportsMemoless && !config.PrecisionLoss {
			supportedChains = append(supportedChains, config.ChainID)
		}
	}

	// Also include default supported chains that might not be stored yet
	defaultConfigs := GetDefaultChainConfigs()
	keys := make([]string, 0, len(defaultConfigs))
	for chainID := range defaultConfigs {
		keys = append(keys, chainID)
	}
	sort.Strings(keys)
	for _, chainID := range keys {
		config := defaultConfigs[chainID]
		if config.SupportsMemoless && !config.PrecisionLoss {
			// Check if already in list
			found := false
			for _, existing := range supportedChains {
				if existing == chainID {
					found = true
					break
				}
			}
			if !found {
				supportedChains = append(supportedChains, chainID)
			}
		}
	}

	return supportedChains, nil
}

// EstimateGasCost estimates gas cost for memoless transaction on a specific chain
func (k KVStore) EstimateGasCost(ctx cosmos.Context, chainID string, baseGasCost cosmos.Uint) (cosmos.Uint, error) {
	config, err := k.GetChainConfig(ctx, chainID)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	// Apply chain-specific gas multiplier
	gasCostDec := cosmos.NewDecFromBigInt(baseGasCost.BigInt())
	adjustedGasCost := gasCostDec.Mul(config.GasMultiplier)

	return cosmos.NewUintFromBigInt(adjustedGasCost.TruncateInt().BigInt()), nil
}

// GetChainAsset returns the chain asset for amount validation
func GetChainAsset(chainID string) (common.Asset, error) {
	switch chainID {
	case "BTC":
		return common.BTCAsset, nil
	case "ETH":
		return common.ETHAsset, nil
	case "BNB":
		return common.BNBBEP20Asset, nil
	case "BCH":
		return common.BCHAsset, nil
	case "LTC":
		return common.LTCAsset, nil
	case "DOGE":
		return common.DOGEAsset, nil
	default:
		return common.EmptyAsset, fmt.Errorf("unsupported chain: %s", chainID)
	}
}
