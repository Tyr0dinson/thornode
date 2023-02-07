//go:build testnet || mocknet
// +build testnet mocknet

package bsc

import (
	_ "embed"
)

//go:embed token_list_testnet.json
var tokenList []byte
