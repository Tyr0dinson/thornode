package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// flakyProxy forwards to target unless down is set, in which case it answers 503.
func flakyProxy(target string, down *atomic.Bool) *httptest.Server {
	u, _ := url.Parse(target)
	rp := httputil.NewSingleHostReverseProxy(u)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rp.ServeHTTP(w, r)
	}))
}

// TestAPIRouterFailsOverBetweenEndpoints exercises the TRON wiring pieces: two api_host
// targets on one manager, a TronApi over the api router, and a failover when the
// active endpoint starts answering 503.
func (s *TronTestSuite) TestAPIRouterFailsOverBetweenEndpoints(c *C) {
	var aDown, bDown atomic.Bool
	a := flakyProxy(s.api.URL, &aDown)
	defer a.Close()
	b := flakyProxy(s.api.URL, &bDown)
	defer b.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: s.rpc.URL, APIHost: a.URL, Priority: 1},
		{Name: "b", RPCHost: s.rpc.URL, APIHost: b.URL, Priority: 2},
	}
	fc := config.BifrostChainConfiguration{ChainID: common.TRONChain}.GetFailoverConfig()
	fc.ErrorThreshold = 1
	mgr, err := endpoints.NewManager(common.TRONChain, cfgEps, fc, zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("0x2b6653dc")
	}
	targets, err := endpoints.TargetsFrom(cfgEps, endpoints.APIHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{
		Base: endpoints.SyntheticBase(common.TRONChain), Targets: targets, WritePaths: []string{"/wallet/broadcasttransaction"},
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second,
		&http.Client{Transport: router, Timeout: 2 * time.Second})

	block, err := apiClient.GetLatestBlock()
	c.Assert(err, IsNil)
	c.Assert(block.Header.RawData.Number, Equals, int64(55088560))
	c.Assert(mgr.Active().Name, Equals, "a")

	aDown.Store(true)
	block, err = apiClient.GetLatestBlock()
	c.Assert(err, IsNil) // served by b
	c.Assert(block.Header.RawData.Number, Equals, int64(55088560))
	c.Assert(mgr.Entry("a").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(mgr.Active().Name, Equals, "b")

	// a write path is never retried on another endpoint once sent: with b active and
	// b down, the broadcast fails instead of moving to a
	bDown.Store(true)
	_, err = apiClient.BroadcastTransaction([]byte(`{"txID":"00"}`))
	c.Assert(err, NotNil)
}

// emptyTxServer answers gettransactionbyid with an empty transaction (absent).
func emptyTxServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
}

func (s *TronTestSuite) TestVerifyBroadcastAsksEveryEndpoint(c *C) {
	const known = "77ddfa70000000000000000000000000000000000000000000000000000000000"
	absent := emptyTxServer()
	defer absent.Close()
	var bDown atomic.Bool
	b := flakyProxy(s.api.URL, &bDown) // the mock knows txs starting with 77ddfa70
	defer b.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: s.rpc.URL, APIHost: absent.URL, Priority: 1},
		{Name: "b", RPCHost: s.rpc.URL, APIHost: b.URL, Priority: 2},
	}
	fc := config.BifrostChainConfiguration{ChainID: common.TRONChain}.GetFailoverConfig()
	mgr, err := endpoints.NewManager(common.TRONChain, cfgEps, fc, zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("0x2b6653dc")
	}
	targets, err := endpoints.TargetsFrom(cfgEps, endpoints.APIHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.TRONChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second})

	// known only to b (a is active and reports it absent)
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known), Equals, signercache.LookupFound)
	// absent on a, b broken: ambiguous
	bDown.Store(true)
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known), Equals, signercache.LookupAmbiguous)
	// every endpoint disabled: nothing can prove absence
	mgr.Disable("b", "identity")
	mgr.Disable("a", "identity")
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known), Equals, signercache.LookupAmbiguous)
}
