//go:build regtest
// +build regtest

package thorchain

import (
	re "regexp"
)

var adminMimirDenyList = []*re.Regexp{
	// For regtest, admin mimir can set any key.
}
