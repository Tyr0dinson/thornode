//go:build !testnet && !mocknet
// +build !testnet,!mocknet

package bsctokens

import (
	_ "embed"
)

//go:embed bsc_mainnet_V106.json
var BSCTokenListRawV106 []byte
