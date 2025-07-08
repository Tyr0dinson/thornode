package providers

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
	"time"
)

func (s *ProviderTestSuite) TestBitmartProvider(c *C) {
	config, found := s.config[common.ProviderBitmart]
	c.Assert(found, Equals, true)

	provider, err := NewBitmartProvider(s.ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 2)
	c.Assert(provider.tickers, HasLen, 0)

	pair, err := types.NewCurrencyPair("BTC/USDT")
	c.Assert(err, IsNil)

	symbol := provider.toMappedProviderSymbol(pair)
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "BTC_USDT")

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
	c.Assert(btcusdt.Price.Text('f', 2), Equals, "108726.01")

	ethusdt, found := tickers["ETH/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ethusdt.Price.Text('f', 2), Equals, "2606.51")

	usdtbtc, found := tickers["USDT/BTC"]
	c.Assert(found, Equals, true)
	c.Assert(usdtbtc.Price.Text('f', 7), Equals, "0.0000092")

	// test ws data
	provider.Start()

	time.Sleep(time.Second)

	tickers, err = provider.GetTickers()
	c.Assert(err, IsNil)

	btcusdt, found = tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(btcusdt.Price.Text('f', 2), Equals, "108717.98")
}
