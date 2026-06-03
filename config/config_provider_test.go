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

func (s *ConfigSuite) TestGetEndpointsBackwardCompat(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID:  common.BTCChain,
		RPCHost:  "http://localhost:8332",
		UserName: "user",
		Password: "pass",
	}
	endpoints := cfg.GetEndpoints()
	c.Assert(endpoints, HasLen, 1)
	c.Assert(endpoints[0].Name, Equals, "primary")
	c.Assert(endpoints[0].RPCHost, Equals, "http://localhost:8332")
	c.Assert(endpoints[0].Priority, Equals, 1)
	c.Assert(endpoints[0].UserName, Equals, "user")
	c.Assert(endpoints[0].Password, Equals, "pass")
}

func (s *ConfigSuite) TestGetEndpointsUsesExplicitList(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID: common.BTCChain,
		RPCHost: "http://localhost:8332", // ignored when Endpoints is set
		Endpoints: []config.ChainEndpointConfig{
			{Name: "local", RPCHost: "http://localhost:8332", Priority: 1},
			{Name: "ankr", RPCHost: "https://rpc.ankr.com/btc/abc", Priority: 2},
		},
	}
	endpoints := cfg.GetEndpoints()
	c.Assert(endpoints, HasLen, 2)
	c.Assert(endpoints[0].Name, Equals, "local")
	c.Assert(endpoints[1].Name, Equals, "ankr")
}

func (s *ConfigSuite) TestGetEndpointsReturnsSortedByPriority(c *C) {
	cfg := config.BifrostChainConfiguration{
		ChainID: common.BTCChain,
		Endpoints: []config.ChainEndpointConfig{
			{Name: "ankr", RPCHost: "https://rpc.ankr.com/btc", Priority: 2},
			{Name: "local", RPCHost: "http://localhost:8332", Priority: 1},
		},
	}
	endpoints := cfg.GetEndpoints()
	c.Assert(endpoints[0].Name, Equals, "local")
	c.Assert(endpoints[1].Name, Equals, "ankr")
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
