package providers

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
	"time"
)

func (s *ProviderTestSuite) TestBitgetProvider(c *C) {
	config, found := s.config[common.ProviderBitget]
	c.Assert(found, Equals, true)

	provider, err := NewBitgetProvider(s.ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 2)
	c.Assert(provider.tickers, HasLen, 0)

	pair, err := types.NewCurrencyPair("BTC/USDT")
	c.Assert(err, IsNil)

	symbol := provider.toMappedProviderSymbol(pair)
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "BTCUSDT")

	msgs, err := provider.GetSubscriptionMsgs()
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	c.Assert(msgs[0], HasLen, 2)

	provider.Poll()
	c.Assert(provider.tickers, HasLen, 4) // supports reversed pairs

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 4)

	btcusdt, found := tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(btcusdt.Price.Text('f', 2), Equals, "108948.00")

	ethusdt, found := tickers["ETH/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ethusdt.Price.Text('f', 2), Equals, "2611.34")

	usdtbtc, found := tickers["USDT/BTC"]
	c.Assert(found, Equals, true)
	c.Assert(usdtbtc.Price.Text('f', 8), Equals, "0.00000918")

	// test ws data
	provider.Start()

	time.Sleep(time.Second)

	tickers, err = provider.GetTickers()
	c.Assert(err, IsNil)

	btcusdt, found = tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(btcusdt.Price.Text('f', 2), Equals, "108769.72")
}
