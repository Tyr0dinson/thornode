//go:build !mocknet && !stagenet && !chainnet
// +build !mocknet,!stagenet,!chainnet

package aggregators

func DexAggregators() []Aggregator {
	return DexAggregatorsList()
}
