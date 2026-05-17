package tss

import (
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const (
	disableTSSPaillierValidationMimirKey = "DisableTSSPaillierValidation"
	disableTSSPaillierIncidentMimirKey   = "DisableTSSPaillierIncidentMimir"
)

func syncPaillierValidationMimir(logger zerolog.Logger, bridge thorclient.ThorchainBridge, blockHeight int64) bool {
	disabled, err := isActiveMimir(bridge, disableTSSPaillierValidationMimirKey, blockHeight)
	if err != nil {
		tsscommon.SetPaillierValidationEnabled(true)
		logger.Error().
			Err(err).
			Str("mimir_key", disableTSSPaillierValidationMimirKey).
			Msg("failed to fetch Paillier validation Mimir; keeping validation enabled")
		return true
	}

	enabled := !disabled
	tsscommon.SetPaillierValidationEnabled(enabled)
	if disabled {
		logger.Warn().
			Str("mimir_key", disableTSSPaillierValidationMimirKey).
			Int64("height", blockHeight).
			Msg("TSS Paillier validation disabled by Mimir")
	}
	return enabled
}

func isActiveMimir(bridge thorclient.ThorchainBridge, key string, blockHeight int64) (bool, error) {
	value, err := bridge.GetMimir(key)
	if err != nil {
		return false, err
	}
	return value > 0 && value <= blockHeight, nil
}
