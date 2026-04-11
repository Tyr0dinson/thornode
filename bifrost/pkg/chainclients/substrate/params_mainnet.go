//go:build !mocknet
// +build !mocknet

package substrate

import "gitlab.com/thorchain/thornode/v3/common"

var ChainParams = map[common.Chain]Params{
	common.DOTChain: {
		SpecName:               "polkadot",
		MortalEraPeriod:        128,
		DefaultFee:             20_000_000, // 0.002 DOT
		BalanceSize:            16,
		CheckMetadataHash:      true,
		SystemRemarkCall:       0,
		BalancesAllowDeathCall: 0,
		BalancesKeepAliveCall:  3,
		UtilityBatchAllCall:    2,
		SystemPallet:           0,
		BalancesPallet:         5,
		UtilityPallet:          26,
		TxPaymentPallet:        32,
	},
	common.TAOChain: {
		SpecName:               "node-subtensor-runtime",
		MortalEraPeriod:        64,      // 64 blocks * 12s ≈ 12.8 minutes
		DefaultFee:             100_000, // 0.0001 TAO
		BalanceSize:            8,
		CheckMetadataHash:      true,
		SystemRemarkCall:       0,
		BalancesAllowDeathCall: 0,
		BalancesKeepAliveCall:  3,
		UtilityBatchAllCall:    2,
		SystemPallet:           0,
		BalancesPallet:         5,
		UtilityPallet:          11,
		TxPaymentPallet:        6,
	},
}
