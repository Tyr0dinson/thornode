//go:build !mocknet
// +build !mocknet

package soltokens

import (
	_ "embed"
)

//go:embed sol_mainnet_latest.json
var SOLTokenListRaw []byte
