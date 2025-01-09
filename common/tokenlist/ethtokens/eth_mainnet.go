//go:build !mocknet
// +build !mocknet

package ethtokens

import (
	_ "embed"
)

//go:embed eth_mainnet_latest.json
var ETHTokenListRawV3_2_0 []byte

//go:embed eth_mainnet_V3_0_0.json
var ETHTokenListRawV3_0_0 []byte
