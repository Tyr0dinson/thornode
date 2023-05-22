//go:build !testnet && !mocknet
// +build !testnet,!mocknet

package bsctokens

import (
	_ "embed"
)

//go:embed bsc_mainnet_V109.json
var BSCTokenListRawV109 []byte
