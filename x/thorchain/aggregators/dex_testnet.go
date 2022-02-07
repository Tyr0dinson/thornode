//go:build testnet || mocknet
// +build testnet mocknet

package aggregators

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
)

func DexAggregators(version semver.Version) []Aggregator {
	if version.GTE(semver.MustParse("0.1.0")) {
		return []Aggregator{
			// uniswap v2
			{common.ETHChain, common.Address(`0x69ba883Af416fF5501D54D5e27A1f497fBD97156`)},
			// uniswap v3
			{common.ETHChain, common.Address(`0x3b7DbdD635B99cEa39D3d95Dbd0217F05e55B212`)},
			// sushiswap
			{common.ETHChain, common.Address(`0x7fD9bd7A2Cab44820DD2874859E461640F04542D`)},
		}
	}
	return nil
}
