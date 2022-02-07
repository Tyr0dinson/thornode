//go:build !testnet && !mocknet
// +build !testnet,!mocknet

package aggregators

import (
	"github.com/blang/semver"
)

func DexAggregators(version semver.Version) []Aggregator {
	return nil
}
