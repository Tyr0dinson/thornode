package thorchain

import (
	"fmt"
	"github.com/cosmos/cosmos-sdk/telemetry"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	requiredRates = "BTC,USDT"
	maxDeviation  = 1
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
	// [BTC][thor1...]*big.Float{}
	pricesByAsset := map[string]map[string]*big.Float{}
	for _, symbol := range strings.Split(requiredRates, ",") {
		pricesByAsset[symbol] = map[string]*big.Float{}
	}

	iterator := om.keeper.GetPriceFeedIterator(ctx)
	defer iterator.Close()

	ten := big.NewInt(10)

	for ; iterator.Valid(); iterator.Next() {
		var priceFeed PriceFeed
		om.keeper.Cdc().MustUnmarshal(iterator.Value(), &priceFeed)
		om.keeper.DelPriceFeed(ctx, priceFeed.Node.String())

		// Parse rates string into big.Float
		for _, rate := range priceFeed.Rates {
			divisor := new(big.Int).Exp(ten, big.NewInt(int64(rate.Decimals)), nil)
			amount := new(big.Float).SetUint64(rate.Amount)
			number := new(big.Float).Quo(amount, new(big.Float).SetInt(divisor))

			_, found := pricesByAsset[rate.Symbol]
			if !found {
				continue
			}
			pricesByAsset[rate.Symbol][priceFeed.Node.String()] = number
		}
	}

	// Verify and calculate on chain price
	nodes := []ctypes.AccAddress{}

	missingPrices := map[string]int{}
	deviatingPrices := map[string]int{}

	nodesIterator := om.keeper.GetNodeAccountIterator(ctx)
	for ; nodesIterator.Valid(); nodesIterator.Next() {
		var node NodeAccount
		om.keeper.Cdc().MustUnmarshal(nodesIterator.Value(), &node)
		if node.Status == types.NodeStatus_Active {
			nodes = append(nodes, node.NodeAddress)

			missingPrices[node.NodeAddress.String()] = 0
			deviatingPrices[node.NodeAddress.String()] = 0
		}
	}

	for symbol, priceByNode := range pricesByAsset {
		// remove old prices
		// Todo: remove all via iterator, in case the required symbols changed
		om.keeper.DelPrice(ctx, symbol)

		// TODO: clarify if 67% or 51% of nodes reporting is enough
		if !HasSuperMajority(len(priceByNode), len(nodes)) {
			ctx.Logger().Error(
				"not enough prices for asset", "asset", symbol,
				"num_prices", len(priceByNode), "num_nodes", len(nodes))
			continue
		}

		allPricesForSymbol := []*big.Float{}

		for _, node := range nodes {
			price, found := priceByNode[node.String()]
			if !found {
				missingPrices[node.String()]++
				continue
			}
			allPricesForSymbol = append(allPricesForSymbol, price)
		}

		deviation, median, err := ComputeMedianAbsoluteDeviation(allPricesForSymbol)
		if err != nil {
			ctx.Logger().Error("fail to compute deviation", "err", err)
			continue
		}

		margin := new(big.Float).Mul(deviation, big.NewFloat(maxDeviation))

		lowerBound := new(big.Float).Sub(median, margin)
		upperBound := new(big.Float).Add(median, margin)

		for node, price := range priceByNode {
			if price.Cmp(lowerBound) == -1 || price.Cmp(upperBound) == 1 {
				deviatingPrices[node]++
			}
		}

		err = om.keeper.SetPrice(ctx, OraclePrice{
			Symbol: symbol,
			Amount: median.Text('f', -1),
		})
		if err != nil {
			ctx.Logger().Error("fail to set price", "err", err)
		}

		f, _ := median.Float32()
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "oracle_price"},
			f,
			[]metrics.Label{
				telemetry.NewLabel("symbol", strings.ToLower(symbol)),
			},
		)

		if symbol == "BTC" {
			if os.Getuid() != 1001 {
				continue
			}

			filename := fmt.Sprintf("/tmp/debug-thornode-%s.txt", symbol)
			f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				continue
			}
			defer f.Close()

			line := fmt.Sprintf(
				"enshrined_oracle,source=thornode,symbol=%s value=%s %d\n",
				symbol,
				median.Text('f', -1),
				time.Now().UnixNano(),
			)

			_, _ = f.WriteString(line)
		}
	}

	return nil
}

func ComputeMedianAbsoluteDeviation(values []*big.Float) (*big.Float, *big.Float, error) {
	median, err := ComputeMedian(values)
	if err != nil {
		return nil, nil, err
	}

	deviations := make([]*big.Float, len(values))
	for i, value := range values {
		difference := new(big.Float).Sub(value, median)
		deviations[i] = new(big.Float).Abs(difference)
	}

	mad, err := ComputeMedian(deviations)
	if err != nil {
		return nil, nil, err
	}

	return mad, median, nil
}

func ComputeMedian(values []*big.Float) (*big.Float, error) {
	if len(values) == 1 {
		return values[0], nil
	}

	sort.Slice(values, func(i, j int) bool {
		return values[i].Cmp(values[j]) < 0
	})

	midpoint := len(values) / 2

	if len(values)%2 == 1 {
		return values[midpoint], nil
	}

	sum := new(big.Float).Add(values[midpoint-1], values[midpoint])
	return sum.Quo(sum, big.NewFloat(2)), nil
}
