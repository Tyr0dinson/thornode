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
	common.Address(`0x0e3EB2eAB0e524b69C79E24910f4318dB46bAa9c`),
	// RangoThorchainOutputAggUniV2
	common.Address(`0x2a7813412b8da8d18Ce56FE763B9eb264D8e28a8`),
	// RangoThorchainOutputAggUniV3
	common.Address(`0xbB8De86F3b041B3C084431dcf3159fE4827c5F0D`),
}
