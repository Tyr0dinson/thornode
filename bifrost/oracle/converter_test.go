package oracle

import (
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

	converter := NewConverter(tickersBySymbol)

	rates, err := converter.ConvertToUsd()
	c.Assert(err, IsNil)
	c.Assert(len(rates), Equals, 3)
	c.Assert(rates["BTC"].Text('f', 1), Equals, "100000.0")
	// most volume from USDT/BTC pairs, so nearly 1.001 USD
	c.Assert(rates["USDT"].Text('f', 4), Equals, "1.0010")
	// USDT/USD updated with USDT/BTC pairs before converting, ETH/USDT
	// 2600 * 1.001 = 2602.6
	c.Assert(rates["ETH"].Text('f', 2), Equals, "2602.60")
}
