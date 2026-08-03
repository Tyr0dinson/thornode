//go:build !mocknet
// +build !mocknet

package substrate

import "gitlab.com/thorchain/thornode/v3/common"

var ChainParams = map[common.Chain]Params{
	// DOT targets Polkadot Asset Hub (spec_name "statemint"), where user DOT
	// balances live since the Asset Hub Migration (2025-11). The relay chain
	// no longer holds user DOT. Indexes verified against live Asset Hub
	// metadata, specVersion 2003001.
	common.DOTChain: {
		SpecName:             "statemint",
		MortalEraPeriod:      128,
		DefaultFee:           20_000_000, // 0.002 DOT — recalibrate against live TransactionFeePaid before activation
		BalanceSize:          16,
		CheckMetadataHash:    true,
		ChargeAssetTxPayment: true,
		Indexes: map[string]byte{
			BalancesPallet:                 10,
			BalancesTransferAllowDeathCall: 0,
			BalancesTransferKeepAliveCall:  3,
			BalancesTransferEvent:          2,
			SystemPallet:                   0,
			SystemRemarkCall:               0,
			TransactionPaymentPallet:       11,
			UtilityPallet:                  40,
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
