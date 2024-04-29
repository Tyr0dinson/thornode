//go:build !mocknet && !stagenet
// +build !mocknet,!stagenet

package aggregators

import (
	"github.com/blang/semver"
)

func DexAggregators(version semver.Version) []Aggregator {
	switch {
	case version.GTE(semver.MustParse("1.131.0")):
		return DexAggregatorsV131()
	default:
		return make([]Aggregator, 0)
	}
}
