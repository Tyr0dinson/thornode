//go:build !mocknet
// +build !mocknet

package ethtokens

import (
	_ "embed"
)

//go:embed eth_mainnet_latest.json
var ETHTokenListRawV128 []byte
