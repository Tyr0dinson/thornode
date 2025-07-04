package oracle

import (
	"fmt"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	. "gopkg.in/check.v1"
	"math/big"
	"strings"
	"testing"
	"time"
)

type ConverterTestSuite struct{}

var _ = Suite(&ConverterTestSuite{})

func Test(t *testing.T) {
	TestingT(t)
}

func (s *ConverterTestSuite) SetUpSuite(c *C) {}

func newTicker(symbol string, price, volume float64) types.Ticker {
	parts := strings.Split(symbol, "/")
	return types.Ticker{
		Time:   time.Now(),
		Pair:   types.CurrencyPair{Base: parts[0], Quote: parts[1]},
		Price:  big.NewFloat(price),
		Volume: big.NewFloat(volume),
	}
}

func (s *ConverterTestSuite) TestStandardDeviation(c *C) {
	values := []*big.Float{
		big.NewFloat(32),
		big.NewFloat(37),
		big.NewFloat(28),
		big.NewFloat(19),
		big.NewFloat(35),
	}

	deviation, mean, err := ComputeStandardDeviation(values)
	c.Assert(err, IsNil)
	c.Assert(deviation.Text('f', 8), Equals, "6.36867333")
	c.Assert(mean.Text('f', -1), Equals, "30.2")
}

func (s *ConverterTestSuite) TestCappedPercent(c *C) {
	testCases := []struct {
		Threshold float64
		Precision int
		Values    []float64
		Result    []string
	}{
		{
			Threshold: 0.4,
			Precision: 1,
			Values:    []float64{50, 40, 10},
			Result:    []string{"0.4", "0.4", "0.2"},
		},
		{
			Threshold: 0.25,
			Precision: 2,
			Values:    []float64{40, 35, 10, 10, 5},
			Result:    []string{"0.25", "0.25", "0.20", "0.20", "0.10"},
		},
		{
			Threshold: 0.125,
			Precision: 3,
			Values:    []float64{100, 3, 2, 1, 0},
			Result:    []string{"0.125", "0.125", "0.125", "0.125", "0.000"},
		},
		{
			Threshold: 0,
			Precision: 0,
			Values:    []float64{99, 1},
			Result:    []string{"0", "0"},
		},
		{
			Threshold: 1,
			Precision: 2,
			Values:    []float64{1, 99},
			Result:    []string{"0.01", "0.99"},
		},
	}

	for _, tc := range testCases {
		values := map[string]*big.Float{}
		results := map[string]string{}

		for i, value := range tc.Values {
			key := fmt.Sprintf("%d", i)
			values[key] = big.NewFloat(value)
			results[key] = tc.Result[i]
		}

		threshold := big.NewFloat(tc.Threshold)

		for key, value := range ComputeCappedPercentage(values, threshold) {
			c.Assert(value.Text('f', tc.Precision), Equals, results[key])
		}
	}
}

func (s *ConverterTestSuite) TestUsdConversion(c *C) {
	// Test if USD rate calculation favors rates with higher volume
	// Convert USDT/BTC rates first (vol: ~1.75b USD)
	// This results in a USDT price of ~1.001 USD
	// Then calculate ETH/USDT (vol: ~456m USD, at the beginning of the round)

	tickersBySymbol := map[string]map[string]types.Ticker{
		"BTC/USD": {
			"coinbase": newTicker("BTC/USD", 100_000, 7_500),
			"crypto":   newTicker("BTC/USD", 100_000, 7_500),
			"kraken":   newTicker("BTC/USD", 100_000, 1_500),
			"bitstamp": newTicker("BTC/USD", 100_000, 1_000),
		},

		"USDT/USD": {
			"kraken":   newTicker("USDT/USD", 1.002, 370_000_000),
			"coinbase": newTicker("USDT/USD", 1.002, 70_000_000),
			"bitfinex": newTicker("USDT/USD", 1.002, 17_000_000),
		},

		"USDT/BTC": {
			"binance": newTicker("USDT/BTC", 0.00001001, 2_000_000_000),
			"bitget":  newTicker("USDT/BTC", 0.00001001, 900_000_000),
			"gate":    newTicker("USDT/BTC", 0.00001001, 600_000_000),
		},

		"ETH/USDT": {
			"binance": newTicker("ETH/USDT", 2600, 800_000),
			"bitget":  newTicker("ETH/USDT", 2600, 500_000),
			"okx":     newTicker("ETH/USDT", 2600, 330_000),
		},
	}

	converter := NewConverter(tickersBySymbol, metrics.NewMetrics())

	rates, err := converter.ConvertToUsd()
	c.Assert(err, IsNil)
	c.Assert(len(rates), Equals, 3)
	c.Assert(rates["BTC"].Text('f', 1), Equals, "100000.0")
	// most volume from USDT/BTC pairs, so nearly 1.001 USD
	c.Assert(rates["USDT"].Text('f', 8), Equals, "1.00112910")
	// USDT/USD updated with USDT/BTC pairs before converting, ETH/USDT
	// 2600 * 1.00112910 ~ 2602.94
	c.Assert(rates["ETH"].Text('f', 2), Equals, "2602.94")
}
