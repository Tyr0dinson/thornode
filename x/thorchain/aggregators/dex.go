package aggregators

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
)

type Aggregator struct {
	Chain   common.Chain
	Address common.Address
}

// FetchDexAggregator - fetches a dex aggregator address that matches the given chain and suffix
func FetchDexAggregator(version semver.Version, chain common.Chain, suffix string) (common.Address, error) {
	for _, agg := range DexAggregators(version) {
		if !chain.Equals(agg.Chain) {
			continue
		}
		if strings.HasSuffix(agg.Address.String(), suffix) {
			return agg.Address, nil
		}
	}

	return common.NoAddress, fmt.Errorf("%s aggregator not found", suffix)
}
