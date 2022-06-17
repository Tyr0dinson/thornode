//go:build stagenet
// +build stagenet

package ethereum

import (
	"gitlab.com/thorchain/thornode/common"
)

var whitelistSmartContractAddress = []common.Address{
	// XRUNE
	common.Address(`0x69fa0feE221AD11012BAb0FdB45d444D3D2Ce71c`),
	// THORSwap Faucet
	common.Address(`0xB73B8E66196f2AF0762833304e3f15dB2e8Df0c3`),
	// THORSwap Wrapped Uniswap V2 Router
	common.Address(`0x1e181df53d07b698c6a58ca6308ab5d827f116e1`),
	// THORSwap Wrapped Uniswap V3 Router
	common.Address(`0x2f8aedd149afbdb5206ecaf8b1a3abb9186c8053`),
	// RangoV1
	common.Address(`0x0e81E5F5555Ece18b5F86F52a736476790eEF82d`),
	// RangoThorchainOutputAggUniV2
	common.Address(`0x4e071DA486bB901BC802758f6d63BCFb7D88345b`),
	// RangoThorchainOutputAggUniV3
	common.Address(`0x128766D155615b53C7655cD78A1A870C110bfdE6`),
}
