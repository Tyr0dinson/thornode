package oracle

import (
	"net/http/httptest"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/mock"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

type OracleTestSuite struct {
	server *httptest.Server
	wsUrl  string
}

var _ = Suite(&OracleTestSuite{})

func (s *OracleTestSuite) SetUpSuite(c *C) {
	s.server = mock.NewServer()
	s.wsUrl = strings.Replace(s.server.URL, "http", "ws", 1)
}

func (s *OracleTestSuite) TearDownSuite(c *C) {
	s.server.Close()
}

func (s *OracleTestSuite) TestNewOracle(c *C) {
	o, err := NewOracle(config.Bifrost{})
	c.Assert(err, NotNil)
	c.Assert(o, IsNil)

	cfg := config.Bifrost{
		Oracle: config.BifrostOracleConfiguration{
			LogLevel: "foo",
		},
	}
	cfg.Providers.Binance = config.BifrostOracleProviderConfiguration{
		Name:            "binance",
		Disabled:        false,
		PollingInterval: time.Second * 5,
		ApiEndpoints:    []string{s.server.URL + "/binance"},
		WsEndpoints:     []string{s.wsUrl + "/binance/ws"},
		Pairs:           []string{"BTC/USDT"},
		SymbolMapping:   nil,
	}
	cfg.Providers.Coinbase = config.BifrostOracleProviderConfiguration{
		Name:            "coinbase",
		Disabled:        true,
		PollingInterval: time.Second * 5,
		ApiEndpoints:    []string{"api"},
		WsEndpoints:     []string{"ws"},
		Pairs:           []string{"BTC/USDT"},
		SymbolMapping:   nil,
	}

	o, err = NewOracle(cfg)
	c.Assert(err, IsNil)
	c.Assert(o, NotNil)
	c.Assert(o.logger.GetLevel().String(), Equals, "info")
	c.Assert(len(o.providers), Equals, 1)

	o.Start()
	time.Sleep(time.Second * 2)

	prices, version := o.GetPrices()
	c.Assert(prices, NotNil)
	c.Assert(version, NotNil)
}
