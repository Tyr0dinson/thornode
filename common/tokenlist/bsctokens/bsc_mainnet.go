//go:build !mocknet
// +build !mocknet

package bsctokens

import (
	_ "embed"
)

//go:embed bsc_mainnet_latest.json
var BSCTokenListRawV3_2_0 []byte

//go:embed bsc_mainnet_V3_0_0.json
var BSCTokenListRawV3_0_0 []byte
