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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
	const known = "77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299"
	absent := emptyTxServer()
	defer absent.Close()
	// b holds the payment, solidified and successful
	paid := &failedRetryRPC{fallback: s.api.Config.Handler}
	paid.evidence(known, "", "SUCCESS", 99)
	bNode := httptest.NewServer(paid)
	defer bNode.Close()
	var bDown atomic.Bool
	b := flakyProxy(bNode.URL, &bDown)
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

// A solidified failed execution seen on any endpoint is final on TRON, so it allows
// the failed-payment recovery even when another endpoint has not seen the
// transaction; a success seen on any endpoint always wins over it.
func (s *TronTestSuite) TestVerifyBroadcastFailedOnOneEndpoint(c *C) {
	const sig = "77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299"
	absent := emptyTxServer()
	defer absent.Close()
	failed := &failedRetryRPC{fallback: s.api.Config.Handler}
	failed.evidence(sig, "FAILED", "REVERT", 99)
	failedNode := httptest.NewServer(failed)
	defer failedNode.Close()
	paid := &failedRetryRPC{fallback: s.api.Config.Handler}
	paid.evidence(sig, "", "SUCCESS", 99)
	paidNode := httptest.NewServer(paid)
	defer paidNode.Close()
	verify := func(a, b *httptest.Server) signercache.LookupResult {
		cfgEps := []config.ChainEndpointConfig{
			{Name: "a", RPCHost: s.rpc.URL, APIHost: a.URL, Priority: 1},
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
		return tronVerifyBroadcast(context.Background(), mgr, apiClient, sig, time.Hour)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	c.Assert(verify(absent, failedNode), Equals, signercache.LookupFailed)
	// another endpoint that cannot answer does not hide the failure
	c.Assert(verify(down, failedNode), Equals, signercache.LookupFailed)
	c.Assert(verify(failedNode, paidNode), Equals, signercache.LookupFound)
	c.Assert(verify(paidNode, failedNode), Equals, signercache.LookupFound)
}

// An endpoint whose identity was never verified in this process (unreachable at
// startup) may hold a broadcast from before a restart: while it cannot be asked,
// absence elsewhere is not proof. TRON still skips it once it has been down longer
// than maxDown, as its transactions expire within that.
func (s *TronTestSuite) TestVerifyBroadcastCountsAnUnverifiedEndpoint(c *C) {
	const known = "77ddfa70000000000000000000000000000000000000000000000000000000000"
	absent := emptyTxServer()
	defer absent.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: s.rpc.URL, APIHost: absent.URL, Priority: 1},
		{Name: "b", RPCHost: s.rpc.URL, APIHost: "http://127.0.0.1:1", Priority: 2},
	}
	fc := config.BifrostChainConfiguration{ChainID: common.TRONChain}.GetFailoverConfig()
	mgr, err := endpoints.NewManager(common.TRONChain, cfgEps, fc, zerolog.Nop())
	c.Assert(err, IsNil)
	mgr.Entry("a").SetIdentity("0x2b6653dc") // b never verified
	mgr.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	targets, err := endpoints.TargetsFrom(cfgEps, endpoints.APIHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.TRONChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second})

	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known, time.Hour), Equals, signercache.LookupAmbiguous)
	mgr.Entry("b").SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, known, time.Hour), Equals, signercache.LookupNotFound)
}

// tronHostPair serves a TRON api_host (getnowblock, getblockbynum) and rpc_host
// (eth_blockNumber, eth_chainId, eth_getBlockByNumber) whose block ids agree unless
// apiBlockID differs from rpcHash.
func tronHostPair(apiBlockID, rpcHash string) (api, rpc *httptest.Server) {
	return tronHostPairAt(1000, apiBlockID, rpcHash)
}

// tronHostPairAt is tronHostPair with the api_host tip at apiTip (the rpc_host tip
// stays 1000), so the compared block is 20 below the lower of the two.
func tronHostPairAt(apiTip int, apiBlockID, rpcHash string) (api, rpc *httptest.Server) {
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/wallet/getblock" && !strings.Contains(string(body), "id_or_num"):
			_, _ = w.Write([]byte(`{"blockID":"tip","block_header":{"raw_data":{"number":` + strconv.Itoa(apiTip) + `}}}`))
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
// priority 1); unpinned it is ErrIdentityMismatch, which disables the endpoint.
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
	// an api_host on another network whose tip is far ahead of the rpc_host is still
	// proven different: the compared block is below the lower tip, which both have
	aheadAPI, rpc3 := tronHostPairAt(5000, other, id)
	defer aheadAPI.Close()
	defer rpc3.Close()
	_, err = build(aheadAPI, rpc3, "")(context.Background(), "0x2b6653dc")
	c.Assert(errors.Is(err, endpoints.ErrIdentityMismatch), Equals, true, Commentf("%v", err))
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

// Only an endpoint whose reads failed is reported as blocking re-sign; one that
// answered with evidence that does not prove the outcome yet (a receipt above its
// solidified height) keeps the result ambiguous without being blamed.
func (s *TronTestSuite) TestVerifyBroadcastBlamesOnlyEndpointsThatDidNotAnswer(c *C) {
	const sig = "77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299"
	pending := &failedRetryRPC{fallback: s.api.Config.Handler}
	pending.evidence(sig, "", "SUCCESS", 101) // above the solidified height 100
	pendingNode := httptest.NewServer(pending)
	defer pendingNode.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "pending", RPCHost: s.rpc.URL, APIHost: pendingNode.URL, Priority: 1},
		{Name: "down", RPCHost: s.rpc.URL, APIHost: down.URL, Priority: 2},
	}
	metrics, err := endpoints.NewMetrics(prometheus.NewRegistry())
	c.Assert(err, IsNil)
	fc := config.BifrostChainConfiguration{ChainID: common.TRONChain}.GetFailoverConfig()
	mgr, err := endpoints.NewManager(common.TRONChain, cfgEps, fc, zerolog.Nop(), endpoints.WithMetrics(metrics))
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("0x2b6653dc")
	}
	targets, err := endpoints.TargetsFrom(cfgEps, endpoints.APIHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.TRONChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second})

	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, sig, time.Hour), Equals, signercache.LookupAmbiguous)
	blocked := func(name string) float64 {
		return testutil.ToFloat64(metrics.VerificationBlockedCounter().WithLabelValues("TRON", name))
	}
	c.Assert(blocked("down"), Equals, 1.0)
	c.Assert(blocked("pending"), Equals, 0.0)

	// the pending endpoint alone: ambiguous, nobody blamed
	mgr.Disable("down", "identity")
	c.Assert(tronVerifyBroadcast(context.Background(), mgr, apiClient, sig, time.Hour), Equals, signercache.LookupAmbiguous)
	c.Assert(blocked("pending"), Equals, 0.0)
}

// A verification has the API timeout for each of its reads on one endpoint, as each
// read had before it went through every endpoint; without a timeout it has none.
func (s *TronTestSuite) TestVerificationContextCoversEveryRead(c *C) {
	client := &TronClient{config: config.BifrostChainConfiguration{BlockScanner: config.BifrostBlockScannerConfiguration{HTTPRequestTimeout: 30 * time.Second}}}
	ctx, cancel := client.verificationContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	c.Assert(ok, Equals, true)
	c.Assert(time.Until(deadline) > 89*time.Second && time.Until(deadline) <= 90*time.Second, Equals, true)

	client.config.BlockScanner.HTTPRequestTimeout = 0
	ctx2, cancel2 := client.verificationContext()
	defer cancel2()
	_, ok = ctx2.Deadline()
	c.Assert(ok, Equals, false)
}

// The scanner reads everything from api_host, so an endpoint is only as far as the
// lower of its two hosts: an api_host that stopped advancing while rpc_host follows
// the chain makes the endpoint lag and be evicted, and an api_host that does not
// answer a block fails the probe.
func (s *TronTestSuite) TestProbeHeightIsTheLowerOfTheTwoHosts(c *C) {
	const id = "00000000000003d4aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	height := func(apiTip int) (int64, error) {
		api, rpc := tronHostPairAt(apiTip, id, id)
		defer api.Close()
		defer rpc.Close()
		e := config.ChainEndpointConfig{Name: "a", RPCHost: rpc.URL, APIHost: api.URL, Priority: 1}
		return tronProbe(e, time.Second, "", zerolog.Nop()).Height(context.Background())
	}
	h, err := height(900) // api_host frozen 100 blocks behind its rpc_host
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(900))
	h, err = height(1200) // the rpc_host is the lower one
	c.Assert(err, IsNil)
	c.Assert(h, Equals, int64(1000))

	noBlock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`)) // a proxy answering 200 without a block
	}))
	defer noBlock.Close()
	_, rpc := tronHostPair(id, id)
	defer rpc.Close()
	e := config.ChainEndpointConfig{Name: "a", RPCHost: rpc.URL, APIHost: noBlock.URL, Priority: 1}
	_, err = tronProbe(e, time.Second, "", zerolog.Nop()).Height(context.Background())
	c.Assert(err, ErrorMatches, ".*blockID.*")
}

// java-tron answers {} (HTTP 200) for a block it does not have, such as a lite fullnode
// asked for a block older than the history it keeps. The api router asks the next
// endpoint for it instead of handing the empty answer back as the active endpoint's
// final word, which would retry the same height on the same node for good; nothing is
// charged, and {} is returned only when no endpoint has the block.
func (s *TronTestSuite) TestBlockOneEndpointLacksIsAskedOfTheNext(c *C) {
	var hasBlock atomic.Bool
	// java-tron labels every answer JSON (RateLimiterServlet.service)
	lacking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer lacking.Close()
	keeping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if !hasBlock.Load() {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"blockID":"000000000000002a","block_header":{"raw_data":{"number":42}}}`))
	}))
	defer keeping.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: s.rpc.URL, APIHost: lacking.URL, Priority: 1},
		{Name: "b", RPCHost: s.rpc.URL, APIHost: keeping.URL, Priority: 2},
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
	router, err := newTronAPIRouter(mgr, targets, time.Second, time.Second, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second,
		&http.Client{Transport: router, Timeout: 2 * time.Second})

	// no endpoint has it: the empty answer comes back, as before
	block, err := apiClient.GetBlock(42)
	c.Assert(err, IsNil)
	c.Assert(block.Header.RawData.Number, Equals, int64(0))

	// b has it: b's block, and a is not charged for not having it
	hasBlock.Store(true)
	block, err = apiClient.GetBlock(42)
	c.Assert(err, IsNil)
	c.Assert(block.Header.RawData.Number, Equals, int64(42))
	c.Assert(mgr.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(mgr.Active().Name, Equals, "a")
}

// Only block and transaction lookups are asked of the next endpoint on an empty answer:
// java-tron also answers {} for an account that is not activated, which is the same
// on every endpoint, so asking each in turn would only add latency and load.
func (s *TronTestSuite) TestEmptyAccountIsNotAskedElsewhere(c *C) {
	var aHits, bHits atomic.Int32
	server := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{}`))
		}))
	}
	a, b := server(&aHits), server(&bHits)
	defer a.Close()
	defer b.Close()
	cfgEps := []config.ChainEndpointConfig{
		{Name: "a", RPCHost: s.rpc.URL, APIHost: a.URL, Priority: 1},
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
	router, err := newTronAPIRouter(mgr, targets, time.Second, time.Second, zerolog.Nop())
	c.Assert(err, IsNil)
	apiClient := api.NewTronAPIWithClient(endpoints.SyntheticBase(common.TRONChain).String(), 2*time.Second,
		&http.Client{Transport: router, Timeout: 2 * time.Second})
	_, _ = apiClient.GetAccount("TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	c.Assert(aHits.Load(), Equals, int32(1))
	c.Assert(bHits.Load(), Equals, int32(0))
}
