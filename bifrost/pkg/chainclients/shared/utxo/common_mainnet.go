//go:build !mocknet
// +build !mocknet

package utxo

import (
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

func GetConfMulBasisPoint(chain string, bridge thorclient.ThorchainBridge) (cosmos.Uint, error) {
	confMultiplier, err := bridge.GetMimir(constants.MimirPrefixConfMultiplierBasisPoints + "-" + chain)
	// should never be negative
	if err != nil || confMultiplier <= 0 {
		return cosmos.NewUint(constants.MaxBasisPts), err
	}
	return cosmos.NewUint(uint64(confMultiplier)), nil
}

func MaxConfAdjustment(confirm uint64, chain string, bridge thorclient.ThorchainBridge) (uint64, error) {
	maxConfirmations, err := bridge.GetMimir(constants.MimirPrefixMaxConfirmations + "-" + chain)
	if err != nil {
		return confirm, err
	}
	if maxConfirmations > 0 && confirm > uint64(maxConfirmations) {
		confirm = uint64(maxConfirmations)
	}
	return confirm, nil
}
