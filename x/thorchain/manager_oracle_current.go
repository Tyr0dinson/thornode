package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"math/big"
	"regexp"
	"strings"
)

type OracleMgrVCUR struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newOracleMgrVCUR creates a new instance of OracleMgrVCUR
func newOracleMgrVCUR(
	keeper keeper.Keeper,
	eventMgr EventManager,
) *OracleMgrVCUR {
	return &OracleMgrVCUR{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (om *OracleMgrVCUR) BeginBlock(ctx cosmos.Context) error {
	ctx.Logger().Info("-- BeginBlock --")

	priceFeeds := []PriceFeed{}

	iterator := om.keeper.GetPriceFeedIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var priceFeed PriceFeed
		om.keeper.Cdc().MustUnmarshal(iterator.Value(), &priceFeed)
		priceFeeds = append(priceFeeds, priceFeed)
	}

	rates := map[string][]big.Float{}
	reRate := regexp.MustCompile(`^([0-9.]+)([A-Z]+)$`)

	for _, priceFeed := range priceFeeds {
		for _, rate := range strings.Split(priceFeed.Rates, ",") {
			parts := reRate.FindStringSubmatch(rate)
			if len(parts) != 3 {
				continue
			}
			amount, _, err := big.ParseFloat(parts[1], 10, 128, big.ToNearestEven)
			if err != nil {
				continue
			}
			symbol := parts[2]
			_, ok := rates[symbol]
			if !ok {
				rates[symbol] = []big.Float{}
			}
			rates[symbol] = append(rates[symbol], *new(big.Float).Set(amount))
		}
	}

	return nil
}
