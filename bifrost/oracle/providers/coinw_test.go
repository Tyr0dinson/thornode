package providers

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
	"time"
)

func (s *ProviderTestSuite) TestCoinwProvider(c *C) {
	config, found := s.config[common.ProviderCoinw]
	c.Assert(found, Equals, true)

	provider, err := NewCoinwProvider(s.ctx, s.logger, config, s.metrics)
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
	c.Assert(msgs, HasLen, 2)

	// test ws data
	provider.Start()

	time.Sleep(time.Second)

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 2)

	btcusdt, found := tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(btcusdt.Price.Text('f', 2), Equals, "108976.11")

	ethusdt, found := tickers["ETH/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ethusdt.Price.Text('f', 2), Equals, "2617.62")
}
