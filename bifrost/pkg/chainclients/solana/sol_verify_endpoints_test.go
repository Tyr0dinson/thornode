package solana

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// An endpoint whose identity was never verified in this process (unreachable at
// startup) cannot be asked, but may hold a durable-nonce broadcast from before a
// restart: absence on the other endpoints is not proof, and the cache entry is kept.
func (s *SolTestSuite) TestVerifyBroadcastCountsAnUnverifiedEndpoint(c *C) {
	absent := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer absent.Close()
	eps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: absent.URL, Priority: 1},
		{Name: "b", RPCHost: "http://127.0.0.1:1", Priority: 2},
	}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	cfg.BlockScanner.HTTPRequestTimeout = 2 * time.Second
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	mgr.Entry("a").SetIdentity("genesis") // b never verified
	mgr.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	client := &SOLClient{
		logger:      zerolog.Nop(),
		cfg:         cfg,
		endpointMgr: mgr,
		rpcClient:   rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
	}
	sig := "5Uh4W9xR8sQzX6n1c9n4mZsPqk1Y2fQe7bDdC3oJp9rT"
	c.Assert(client.verifyBroadcast(sig, stypes.TxOutItem{}), Equals, signercache.LookupAmbiguous)
	// once proven to be on another network it no longer counts
	mgr.Disable("b", "identity")
	c.Assert(client.verifyBroadcast(sig, stypes.TxOutItem{}), Equals, signercache.LookupNotFound)
}

// A broadcast that landed and failed on chain is proof the original can never land
// (a signature lands at most once): the re-sign of #3089 goes ahead even while
// another endpoint cannot be asked or does not answer.
func (s *SolTestSuite) TestVerifyBroadcastLandedFailedIsProofOnItsOwn(c *C) {
	failed := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"slot":7,"transaction":{"signatures":["x"],"message":{"accountKeys":[],"instructions":[]}},"meta":{"err":{"InstructionError":[0,"Custom"]}}}}`))
	}))
	defer failed.Close()
	eps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: failed.URL, Priority: 1},
		{Name: "b", RPCHost: "http://127.0.0.1:1", Priority: 2},
	}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	cfg.BlockScanner.HTTPRequestTimeout = 2 * time.Second
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	mgr.Entry("a").SetIdentity("genesis") // b never verified: cannot be asked
	mgr.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	client := &SOLClient{
		logger:      zerolog.Nop(),
		cfg:         cfg,
		endpointMgr: mgr,
		rpcClient:   rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
	}
	c.Assert(client.verifyBroadcast("5Uh4W9xR8sQzX6n1c9n4mZsPqk1Y2fQe7bDdC3oJp9rT", stypes.TxOutItem{}), Equals, signercache.LookupNotFound)
}
