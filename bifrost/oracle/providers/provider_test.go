package providers

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/mock"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	. "gopkg.in/check.v1"
)

type ProviderTestSuite struct {
	server  *httptest.Server
	url     string
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

func (s *ProviderTestSuite) SetUpSuite(c *C) {
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
		common.ProviderBitget,
		common.ProviderBitmart,
		common.ProviderCoinw,
	} {
		s.config[provider] = s.newProviderConfig(provider, symbols)
	}
	s.metrics = metrics.NewMetrics()

}

func (s *ProviderTestSuite) TearDownSuite(c *C) {
	s.server.Close()
}

func (s *ProviderTestSuite) newProviderConfig(
	name string,
	symbols []string,
) config.BifrostOracleProviderConfiguration {
	return config.BifrostOracleProviderConfiguration{
		Name:            name,
		Disabled:        false,
		PollingInterval: time.Second * 5,
		ApiEndpoints:    []string{fmt.Sprintf("%s/%s", s.server.URL, name)},
		WsEndpoints:     []string{fmt.Sprintf("%s/%s/ws", s.wsUrl, name)},
		Pairs:           symbols,
	}
}
