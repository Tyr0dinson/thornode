package config_test

import (
	"fmt"
	"strings"
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

func (s *ConfigSuite) TestFailoverConfigNewDefaults(c *C) {
	cfg := config.BifrostChainConfiguration{}
	fc := cfg.GetFailoverConfig()
	c.Assert(fc.HealthCheckTimeout, Equals, 5*time.Second)
	c.Assert(fc.IntegrityCheckDepth, Equals, int64(0)) // 0 = disabled; default.yaml enables per chain
}

func (s *ConfigSuite) TestFailoverConfigClampsTimeoutToInterval(c *C) {
	cfg := config.BifrostChainConfiguration{}
	cfg.Failover.HealthCheckInterval = 4 * time.Second
	fc := cfg.GetFailoverConfig()
	c.Assert(fc.HealthCheckTimeout, Equals, 2*time.Second)
	c.Assert(fc.Validate(), IsNil)
}

func (s *ConfigSuite) TestFailoverConfigValidate(c *C) {
	ok := config.BifrostChainConfiguration{}.GetFailoverConfig()
	c.Assert(ok.Validate(), IsNil)

	bad := ok
	bad.ErrorThreshold = 0
	c.Assert(bad.Validate(), ErrorMatches, ".*error_threshold.*")

	bad = ok
	bad.MinRecoveryChecks = 0
	c.Assert(bad.Validate(), ErrorMatches, ".*min_recovery_checks.*")

	bad = ok
	bad.MaxBlockLag = -1
	c.Assert(bad.Validate(), ErrorMatches, ".*max_block_lag.*")

	bad = ok
	bad.HealthCheckInterval = 500 * time.Millisecond
	c.Assert(bad.Validate(), ErrorMatches, ".*health_check_interval.*")

	bad = ok
	bad.HealthCheckTimeout = ok.HealthCheckInterval
	c.Assert(bad.Validate(), ErrorMatches, ".*health_check_timeout.*")

	bad = ok
	bad.IntegrityCheckDepth = -1
	c.Assert(bad.Validate(), ErrorMatches, ".*integrity_check_depth.*")
}

func (s *ConfigSuite) TestValidateEndpoints(c *C) {
	// legacy single host: valid when it carries the hosts its chain needs
	c.Assert(config.BifrostChainConfiguration{ChainID: common.BTCChain, RPCHost: "http://x"}.ValidateEndpoints(), IsNil)
	c.Assert(config.BifrostChainConfiguration{ChainID: common.BTCChain, APIHost: "http://x"}.ValidateEndpoints(), ErrorMatches, `.*endpoint "primary" needs rpc_host.*`)
	c.Assert(config.BifrostChainConfiguration{ChainID: common.TRONChain, APIHost: "http://x"}.ValidateEndpoints(), ErrorMatches, `.*endpoint "primary" needs both rpc_host and api_host.*`)
	c.Assert(config.BifrostChainConfiguration{ChainID: common.GAIAChain, CosmosGRPCHost: "h:9090"}.ValidateEndpoints(), ErrorMatches, `.*endpoint "primary" needs both cosmos_grpc_host and rpc_host.*`)
	// chains without an endpoint manager: a legacy config is not checked here, and an
	// endpoints list is rejected instead of being silently ignored
	c.Assert(config.BifrostChainConfiguration{ChainID: common.XMRChain}.ValidateEndpoints(), IsNil)
	c.Assert(config.BifrostChainConfiguration{ChainID: common.XMRChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "http://x", Priority: 1},
	}}.ValidateEndpoints(), ErrorMatches, ".*XMR client does not support an endpoints list.*")

	dup := config.BifrostChainConfiguration{ChainID: common.BTCChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "http://x", Priority: 1},
		{Name: "a", RPCHost: "http://y", Priority: 2},
	}}
	c.Assert(dup.ValidateEndpoints(), ErrorMatches, `.*duplicate endpoint name "a".*`)

	unnamed := config.BifrostChainConfiguration{ChainID: common.BTCChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "", RPCHost: "http://x", Priority: 1},
	}}
	c.Assert(unnamed.ValidateEndpoints(), ErrorMatches, ".*endpoint 1 has no name.*")

	noHost := config.BifrostChainConfiguration{ChainID: common.BTCChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", Priority: 1},
	}}
	c.Assert(noHost.ValidateEndpoints(), ErrorMatches, `.*endpoint "a" has no host.*`)

	apiOnly := config.BifrostChainConfiguration{ChainID: common.XRPChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", APIHost: "http://x", Priority: 1},
	}}
	c.Assert(apiOnly.ValidateEndpoints(), ErrorMatches, `.*endpoint "a" needs rpc_host.*`)

	gaiaMissingRPC := config.BifrostChainConfiguration{ChainID: common.GAIAChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", CosmosGRPCHost: "h:9090", Priority: 1},
	}}
	c.Assert(gaiaMissingRPC.ValidateEndpoints(), ErrorMatches, `.*endpoint "a" needs both cosmos_grpc_host and rpc_host.*`)

	tronMissingAPI := config.BifrostChainConfiguration{ChainID: common.TRONChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "http://x", Priority: 1},
	}}
	c.Assert(tronMissingAPI.ValidateEndpoints(), ErrorMatches, `.*endpoint "a" needs both rpc_host and api_host.*`)

	tooMany := config.BifrostChainConfiguration{ChainID: common.BTCChain}
	for i := 0; i < 17; i++ {
		tooMany.Endpoints = append(tooMany.Endpoints, config.ChainEndpointConfig{Name: fmt.Sprintf("e%d", i), RPCHost: "http://x", Priority: i + 1})
	}
	c.Assert(tooMany.ValidateEndpoints(), ErrorMatches, ".*at most 16 endpoints.*")
}

// A legacy config carries the chain-level username/password into its synthesized
// endpoint only on chains whose client sent them before endpoint lists existed
// (UTXO and the EVM client); ETH, GAIA, NOBLE, SOL, XRP and TRON never sent them, so
// the default thorchain/password pair from the config anchor must not reach them.
func (s *ConfigSuite) TestLegacyEndpointCarriesBasicAuthOnlyWhereItWasSent(c *C) {
	legacy := func(chain common.Chain) config.ChainEndpointConfig {
		return config.BifrostChainConfiguration{ChainID: chain, RPCHost: "http://x", UserName: "thorchain", Password: "password", AuthorizationBearer: "tok"}.GetEndpoints()[0]
	}
	// the EVM client sent both
	for _, chain := range []common.Chain{common.AVAXChain, common.BSCChain, common.BASEChain, common.POLChain} {
		e := legacy(chain)
		c.Assert(e.UserName, Equals, "thorchain", Commentf("%s", chain))
		c.Assert(e.Password, Equals, "password", Commentf("%s", chain))
		c.Assert(e.AuthorizationBearer, Equals, "tok", Commentf("%s", chain))
	}
	// the UTXO daemons sent username/password only
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.ZECChain} {
		e := legacy(chain)
		c.Assert(e.UserName, Equals, "thorchain", Commentf("%s", chain))
		c.Assert(e.Password, Equals, "password", Commentf("%s", chain))
		c.Assert(e.AuthorizationBearer, Equals, "", Commentf("%s", chain))
	}
	// the EVM client sent username/password only when both were set; otherwise
	// credentials embedded in rpc_host stayed in effect
	for _, chain := range []common.Chain{common.AVAXChain, common.BSCChain, common.BASEChain, common.POLChain} {
		e := config.BifrostChainConfiguration{ChainID: chain, RPCHost: "https://u:k@gw", UserName: "thorchain"}.GetEndpoints()[0]
		c.Assert(e.UserName, Equals, "", Commentf("%s", chain))
		c.Assert(e.Password, Equals, "", Commentf("%s", chain))
	}
	// every other chain sent neither
	for _, chain := range []common.Chain{common.ETHChain, common.GAIAChain, common.NOBLEChain, common.SOLChain, common.XRPChain, common.TRONChain} {
		e := legacy(chain)
		c.Assert(e.UserName, Equals, "", Commentf("%s", chain))
		c.Assert(e.Password, Equals, "", Commentf("%s", chain))
		c.Assert(e.AuthorizationBearer, Equals, "", Commentf("%s", chain))
	}
	// an explicit endpoints list sends exactly what it sets
	eps := config.BifrostChainConfiguration{ChainID: common.GAIAChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "http://x", CosmosGRPCHost: "h:9090", UserName: "u", Password: "p", Priority: 1},
	}}.GetEndpoints()
	c.Assert(eps[0].UserName, Equals, "u")
}

// The endpoint router speaks http(s) only: a host with another scheme fails at
// startup with an error that names the scheme (never the host, which may carry
// credentials), instead of starting and failing every request. Only the hosts the
// chain reads are checked.
func (s *ConfigSuite) TestEndpointHostSchemes(c *C) {
	ok := []string{"node:8545", "http://node:8545", "HTTPS://node:8545", "tcp://gaia:26657", "TCP://gaia:26657"}
	for _, host := range ok {
		c.Assert(config.BifrostChainConfiguration{ChainID: common.ETHChain, RPCHost: host}.ValidateEndpoints(), IsNil, Commentf(host))
	}
	for _, host := range []string{"ws://user:secret@node:8546", "WSS://node:8546", "ipc:///geth.ipc", "unix:///gaia.sock"} {
		err := config.BifrostChainConfiguration{ChainID: common.ETHChain, RPCHost: host}.ValidateEndpoints()
		c.Assert(err, ErrorMatches, ".*rpc_host uses the unsupported .*:// scheme.*", Commentf(host))
		c.Assert(strings.Contains(err.Error(), "secret"), Equals, false)
	}
	// a bare host whose path or query holds "://" has no scheme, so the host (and its
	// credentials) is never reported as one
	c.Assert(config.BifrostChainConfiguration{ChainID: common.ETHChain, RPCHost: "user:secret@node:8545/x?u=http://y"}.ValidateEndpoints(), IsNil)
	// an explicit endpoints list is checked the same way
	err := config.BifrostChainConfiguration{ChainID: common.BSCChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "ws://node:8546", Priority: 1},
	}}.ValidateEndpoints()
	c.Assert(err, ErrorMatches, `.*endpoint "a" rpc_host uses the unsupported ws:// scheme.*`)
	// api_host is read only by TRON, so only TRON checks it
	c.Assert(config.BifrostChainConfiguration{ChainID: common.ETHChain, RPCHost: "http://node", APIHost: "ws://leftover"}.ValidateEndpoints(), IsNil)
	err = config.BifrostChainConfiguration{ChainID: common.TRONChain, RPCHost: "http://node", APIHost: "ws://api"}.ValidateEndpoints()
	c.Assert(err, ErrorMatches, ".*api_host uses the unsupported ws:// scheme.*")
}
