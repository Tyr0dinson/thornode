//go:build regtest
// +build regtest

package thorchain

import (
	re "regexp"

	"github.com/blang/semver"
)

var adminMimirDenyList = []*re.Regexp{
	// For regtest, admin mimir can set any key.
}

func satisfiesTorAnchorWhitelist(version semver.Version, key string) bool { return true }
