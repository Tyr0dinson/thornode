//go:build mocknet
// +build mocknet

package substrate

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

var ChainParams = map[common.Chain]Params{
	// Mocknet runs Asset Hub against a local relay chain with mainnet parameters.
	common.DOTChain: {
		SpecName:         "statemint",
		MortalEraPeriod:  128,
		Lookback:         time.Minute,
		DefaultFee:       20_000_000, // 0.002 DOT
		BalanceSize:      16,
		SignedExtensions: SignedExtensions[common.DOTChain],
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
		SpecName: "node-subtensor",
		// Keep the mocknet era near the mainnet duration at a 0.3s block interval.
		MortalEraPeriod:  2048,
		Lookback:         time.Minute,
		DefaultFee:       100_000, // 0.0001 TAO
		BalanceSize:      8,
		SignedExtensions: SignedExtensions[common.TAOChain],
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
