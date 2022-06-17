//go:build testnet || mocknet
// +build testnet mocknet

package ethereum

import (
	"gitlab.com/thorchain/thornode/common"
)

var whitelistSmartContractAddress = []common.Address{
	// THORSwap Faucet
	common.Address(`0x83b0c5136790dDf6cA8D3fb3d220C757e0a91fBe`),
	// aggregator for uniswap v2
	common.Address(`0x942c6dA485FD6cEf255853ef83a149d43A73F18a`),
	// aggregator for uniswap v3
	common.Address(`0x7236D46c894Be8Af0C6b26Dd97608E396Db0f339`),
	// aggregator for sushiswap
	common.Address(`0x7fD9bd7A2Cab44820DD2874859E461640F04542D`),
	// generic
	common.Address(`0xDdf2498f9C57A8BB6dc9c75c64ef007E2592A81F`),
	// RangoV1
	common.Address(`0x0e81E5F5555Ece18b5F86F52a736476790eEF82d`),
	// RangoThorchainOutputAggUniV2
	common.Address(`0x4e071DA486bB901BC802758f6d63BCFb7D88345b`),
	// RangoThorchainOutputAggUniV3
	common.Address(`0x128766D155615b53C7655cD78A1A870C110bfdE6`),
}
