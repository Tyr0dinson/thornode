//go:build !mocknet
// +build !mocknet

package basetokens

import (
	_ "embed"
)

//go:embed base_mainnet_latest.json
var BASETokenListRawV3_2_0 []byte

//go:embed base_mainnet_V3_1_0.json
var BASETokenListRawV3_1_0 []byte
