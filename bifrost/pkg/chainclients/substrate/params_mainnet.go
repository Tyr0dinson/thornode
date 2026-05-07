//go:build !mocknet
// +build !mocknet

package substrate

import "gitlab.com/thorchain/thornode/v3/common"

var ChainParams = map[common.Chain]Params{
	common.DOTChain: {
		SpecName:          "polkadot",
		MortalEraPeriod:   128,
		DefaultFee:        20_000_000, // 0.002 DOT
		BalanceSize:       16,
		CheckMetadataHash: true,
		Indexes: map[string]byte{
			BalancesPallet:                 5,
			BalancesTransferAllowDeathCall: 0,
			BalancesTransferKeepAliveCall:  3,
			BalancesTransferEvent:          2,
			SystemPallet:                   0,
			SystemRemarkCall:               0,
			TransactionPaymentPallet:       32,
			UtilityPallet:                  26,
			UtilityBatchAllCall:            2,
		},
	},
	common.TAOChain: {
		SpecName:          "node-subtensor",
		MortalEraPeriod:   64,      // 64 blocks * 12s ≈ 12.8 minutes
		DefaultFee:        100_000, // 0.0001 TAO
		BalanceSize:       8,
		CheckMetadataHash: true,
		Indexes: map[string]byte{
			BalancesPallet:                 5,
			BalancesTransferAllowDeathCall: 0,
			BalancesTransferKeepAliveCall:  3,
			BalancesTransferEvent:          2,
			SystemPallet:                   0,
			SystemRemarkCall:               0,
			TransactionPaymentPallet:       6,
			UtilityPallet:                  11,
			UtilityBatchAllCall:            2,
		},
	},
}
