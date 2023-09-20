//go:build (testnet || mocknet) && !regtest
// +build testnet mocknet
// +build !regtest

package thorchain

import (
	re "regexp"
)

var adminMimirDenyList = []*re.Regexp{
	// For testnet and mocknet, admin mimir can set any key.
}
