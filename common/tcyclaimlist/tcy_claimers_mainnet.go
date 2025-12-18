//go:build !mocknet && !stagenet && !chainnet
// +build !mocknet,!stagenet,!chainnet

package tcyclaimlist

import (
	_ "embed"
)

//go:embed tcy_claimers_mainnet.json
var TCYClaimsListRaw []byte
