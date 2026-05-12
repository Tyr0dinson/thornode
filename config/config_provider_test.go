package config_test

import (
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ConfigSuite is registered here; it is run by TestPackage in config_test.go.
// Do NOT add a func TestXxx in this file.

type ConfigSuite struct{}

var _ = Suite(&ConfigSuite{})

func (s *ConfigSuite) TestGetProvidersBackwardCompat(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID:  common.BTCChain,
		RPCHost:  "http://localhost:8332",
		UserName: "user",
		Password: "pass",
	}
	providers := cfg.GetProviders()
	c.Assert(providers, HasLen, 1)
	c.Assert(providers[0].Name, Equals, "primary")
	c.Assert(providers[0].RPCHost, Equals, "http://localhost:8332")
	c.Assert(providers[0].Priority, Equals, 1)
	c.Assert(providers[0].UserName, Equals, "user")
	c.Assert(providers[0].Password, Equals, "pass")
	c.Assert(providers[0].Type, Equals, config.ProviderTypeBitcoinRPC)
}

func (s *ConfigSuite) TestGetProvidersUsesExplicitList(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID: common.BTCChain,
		RPCHost: "http://localhost:8332", // ignored when Providers is set
		Providers: []config.ChainProviderConfig{
			{Name: "local", RPCHost: "http://localhost:8332", Priority: 1, Type: config.ProviderTypeBitcoinRPC},
			{Name: "ankr", RPCHost: "https://rpc.ankr.com/btc/abc", Priority: 2, Type: config.ProviderTypeBitcoinRPC},
		},
	}
	providers := cfg.GetProviders()
	c.Assert(providers, HasLen, 2)
	c.Assert(providers[0].Name, Equals, "local")
	c.Assert(providers[1].Name, Equals, "ankr")
}

func (s *ConfigSuite) TestGetProvidersReturnsSortedByPriority(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID: common.BTCChain,
		Providers: []config.ChainProviderConfig{
			{Name: "ankr", RPCHost: "https://rpc.ankr.com/btc", Priority: 2, Type: config.ProviderTypeBitcoinRPC},
			{Name: "local", RPCHost: "http://localhost:8332", Priority: 1, Type: config.ProviderTypeBitcoinRPC},
		},
	}
	providers := cfg.GetProviders()
	c.Assert(providers[0].Name, Equals, "local")
	c.Assert(providers[1].Name, Equals, "ankr")
}

func (s *ConfigSuite) TestFailoverConfigDefaults(c *C) {
	cfg := config.BifrostChainConfiguration{}
	fc := cfg.GetFailoverConfig()
	c.Assert(fc.HealthCheckInterval, Equals, 10*time.Second)
	c.Assert(fc.MaxBlockLag, Equals, int64(3))
	c.Assert(fc.ErrorThreshold, Equals, 5)
	c.Assert(fc.ErrorWindow, Equals, 60*time.Second)
	c.Assert(fc.RecoveryCheckInterval, Equals, 30*time.Second)
	c.Assert(fc.MinRecoveryChecks, Equals, 3)
	c.Assert(fc.PreferHigherPriority, Equals, false)
	c.Assert(fc.RateLimitBackoff, Equals, 30*time.Second)
}
