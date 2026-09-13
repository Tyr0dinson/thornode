package utxo

import (
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// EndpointsSuite is run by the package's existing TestingT call. Do NOT add a func Test.
type EndpointsSuite struct{}

var _ = Suite(&EndpointsSuite{})

func (s *EndpointsSuite) TestBuildUTXOFallbacksSkipsHostlessAndKeepsOrder(c *C) {
	eps := []config.ChainEndpointConfig{
		{Name: "b", RPCHost: "http://b:8332", Priority: 2},
		{Name: "nohost", Priority: 3},
		{Name: "d", APIHost: "http://d:8332", Priority: 4}, // api_host accepted as rpc host
	}
	readers, used := buildUTXOFallbacks(eps, 0, time.Second, common.BTCChain, zerolog.Nop())
	c.Assert(readers, HasLen, 2)
	c.Assert(used, HasLen, 2)
	c.Assert(used[0].Name, Equals, "b")
	c.Assert(used[1].Name, Equals, "d")
	c.Assert(used[1].RPCHost, Equals, "http://d:8332")
}

func (s *EndpointsSuite) TestBuildUTXOFallbacksRejectsBadURL(c *C) {
	eps := []config.ChainEndpointConfig{{Name: "bad", RPCHost: "http://[::1", Priority: 2}}
	readers, used := buildUTXOFallbacks(eps, 0, time.Second, common.BTCChain, zerolog.Nop())
	c.Assert(readers, HasLen, 0)
	c.Assert(used, HasLen, 0)
}
