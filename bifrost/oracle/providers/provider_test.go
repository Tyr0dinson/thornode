package providers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/mock"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

type ProviderTestSuite struct {
	server  *httptest.Server
	wsUrl   string
	ctx     context.Context
	logger  zerolog.Logger
	config  map[string]config.BifrostOracleProviderConfiguration
	metrics *metrics.Metrics
}

var _ = Suite(&ProviderTestSuite{})

func Test(t *testing.T) {
	TestingT(t)
}

func (s *ProviderTestSuite) SetUpSuite(_ *C) {
	symbols := []string{
		"BTC/USDT",
		"ETH/USDT",
		"BTC/USD",
		"ETH/USD",
	}

	s.server = mock.NewServer()
	s.wsUrl = strings.Replace(s.server.URL, "http", "ws", 1)
	s.ctx = context.Background()
	s.logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	s.config = map[string]config.BifrostOracleProviderConfiguration{}
	for _, provider := range []string{
		common.ProviderBinance,
		common.ProviderBitfinex,
		common.ProviderBitget,
		common.ProviderBitmart,
		common.ProviderBitstamp,
		common.ProviderBybit,
		common.ProviderCoinbase,
		common.ProviderCoinw,
		common.ProviderCrypto,
		common.ProviderDigifinex,
		common.ProviderGate,
		common.ProviderGemini,
		common.ProviderHtx,
		common.ProviderKraken,
		common.ProviderKucoin,
		common.ProviderLbank,
		common.ProviderMexc,
		common.ProviderOkx,
		common.ProviderThorchain,
	} {
		s.config[provider] = s.newProviderConfig(provider, symbols)
	}
	s.metrics = metrics.NewMetrics(false)
}

func (s *ProviderTestSuite) TearDownSuite(_ *C) {
	s.server.Close()
}

func (s *ProviderTestSuite) newProviderConfig(
	name string,
	symbols []string,
) config.BifrostOracleProviderConfiguration {
	return config.BifrostOracleProviderConfiguration{
		Name:            name,
		Disabled:        false,
		PollingInterval: time.Second,
		ApiEndpoints:    []string{fmt.Sprintf("%s/%s", s.server.URL, name)},
		WsEndpoints:     []string{fmt.Sprintf("%s/%s/ws", s.wsUrl, name)},
		Pairs:           symbols,
	}
}

func newPair(symbol string) types.CurrencyPair {
	pair, err := types.NewCurrencyPair(symbol)
	if err != nil {
		return types.CurrencyPair{}
	}
	return pair
}

// pollAndWait runs poll and blocks until it has refreshed the given pairs.
// Coinbase and LBank poll each pair in its own goroutine and return at once,
// so without this a REST response can land after, and overwrite, a
// websocket price the test is about to assert.
func pollAndWait(c *C, p *websocketProvider, poll func(), pairs ...string) {
	since := time.Now()
	poll()
	waitForTickers(c, p, func(tickers map[string]types.Ticker) bool {
		for _, pair := range pairs {
			if ticker, ok := tickers[pair]; !ok || ticker.Time.Before(since) {
				return false
			}
		}
		return true
	})
}

// waitForTickerPrices blocks until every pair has the given price.
func waitForTickerPrices(c *C, p *websocketProvider, prices map[string]string) {
	waitForTickers(c, p, func(tickers map[string]types.Ticker) bool {
		for pair, price := range prices {
			if ticker, ok := tickers[pair]; !ok || ticker.Price.Text('f', 2) != price {
				return false
			}
		}
		return true
	})
}

func waitForTickers(c *C, p *websocketProvider, done func(map[string]types.Ticker) bool) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		tickers, err := p.GetTickers()
		c.Assert(err, IsNil)
		if done(tickers) {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("tickers not updated: %v", tickers)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
