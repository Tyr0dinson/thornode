package endpoints_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type RouterSuite struct{}

var _ = Suite(&RouterSuite{})

type hit struct {
	path   string
	query  string
	auth   string
	method string
}

// rpcServer records hits and answers with status (or 200 + body).
func rpcServer(status *int32, hits *[]hit, body string) *httptest.Server {
	if body == "" {
		body = `{"jsonrpc":"2.0","id":1,"result":"ok"}`
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(b, &req)
		if hits != nil {
			*hits = append(*hits, hit{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), req.Method})
		}
		st := int(atomic.LoadInt32(status))
		if st != http.StatusOK {
			w.WriteHeader(st)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	srv.Config.SetKeepAlivesEnabled(false)
	srv.Start()
	return srv
}

func routerFor(c *C, m *endpoints.Manager, targets []endpoints.Target, writes []string) *http.Client {
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, WriteMethods: writes}, zerolog.Nop())
	c.Assert(err, IsNil)
	return &http.Client{Transport: r, Timeout: 2 * time.Second}
}

func base(m *endpoints.Manager) string { return endpoints.SyntheticBase(m.Chain()).String() }

func post(c *C, hc *http.Client, url, method string) (*http.Response, error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":[]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	return hc.Do(req)
}

func twoEndpointMgr(c *C, chain common.Chain, cfgEps []config.ChainEndpointConfig, threshold int) (*endpoints.Manager, []endpoints.Target) {
	m, err := endpoints.NewManager(chain, cfgEps, fc(threshold, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	targets, err := endpoints.TargetsFrom(cfgEps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	return m, targets
}

func (s *RouterSuite) TestReadFailsOverOn5xxAndRecordsError(c *C) {
	var st1, st2 int32 = http.StatusBadGateway, http.StatusOK
	var h1, h2 []hit
	p := rpcServer(&st1, &h1, "")
	defer p.Close()
	b := rpcServer(&st2, &h2, "")
	defer b.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: p.URL + "/v1/KEY1", Priority: 1, AuthorizationBearer: "T1"},
		{Name: "backup", RPCHost: b.URL + "/other/KEY2", Priority: 2, UserName: "u", Password: "p"},
	}, 2)
	hc := routerFor(c, m, targets, nil)

	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(resp.StatusCode, Equals, http.StatusOK)
	c.Assert(h1, HasLen, 1)
	c.Assert(h1[0].auth, Equals, "Bearer T1")
	c.Assert(h1[0].path, Equals, "/v1/KEY1")
	c.Assert(h2, HasLen, 1)
	c.Assert(h2[0].path, Equals, "/other/KEY2")
	c.Assert(h2[0].auth, Equals, "Basic dTpw")
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy) // 1 of 2
	c.Assert(m.Active().Name, Equals, "primary")

	resp, err = post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Active().Name, Equals, "backup")

	resp, err = post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(h1, HasLen, 2)
	c.Assert(h2, HasLen, 3)
}

func (s *RouterSuite) TestPathSuffixAndQueryArePreservedRelativeToBase(c *C) {
	var st int32 = http.StatusOK
	var hits []hit
	srv := rpcServer(&st, &hits, "")
	defer srv.Close()
	m, err := endpoints.NewManager(common.TRONChain, []config.ChainEndpointConfig{
		{Name: "a", APIHost: "http://127.0.0.1:1/apiv1", RPCHost: "http://x", Priority: 1}, // dead, dial error
		{Name: "b", APIHost: srv.URL + "/tron/KEY?token=x", RPCHost: "http://y", Priority: 2},
	}, fc(5, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	targets, err := endpoints.TargetsFrom([]config.ChainEndpointConfig{
		{Name: "a", APIHost: "http://127.0.0.1:1/apiv1"},
		{Name: "b", APIHost: srv.URL + "/tron/KEY?token=x"},
	}, endpoints.APIHost)
	c.Assert(err, IsNil)
	hc := routerFor(c, m, targets, nil)
	req, _ := http.NewRequest(http.MethodPost, base(m)+"wallet/getnowblock?visible=true", bytes.NewReader([]byte(`{}`)))
	resp, err := hc.Do(req)
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(hits, HasLen, 1)
	c.Assert(hits[0].path, Equals, "/tron/KEY/wallet/getnowblock")
	c.Assert(hits[0].query, Equals, "token=x&visible=true")
}

func (s *RouterSuite) TestWriteIsNotRetriedAfter5xxButIsRetriedOnDialError(c *C) {
	var st1, st2 int32 = http.StatusInternalServerError, http.StatusOK
	var h1, h2 []hit
	p := rpcServer(&st1, &h1, "")
	b := rpcServer(&st2, &h2, "")
	defer b.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: p.URL, Priority: 1},
		{Name: "backup", RPCHost: b.URL, Priority: 2},
	}, 5)
	hc := routerFor(c, m, targets, []string{"eth_sendRawTransaction"})

	_, err := post(c, hc, base(m), "eth_sendRawTransaction")
	c.Assert(err, ErrorMatches, "(?s).*endpoint primary: http 500.*") // returned as an error, never retried
	c.Assert(h1, HasLen, 1)
	c.Assert(h2, HasLen, 0)

	p.Close() // dial error on the primary: provably unsent, so the write moves on
	resp, err := post(c, hc, base(m), "eth_sendRawTransaction")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(resp.StatusCode, Equals, http.StatusOK)
	c.Assert(h2, HasLen, 1)
}

func (s *RouterSuite) TestRateLimitPutsEndpointInCooldown(c *C) {
	var st1, st2 int32 = http.StatusTooManyRequests, http.StatusOK
	var h1, h2 []hit
	p := rpcServer(&st1, &h1, "")
	defer p.Close()
	b := rpcServer(&st2, &h2, "")
	defer b.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{
		{Name: "primary", RPCHost: p.URL, Priority: 1},
		{Name: "backup", RPCHost: b.URL, Priority: 2},
	}, 5)
	hc := routerFor(c, m, targets, nil)
	for i := 0; i < 3; i++ {
		resp, err := post(c, hc, base(m), "eth_blockNumber")
		c.Assert(err, IsNil)
		_ = resp.Body.Close()
		c.Assert(resp.StatusCode, Equals, http.StatusOK)
	}
	c.Assert(h1, HasLen, 1)
	c.Assert(h2, HasLen, 3)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
}

func (s *RouterSuite) TestJSONRPCErrorsAndRedirectsAreClassified(c *C) {
	var stErr, stOK, stRedirect int32 = http.StatusOK, http.StatusOK, http.StatusFound
	var h1, h2, h3 []hit
	notFound := rpcServer(&stErr, &h1, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	defer notFound.Close()
	limited := rpcServer(&stErr, &h3, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"limit"}}`)
	defer limited.Close()
	redirect := rpcServer(&stRedirect, nil, "")
	defer redirect.Close()
	good := rpcServer(&stOK, &h2, `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted"}}`)
	defer good.Close()
	m, err := endpoints.NewManager(common.ETHChain, []config.ChainEndpointConfig{
		{Name: "nf", RPCHost: notFound.URL, Priority: 1},
		{Name: "lim", RPCHost: limited.URL, Priority: 2},
		{Name: "redir", RPCHost: redirect.URL, Priority: 3},
		{Name: "good", RPCHost: good.URL, Priority: 4},
	}, fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	targets, err := endpoints.TargetsFrom([]config.ChainEndpointConfig{
		{Name: "nf", RPCHost: notFound.URL}, {Name: "lim", RPCHost: limited.URL}, {Name: "redir", RPCHost: redirect.URL}, {Name: "good", RPCHost: good.URL},
	}, endpoints.RPCHost)
	c.Assert(err, IsNil)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_call")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, ".*execution reverted.*") // application error passed through intact
	c.Assert(m.Entry("nf").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("lim").InCooldown(), Equals, true)
	c.Assert(m.Entry("redir").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("good").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "good")
}

func (s *RouterSuite) TestApplicationHTTPErrorsAreNotFailover(c *C) {
	var st1, st2 int32 = http.StatusBadRequest, http.StatusOK
	var h1, h2 []hit
	p := rpcServer(&st1, &h1, "")
	defer p.Close()
	b := rpcServer(&st2, &h2, "")
	defer b.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "primary", RPCHost: p.URL, Priority: 1}, {Name: "backup", RPCHost: b.URL, Priority: 2}}, 1)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_call")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(resp.StatusCode, Equals, http.StatusBadRequest)
	c.Assert(h2, HasLen, 0)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
}

func (s *RouterSuite) TestAllDownStillTriesEveryEndpointAndRedactsError(c *C) {
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{
		{Name: "a", RPCHost: "http://127.0.0.1:1/SECRET_A", Priority: 1},
		{Name: "b", RPCHost: "http://127.0.0.1:1/SECRET_B", Priority: 2},
	}, 1)
	hc := routerFor(c, m, targets, nil)
	_, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
	c.Assert(err.Error(), Matches, "(?s).*endpoint b.*")
	c.Assert(m.AllDown(), Equals, true)
	_, err = post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
}

func (s *RouterSuite) TestPinnedRequestGoesToNamedEndpointOnly(c *C) {
	var st1, st2 int32 = http.StatusOK, http.StatusOK
	var h1, h2 []hit
	p := rpcServer(&st1, &h1, "")
	defer p.Close()
	b := rpcServer(&st2, &h2, "")
	defer b.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "primary", RPCHost: p.URL, Priority: 1}, {Name: "backup", RPCHost: b.URL, Priority: 2}}, 1)
	m.RecordError("backup", endpoints.ErrorKindTransport) // unhealthy, still reachable when pinned
	hc := routerFor(c, m, targets, nil)
	req, _ := http.NewRequestWithContext(endpoints.PinContext(context.Background(), "backup"), http.MethodPost, base(m), bytes.NewReader([]byte(`{"method":"eth_getTransactionByHash"}`)))
	resp, err := hc.Do(req)
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(h1, HasLen, 0)
	c.Assert(h2, HasLen, 1)
	req, _ = http.NewRequestWithContext(endpoints.PinContext(context.Background(), "nope"), http.MethodPost, base(m), nil)
	_, err = hc.Do(req)
	c.Assert(err, ErrorMatches, ".*pinned endpoint nope not configured.*")
}

func (s *RouterSuite) TestTargetsFromAndBaseValidation(c *C) {
	t, err := endpoints.TargetsFrom([]config.ChainEndpointConfig{{Name: "a", RPCHost: "x:8545"}, {Name: "b"}}, endpoints.RPCHost)
	c.Assert(err, IsNil)
	c.Assert(t, HasLen, 2)
	c.Assert(t[0].URL.String(), Equals, "http://x:8545")
	c.Assert(t[1].URL, IsNil)
	_, err = endpoints.TargetsFrom([]config.ChainEndpointConfig{{Name: "a", RPCHost: "http://[::1"}}, endpoints.RPCHost)
	c.Assert(err, NotNil)

	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "a", RPCHost: "http://a", Priority: 1}, {Name: "b", RPCHost: "http://b", Priority: 2}}, 1)
	bad := endpoints.SyntheticBase(m.Chain())
	bad.RawQuery = "k=v"
	_, err = endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: bad, Targets: targets}, zerolog.Nop())
	c.Assert(err, ErrorMatches, ".*userinfo or a query.*")
	_, err = endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets[:1]}, zerolog.Nop())
	c.Assert(err, ErrorMatches, ".*1 targets for 2 endpoints.*")
}

func (s *RouterSuite) TestBatchResponsesAreClassified(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	limited := rpcServer(&st, &h1, `[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"error":{"code":-32005,"message":"limit"}}]`)
	defer limited.Close()
	good := rpcServer(&st, &h2, `[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"result":"0x2"}]`)
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "lim", RPCHost: limited.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 5)
	hc := routerFor(c, m, targets, nil)
	req, _ := http.NewRequest(http.MethodPost, base(m), bytes.NewReader([]byte(`[{"method":"eth_getTransactionReceipt"},{"method":"eth_getTransactionReceipt"}]`)))
	resp, err := hc.Do(req)
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"0x2".*`) // served by good after lim's batch carried a rate limit
	c.Assert(m.Entry("lim").InCooldown(), Equals, true)
	c.Assert(h2, HasLen, 1)
}

func (s *RouterSuite) TestTruncatedBodyIsAnEndpointFailure(c *C) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "200")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"re`)) // handler returns early: connection cut
	}))
	defer broken.Close()
	var st int32 = http.StatusOK
	var h2 []hit
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "broken", RPCHost: broken.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"ok".*`)
	c.Assert(m.Entry("broken").State(), Equals, endpoints.StateUnhealthy)
}

func (s *RouterSuite) TestWritesRefusedWhenEveryEndpointDisabled(c *C) {
	var st int32 = http.StatusOK
	srv := rpcServer(&st, nil, "")
	defer srv.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "a", RPCHost: srv.URL, Priority: 1}, {Name: "b", RPCHost: srv.URL, Priority: 2}}, 1)
	m.Disable("b", "identity")
	m.Disable("a", "identity")
	hc := routerFor(c, m, targets, []string{"eth_sendRawTransaction"})
	_, err := post(c, hc, base(m), "eth_sendRawTransaction")
	c.Assert(err, ErrorMatches, "(?s).*no permitted write endpoint.*")
	_, err = post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, ErrorMatches, "(?s).*no endpoint available.*")
}

func (s *RouterSuite) TestAttemptBudgetLeavesTimeForNextCandidate(c *C) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	var st int32 = http.StatusOK
	var h2 []hit
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "slow", RPCHost: slow.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 5)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, AttemptTimeout: 10 * time.Second}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base(m), bytes.NewReader([]byte(`{"method":"eth_blockNumber"}`)))
	start := time.Now()
	resp, err := hc.Do(req)
	c.Assert(err, IsNil) // slow got ~300ms, good served within the caller's 600ms
	_ = resp.Body.Close()
	c.Assert(time.Since(start) < 600*time.Millisecond, Equals, true)
	c.Assert(h2, HasLen, 1)
	c.Assert(m.Entry("slow").Snapshot().InlineErrors, Equals, 1)
}

func (s *RouterSuite) TestBodyReadKeepsCallerBudgetAfterHeaders(c *C) {
	// headers arrive at once, the body (larger than the sniff limit) trickles in for
	// longer than the attempt budget: the read must still complete
	slowBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100000")
		flusher, _ := w.(http.Flusher)
		chunk := bytes.Repeat([]byte("x"), 10000)
		for i := 0; i < 10; i++ {
			_, _ = w.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(60 * time.Millisecond)
		}
	}))
	defer slowBody.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "a", RPCHost: slowBody.URL, Priority: 1}, {Name: "b", RPCHost: slowBody.URL, Priority: 2}}, 5)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, AttemptTimeout: 200 * time.Millisecond}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r, Timeout: 5 * time.Second}
	resp, err := post(c, hc, base(m), "eth_getBlockByNumber")
	c.Assert(err, IsNil)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(err, IsNil)
	c.Assert(body, HasLen, 100000)
	c.Assert(m.Entry("a").Snapshot().InlineErrors, Equals, 0)
}

func (s *RouterSuite) TestBodyBrokenMidStreamIsReported(c *C) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "200000")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 70000)) // more than the sniff limit, then cut
	}))
	defer broken.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "a", RPCHost: broken.URL, Priority: 1}, {Name: "b", RPCHost: broken.URL, Priority: 2}}, 5)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_getBlockByNumber")
	c.Assert(err, IsNil)
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(err, NotNil)
	c.Assert(m.Entry("a").Snapshot().InlineErrors, Equals, 1)
}

func (s *RouterSuite) TestWriteResponseRateLimitCoolsDownWithoutRetry(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	limited := rpcServer(&st, &h1, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"limit"}}`)
	defer limited.Close()
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "lim", RPCHost: limited.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 5)
	hc := routerFor(c, m, targets, []string{"eth_sendRawTransaction"})
	resp, err := post(c, hc, base(m), "eth_sendRawTransaction")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, ".*-32005.*") // the answer is handed back, never retried
	c.Assert(h2, HasLen, 0)
	c.Assert(m.Entry("lim").InCooldown(), Equals, true)
	c.Assert(m.Active().Name, Equals, "good") // handed off for the next broadcast
}
