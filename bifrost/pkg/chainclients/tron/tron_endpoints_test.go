package tron

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
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
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known, time.Hour), Equals, signercache.LookupFound)
	// absent on a, b broken: ambiguous
	bDown.Store(true)
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known, time.Hour), Equals, signercache.LookupAmbiguous)
	// every endpoint disabled: nothing can prove absence
	mgr.Disable("b", "identity")
	mgr.Disable("a", "identity")
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known, time.Hour), Equals, signercache.LookupAmbiguous)
}

// tronHostPair serves a TRON api_host (getnowblock, getblockbynum) and rpc_host
// (eth_blockNumber, eth_chainId, eth_getBlockByNumber) whose block ids agree unless
// apiBlockID differs from rpcHash.
func tronHostPair(apiBlockID, rpcHash string) (api, rpc *httptest.Server) {
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/wallet/getblock" && !strings.Contains(string(body), "id_or_num"):
			_, _ = w.Write([]byte(`{"blockID":"tip","block_header":{"raw_data":{"number":1000}}}`))
		case r.URL.Path == "/wallet/getblock":
			if !strings.Contains(string(body), `"id_or_num":"980"`) {
				_, _ = w.Write([]byte(`{}`)) // java-tron: block not available
				return
			}
			_, _ = w.Write([]byte(`{"blockID":"` + apiBlockID + `","block_header":{"raw_data":{"number":980}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	rpc = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_blockNumber":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3e8"}`))
		case "eth_chainId":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2b6653dc"}`))
		case "eth_getBlockByNumber":
			if len(req.Params) == 0 || string(req.Params[0]) != `"0x3d4"` {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0x` + rpcHash + `"}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`))
		}
	}))
	return api, rpc
}

// The api_host must serve the same network as the rpc_host: identity verification
// compares a recent block id across the two. Agreement yields the chain id; with a
// pinned identity a disagreement yields a distinct identity (disabled, fatal for
// priority 1); unpinned it leaves the endpoint unverified.
func (s *TronTestSuite) TestAPIHostIdentityMustMatchRPCHost(c *C) {
	const id = "00000000000003d4aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const other = "00000000000003d4bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	build := func(api, rpc *httptest.Server, expected string) func(context.Context, string) (string, error) {
		e := config.ChainEndpointConfig{Name: "a", RPCHost: rpc.URL, APIHost: api.URL, Priority: 1}
		return tronAPIIdentity("a", endpoints.HTTPPostJSON(e, e.APIHost, time.Second), endpoints.NewEVMProbe(e, time.Second), expected, zerolog.Nop())
	}

	api, rpc := tronHostPair(id, id)
	defer api.Close()
	defer rpc.Close()
	got, err := build(api, rpc, "")(context.Background(), "0x2b6653dc")
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "0x2b6653dc")

	badAPI, rpc2 := tronHostPair(other, id)
	defer badAPI.Close()
	defer rpc2.Close()
	_, err = build(badAPI, rpc2, "")(context.Background(), "0x2b6653dc")
	c.Assert(err, ErrorMatches, ".*disagree on a recent block.*")
	c.Assert(errors.Is(err, endpoints.ErrIdentityMismatch), Equals, true) // disabled, even as the only endpoint
	got, err = build(badAPI, rpc2, "0x2b6653dc")(context.Background(), "0x2b6653dc")
	c.Assert(err, IsNil)
	c.Assert(got, Not(Equals), "0x2b6653dc")
	c.Assert(got, Matches, `0x2b6653dc \(api_host block .* != rpc_host .*\)`)

	// through the manager: the pinned mismatch is fatal for priority 1
	e := config.ChainEndpointConfig{Name: "a", RPCHost: rpc2.URL, APIHost: badAPI.URL, Priority: 1}
	fc := config.BifrostChainConfiguration{ChainID: common.TRONChain}.GetFailoverConfig()
	mgr, err := endpoints.NewManager(common.TRONChain, []config.ChainEndpointConfig{e}, fc, zerolog.Nop(), endpoints.WithExpectedIdentity("0x2b6653dc"))
	c.Assert(err, IsNil)
	rpcProbe := endpoints.NewEVMProbe(e, time.Second)
	probe := endpoints.CompoundProbe{Probe: rpcProbe, ExtraIdentity: tronAPIIdentity("a", endpoints.HTTPPostJSON(e, e.APIHost, time.Second), rpcProbe, "0x2b6653dc", zerolog.Nop())}
	err = mgr.Bootstrap(context.Background(), []endpoints.Probe{probe})
	c.Assert(err, ErrorMatches, `(?s).*priority-1 endpoint a reports identity "0x2b6653dc \(api_host block .*but expected_identity is "0x2b6653dc".*`)
}

func (s *TronTestSuite) TestTronIdentity(c *C) {
	got, err := tronIdentity("0x1", "0xABC", "abc", "")
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "0x1")
	_, err = tronIdentity("0x1", "0xabc", "def", "")
	c.Assert(errors.Is(err, endpoints.ErrIdentityMismatch), Equals, true)
	got, err = tronIdentity("0x1", "0xabc", "def", "0x1")
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "0x1 (api_host block def != rpc_host abc)")
	_, err = tronIdentity("0x1", "", "", "")
	c.Assert(err, NotNil)
}
