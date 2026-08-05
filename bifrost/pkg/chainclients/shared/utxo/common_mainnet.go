//go:build !mocknet
// +build !mocknet

package utxo

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func GetConfMulBasisPoint(chain string, bridge thorclient.ThorchainBridge) (cosmos.Uint, error) {
	confMultiplier, err := bridge.GetMimirWithRef(constants.MimirTemplateConfMultiplierBasisPoints, chain)
	// should never be negative
	if err != nil || confMultiplier <= 0 {
		return cosmos.NewUint(constants.MaxBasisPts), err
	}
	return cosmos.NewUint(uint64(confMultiplier)), nil
}

func MaxConfAdjustment(confirm uint64, chain string, bridge thorclient.ThorchainBridge) (uint64, error) {
	adjusted, err := MaxConfAdjustmentUint(cosmos.NewUint(confirm), chain, bridge)
	if err != nil {
		return confirm, err
	}
	adjustedUint64, err := cosmos.UintToUint64(adjusted)
	if err != nil {
		return confirm, err
	}
	return adjustedUint64, nil
}

// MaxConfAdjustmentUint applies the configured confirmation cap without
// narrowing an arbitrary-precision transaction value first.
func MaxConfAdjustmentUint(confirm cosmos.Uint, chain string, bridge thorclient.ThorchainBridge) (cosmos.Uint, error) {
	maxConfirmations, err := bridge.GetMimirWithRef(constants.MimirTemplateMaxConfirmations, chain)
	if err != nil {
		return confirm, err
	}
	if maxConfirmations > 0 {
		maximum := cosmos.NewUint(uint64(maxConfirmations))
		if confirm.GT(maximum) {
			confirm = maximum
		}
	}
	return confirm, nil
}
