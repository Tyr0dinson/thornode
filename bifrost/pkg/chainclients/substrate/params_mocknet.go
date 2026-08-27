//go:build mocknet
// +build mocknet

package substrate

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

var ChainParams = map[common.Chain]Params{
	// Mocknet runs the real Asset Hub Polkadot runtime (polkadot-omni-node in
	// dev mode; see build/docker/mocknet/polkadot), so these params are
	// identical to the mainnet entry in params_mainnet.go.
	common.DOTChain: {
		SpecName:             "statemint",
		MortalEraPeriod:      128,
		Lookback:             time.Minute,
		DefaultFee:           20_000_000, // 0.002 DOT
		BalanceSize:          16,
		CheckMetadataHash:    true,
		ChargeAssetTxPayment: true,
		Indexes: map[string]byte{
			BalancesPallet:                 10,
			BalancesTransferAllowDeathCall: 0,
			BalancesTransferKeepAliveCall:  3,
			SystemPallet:                   0,
			SystemRemarkCall:               0,
			TransactionPaymentPallet:       11,
			UtilityPallet:                  40,
			UtilityBatchAllCall:            2,
		},
	},
	common.TAOChain: {
		SpecName:          "node-subtensor",
		MortalEraPeriod:   64, // 64 blocks * 12s ≈ 12.8 minutes
		Lookback:          time.Minute,
		DefaultFee:        100_000, // 0.0001 TAO
		BalanceSize:       8,
		CheckMetadataHash: true,
		Indexes: map[string]byte{
			BalancesPallet:                 5,
			BalancesTransferAllowDeathCall: 0,
			BalancesTransferKeepAliveCall:  3,
			SystemPallet:                   0,
			SystemRemarkCall:               0,
			TransactionPaymentPallet:       6,
			UtilityPallet:                  11,
			UtilityBatchAllCall:            2,
		},
	},
}
