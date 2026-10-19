//go:build mocknet
// +build mocknet

package soltokens

import _ "embed"

//go:embed sol_mocknet_latest.json
var SOLTokenListRaw []byte
