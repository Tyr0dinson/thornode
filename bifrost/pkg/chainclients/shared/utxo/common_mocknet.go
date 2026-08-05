//go:build mocknet
// +build mocknet

package utxo

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func GetConfMulBasisPoint(chain string, bridge thorclient.ThorchainBridge) (cosmos.Uint, error) {
	return cosmos.NewUint(1), nil
}

func MaxConfAdjustment(confirm uint64, chain string, bridge thorclient.ThorchainBridge) (uint64, error) {
	return 1, nil
}

// MaxConfAdjustmentUint is the arbitrary-precision form used by UTXO clients.
func MaxConfAdjustmentUint(confirm cosmos.Uint, chain string, bridge thorclient.ThorchainBridge) (cosmos.Uint, error) {
	return cosmos.OneUint(), nil
}
