//go:build (testnet || mocknet) && !regtest
// +build testnet mocknet
// +build !regtest

package thorchain

import (
	re "regexp"

	"github.com/blang/semver"
)

var adminMimirDenyList = []*re.Regexp{
	// For testnet and mocknet, admin mimir can set any key.
}

func satisfiesTorAnchorWhitelist(version semver.Version, key string) bool { return true }
