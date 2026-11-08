package endpoints_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	internal := rpcServer(&stErr, &h1, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"internal error"}}`)
	defer internal.Close()
	limited := rpcServer(&stErr, &h3, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"limit"}}`)
	defer limited.Close()
	redirect := rpcServer(&stRedirect, nil, "")
	defer redirect.Close()
	good := rpcServer(&stOK, &h2, `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted"}}`)
	defer good.Close()
	m, err := endpoints.NewManager(common.ETHChain, []config.ChainEndpointConfig{
		{Name: "internal", RPCHost: internal.URL, Priority: 1},
		{Name: "lim", RPCHost: limited.URL, Priority: 2},
		{Name: "redir", RPCHost: redirect.URL, Priority: 3},
		{Name: "good", RPCHost: good.URL, Priority: 4},
	}, fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	targets, err := endpoints.TargetsFrom([]config.ChainEndpointConfig{
		{Name: "internal", RPCHost: internal.URL}, {Name: "lim", RPCHost: limited.URL}, {Name: "redir", RPCHost: redirect.URL}, {Name: "good", RPCHost: good.URL},
	}, endpoints.RPCHost)
	c.Assert(err, IsNil)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_call")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, ".*execution reverted.*") // application error passed through intact
	c.Assert(m.Entry("internal").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("lim").InCooldown(), Equals, true)
	c.Assert(m.Entry("redir").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("good").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "good")
}

// A node that lacks an optional method answers -32601. Bifrost falls back itself
// (eth_getBlockReceipts to per-transaction receipts), so the answer is the caller's
// business: it is returned as-is and never charged, however often it happens.
func (s *RouterSuite) TestMethodNotFoundIsNotChargedToTheEndpoint(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	old := rpcServer(&st, &h1, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"the method eth_getBlockReceipts does not exist/is not available"}}`)
	defer old.Close()
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "old", RPCHost: old.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	hc := routerFor(c, m, targets, nil)
	for i := 0; i < 6; i++ {
		resp, err := post(c, hc, base(m), "eth_getBlockReceipts")
		c.Assert(err, IsNil)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		c.Assert(string(body), Matches, ".*does not exist.*")
	}
	c.Assert(h1, HasLen, 6)
	c.Assert(h2, HasLen, 0)
	c.Assert(m.Entry("old").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "old")
}

// A write is never cut short by the per-attempt timer: a broadcast that the node
// takes longer than attempt_timeout to answer still completes within the caller's
// deadline, instead of becoming an ambiguous error.
func (s *RouterSuite) TestWriteKeepsTheCallerWholeBudget(c *C) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xtxhash"}`))
	}))
	defer slow.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "slow", RPCHost: slow.URL, Priority: 1}}, 1)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{
		Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, WriteMethods: []string{"eth_sendRawTransaction"}, AttemptTimeout: 200 * time.Millisecond,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r, Timeout: 5 * time.Second}

	// the write waits for the node
	resp, err := post(c, hc, base(m), "eth_sendRawTransaction")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*0xtxhash.*`)
	c.Assert(m.Entry("slow").State(), Equals, endpoints.StateHealthy)

	// a read on the only endpoint keeps the caller's budget too: there is no other
	// candidate to save time for, so a single-endpoint config keeps its request
	// timeout instead of being cut at attempt_timeout
	resp, err = post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(m.Entry("slow").State(), Equals, endpoints.StateHealthy)

	// with a backup behind it, the same slow read is cut at attempt_timeout and the
	// backup answers
	var st int32 = http.StatusOK
	var hb []hit
	backup := rpcServer(&st, &hb, "")
	defer backup.Close()
	m2, targets2 := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "slow", RPCHost: slow.URL, Priority: 1}, {Name: "backup", RPCHost: backup.URL, Priority: 2}}, 5)
	r2, err := endpoints.NewHTTPRouter(m2, endpoints.HTTPRouterConfig{
		Base: endpoints.SyntheticBase(m2.Chain()), Targets: targets2, AttemptTimeout: 200 * time.Millisecond,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	resp, err = post(c, &http.Client{Transport: r2, Timeout: 5 * time.Second}, base(m2), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(hb, HasLen, 1)
}

// When the caller's deadline runs out waiting for an endpoint, that endpoint failed
// and is charged; when the caller cancels, it is not.
func (s *RouterSuite) TestCallerDeadlineIsChargedButCancelIsNot(c *C) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // lets the server notice when the client goes away
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release) // runs first: frees any handler before Close waits for it

	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "hang", RPCHost: hang.URL, Priority: 1}}, 1)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = post(c, &http.Client{Transport: r, Timeout: 300 * time.Millisecond}, base(m), "eth_blockNumber")
	c.Assert(err, NotNil)
	c.Assert(m.Entry("hang").State(), Equals, endpoints.StateUnhealthy)

	m2, targets2 := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "hang", RPCHost: hang.URL, Priority: 1}}, 1)
	r2, err := endpoints.NewHTTPRouter(m2, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m2.Chain()), Targets: targets2}, zerolog.Nop())
	c.Assert(err, IsNil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base(m2), bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)))
	c.Assert(err, IsNil)
	_, err = (&http.Client{Transport: r2, Timeout: 5 * time.Second}).Do(req)
	c.Assert(err, NotNil)
	c.Assert(m2.Entry("hang").State(), Equals, endpoints.StateHealthy)
}

// A 200 whose body then stalls is a dead endpoint too: the classification sniff is
// inside the per-attempt budget, so the read moves to the backup instead of
// consuming the caller's whole deadline.
func (s *RouterSuite) TestStalledSuccessBodyIsBoundedByTheAttemptBudget(c *C) {
	release := make(chan struct{})
	defer close(release)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer stalled.Close()
	var st int32 = http.StatusOK
	var h2 []hit
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "stalled", RPCHost: stalled.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{
		Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, AttemptTimeout: 300 * time.Millisecond,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r, Timeout: 5 * time.Second}

	start := time.Now()
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"ok".*`)
	c.Assert(h2, HasLen, 1)
	c.Assert(time.Since(start) < 2*time.Second, Equals, true, Commentf("took %s", time.Since(start)))
	c.Assert(m.Entry("stalled").State(), Equals, endpoints.StateUnhealthy)
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

func (s *RouterSuite) TestTruncatedBodyIsChargedOnce(c *C) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "200")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"re`)) // cut before the sniff limit
	}))
	defer broken.Close()
	var st int32 = http.StatusOK
	good := rpcServer(&st, nil, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "broken", RPCHost: broken.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 3)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	// one physical failure, one charge: the body wrapper reports it and RoundTrip
	// does not record the same failure again
	c.Assert(m.Entry("broken").Snapshot().InlineErrors, Equals, 1)
}

func (s *RouterSuite) TestRepeatedTruncatedBodiesEvictAndFailOver(c *C) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "200000")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 70000)) // past the sniff limit, then cut
	}))
	defer broken.Close()
	var st int32 = http.StatusOK
	var hg []hit
	good := rpcServer(&st, &hg, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "broken", RPCHost: broken.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 3)
	hc := routerFor(c, m, targets, nil)
	for i := 0; i < 3; i++ {
		resp, err := post(c, hc, base(m), "eth_getBlockByNumber")
		c.Assert(err, IsNil)
		_, _ = io.ReadAll(resp.Body) // fails mid-stream every time
		_ = resp.Body.Close()
	}
	// a header-time success used to reset the run each round, so this never evicted
	c.Assert(m.Entry("broken").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Active().Name, Equals, "good")
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(hg, HasLen, 1)
}

func (s *RouterSuite) TestSuccessIsRecordedWhenTheBodyCompletes(c *C) {
	var st int32 = http.StatusOK
	srv := rpcServer(&st, nil, "")
	defer srv.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "a", RPCHost: srv.URL, Priority: 1}, {Name: "b", RPCHost: srv.URL, Priority: 2}}, 3)
	m.RecordError("a", endpoints.ErrorKindTransport)
	c.Assert(m.Entry("a").Snapshot().InlineErrors, Equals, 1)
	resp, err := post(c, hcFor(c, m, targets), base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(m.Entry("a").Snapshot().InlineErrors, Equals, 0) // reset on clean completion
}

func hcFor(c *C, m *endpoints.Manager, targets []endpoints.Target) *http.Client {
	return routerFor(c, m, targets, nil)
}

// An endpoint that flushes an error status and then stalls the body must not consume
// the caller's remaining deadline: the read moves on to the healthy backup well
// inside the caller's budget instead of draining the stalled body.
func (s *RouterSuite) TestStalledErrorBodyDoesNotStarveTheNextCandidate(c *C) {
	release := make(chan struct{})
	defer close(release)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select { // body never completes until the test ends
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer stalled.Close()
	var st int32 = http.StatusOK
	var h2 []hit
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "stalled", RPCHost: stalled.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	hc := routerFor(c, m, targets, nil)
	hc.Timeout = 5 * time.Second

	start := time.Now()
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"ok".*`)
	c.Assert(h2, HasLen, 1)
	c.Assert(time.Since(start) < 2*time.Second, Equals, true, Commentf("took %s", time.Since(start)))
	c.Assert(m.Entry("stalled").State(), Equals, endpoints.StateUnhealthy)
}

// A complete 200 body that is not valid JSON is a broken answer: the read is retried
// on the backup and the endpoint is charged, exactly like a cut connection. Valid JSON
// of a non-standard shape (TRON's {"Error": "..."}) is still handed to the caller.
func (s *RouterSuite) TestInvalidJSONBodyIsAnEndpointFailure(c *C) {
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := []byte(`{"jsonrpc":"2.0","id":1,"result":`) // complete body, broken JSON
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer garbled.Close()
	var st int32 = http.StatusOK
	var h2 []hit
	good := rpcServer(&st, &h2, "")
	defer good.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "garbled", RPCHost: garbled.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	hc := routerFor(c, m, targets, nil)
	resp, err := post(c, hc, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"ok".*`)
	c.Assert(h2, HasLen, 1)
	c.Assert(m.Entry("garbled").State(), Equals, endpoints.StateUnhealthy)

	// a write is never retried: the garbled answer is returned to the caller as-is
	m2, targets2 := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "garbled", RPCHost: garbled.URL, Priority: 1}, {Name: "good", RPCHost: good.URL, Priority: 2}}, 1)
	hc2 := routerFor(c, m2, targets2, []string{"eth_sendRawTransaction"})
	resp, err = post(c, hc2, base(m2), "eth_sendRawTransaction")
	c.Assert(err, IsNil)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Equals, `{"jsonrpc":"2.0","id":1,"result":`)
	c.Assert(h2, HasLen, 1)

	// valid but non-standard JSON is the caller's business
	odd := rpcServer(&st, nil, `{"Error":"class org.tron.core.exception.ContractValidateException : balance is not sufficient"}`)
	defer odd.Close()
	m3, targets3 := twoEndpointMgr(c, common.TRONChain, []config.ChainEndpointConfig{{Name: "odd", RPCHost: odd.URL, Priority: 1}}, 1)
	hc3 := routerFor(c, m3, targets3, nil)
	resp, err = post(c, hc3, base(m3), "anything")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(m3.Entry("odd").State(), Equals, endpoints.StateHealthy)
}

// A chain can name JSON-RPC codes that mean the endpoint itself cannot serve. Solana's
// -32005 is "node unhealthy" (behind): it is charged and the read moves on, while on
// other chains the same code is a provider rate limit (cooldown, never charged).
func (s *RouterSuite) TestEndpointErrorCodesOverrideTheRateLimitCode(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	behind := rpcServer(&st, &h1, `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is unhealthy"}}`)
	defer behind.Close()
	ok := rpcServer(&st, &h2, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	defer ok.Close()
	read := func(codes []int) *endpoints.Manager {
		m, targets := twoEndpointMgr(c, common.SOLChain, []config.ChainEndpointConfig{{Name: "behind", RPCHost: behind.URL, Priority: 1}, {Name: "ok", RPCHost: ok.URL, Priority: 2}}, 1)
		r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, EndpointErrorCodes: codes}, zerolog.Nop())
		c.Assert(err, IsNil)
		resp, err := post(c, &http.Client{Transport: r, Timeout: 2 * time.Second}, base(m), "getSlot")
		c.Assert(err, IsNil)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		c.Assert(string(body), Matches, `.*"result":"0x1".*`) // served by the next endpoint
		return m
	}

	m := read([]int{-32005})
	c.Assert(m.Entry("behind").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("behind").InCooldown(), Equals, false)

	m = read(nil)
	c.Assert(m.Entry("behind").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("behind").InCooldown(), Equals, true)
}

// When an earlier candidate's "not here" answer is held back and the last candidate
// then runs out the caller's deadline, the held answer is returned, and the stalled
// endpoint is still charged: the caller's deadline ran out waiting for it.
func (s *RouterSuite) TestHeldAnswerDoesNotHideAStalledEndpoint(c *C) {
	var st int32 = http.StatusOK
	var h1 []hit
	short := rpcServer(&st, &h1, `{"result":{"error":"lgrNotFound","error_code":21,"status":"error"}}`)
	defer short.Close()
	release := make(chan struct{})
	stall := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, _ = io.ReadAll(req.Body)
		<-release
	}))
	defer stall.Close()
	defer close(release) // runs first, so the stalled handler returns before Close waits on it
	m, targets := twoEndpointMgr(c, common.XRPChain, []config.ChainEndpointConfig{{Name: "short", RPCHost: short.URL, Priority: 1}, {Name: "stall", RPCHost: stall.URL, Priority: 2}}, 1)
	notHere := func(body []byte) bool { return bytes.Contains(body, []byte(`"lgrNotFound"`)) }
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, RetryElsewhere: notHere}, zerolog.Nop())
	c.Assert(err, IsNil)

	resp, err := post(c, &http.Client{Transport: r, Timeout: 300 * time.Millisecond}, base(m), "ledger")
	if err == nil {
		_ = resp.Body.Close()
	}
	c.Assert(m.Entry("short").State(), Equals, endpoints.StateHealthy) // a proper answer is never charged
	c.Assert(m.Entry("stall").State(), Equals, endpoints.StateUnhealthy)
}

// A chain-native "I do not have that" answer (XRP lgrNotFound) is a proper answer from
// a healthy endpoint, so it is never charged, but another endpoint may have the data:
// the router asks the next candidate and returns the deferred answer only when none
// does better.
func (s *RouterSuite) TestRetryElsewhereAsksTheNextCandidateWithoutCharging(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	short := rpcServer(&st, &h1, `{"result":{"error":"lgrNotFound","error_code":21,"status":"error"}}`)
	defer short.Close()
	full := rpcServer(&st, &h2, `{"result":{"ledger":{"ledger_index":"100"},"status":"success"}}`)
	defer full.Close()
	m, targets := twoEndpointMgr(c, common.XRPChain, []config.ChainEndpointConfig{{Name: "short", RPCHost: short.URL, Priority: 1}, {Name: "full", RPCHost: full.URL, Priority: 2}}, 1)
	notHere := func(body []byte) bool { return bytes.Contains(body, []byte(`"lgrNotFound"`)) }
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, RetryElsewhere: notHere}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r, Timeout: 2 * time.Second}

	resp, err := post(c, hc, base(m), "ledger")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*"ledger_index":"100".*`)
	c.Assert(h1, HasLen, 1)
	c.Assert(h2, HasLen, 1)
	c.Assert(m.Entry("short").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "short")
	// the held-back answer was released: repeating the read many times must not
	// exhaust anything (each held body is closed and its sniff reader returned)
	for i := 0; i < 50; i++ {
		resp, err = post(c, hc, base(m), "ledger")
		c.Assert(err, IsNil)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}

	// when no candidate has it, the deferred answer is returned so the caller sees the
	// chain's own error, and still nothing is charged
	m2, targets2 := twoEndpointMgr(c, common.XRPChain, []config.ChainEndpointConfig{{Name: "short", RPCHost: short.URL, Priority: 1}}, 1)
	r2, err := endpoints.NewHTTPRouter(m2, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m2.Chain()), Targets: targets2, RetryElsewhere: notHere}, zerolog.Nop())
	c.Assert(err, IsNil)
	resp, err = post(c, &http.Client{Transport: r2, Timeout: 2 * time.Second}, base(m2), "ledger")
	c.Assert(err, IsNil)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*lgrNotFound.*`)
	c.Assert(m2.Entry("short").State(), Equals, endpoints.StateHealthy)
}

// A write is never asked elsewhere, whatever its answer says: a "busy" answer to a
// broadcast goes to the caller, sent once.
func (s *RouterSuite) TestRetryElsewhereNeverResendsAWrite(c *C) {
	var st int32 = http.StatusOK
	var h1, h2 []hit
	busy := rpcServer(&st, &h1, `{"result":{"error":"tooBusy","error_code":9,"status":"error"}}`)
	defer busy.Close()
	other := rpcServer(&st, &h2, `{"result":{"engine_result":"tesSUCCESS","status":"success"}}`)
	defer other.Close()
	m, targets := twoEndpointMgr(c, common.XRPChain, []config.ChainEndpointConfig{{Name: "busy", RPCHost: busy.URL, Priority: 1}, {Name: "other", RPCHost: other.URL, Priority: 2}}, 1)
	notHere := func(body []byte) bool { return bytes.Contains(body, []byte(`"tooBusy"`)) }
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets, WriteMethods: []string{"submit"}, RetryElsewhere: notHere}, zerolog.Nop())
	c.Assert(err, IsNil)
	hc := &http.Client{Transport: r, Timeout: 2 * time.Second}

	resp, err := post(c, hc, base(m), "submit")
	c.Assert(err, IsNil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*tooBusy.*`)
	c.Assert(h1, HasLen, 1)
	c.Assert(h2, HasLen, 0)

	// the same answer to a read is asked elsewhere
	resp, err = post(c, hc, base(m), "ledger")
	c.Assert(err, IsNil)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	c.Assert(string(body), Matches, `.*tesSUCCESS.*`)
	c.Assert(h2, HasLen, 1)
}

// When no candidate is left after an endpoint-level JSON-RPC error (here a single
// endpoint's -32603), the error the caller gets carries the node's code and message,
// not a bare status; the endpoint is charged for it.
func (s *RouterSuite) TestEndpointErrorKeepsTheNodesCodeAndMessage(c *C) {
	var st int32 = http.StatusOK
	var hits []hit
	only := rpcServer(&st, &hits, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"missing trie node"}}`)
	defer only.Close()
	eps := []config.ChainEndpointConfig{{Name: "only", RPCHost: only.URL, Priority: 1}}
	m, err := endpoints.NewManager(common.ETHChain, eps, fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	_, err = post(c, routerFor(c, m, targets, nil), base(m), "eth_call")
	c.Assert(err, ErrorMatches, `.*endpoint only: json-rpc error -32603: missing trie node.*`)
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateUnhealthy)
}

// A caller that cancels after the response headers arrived, while the router is
// still reading the start of the body, is not the endpoint's failure: the cancel is
// returned and nothing is charged. A caller deadline that runs out there still is.
func (s *RouterSuite) TestCancelDuringBodyCheckIsNotCharged(c *C) {
	release := make(chan struct{})
	defer close(release)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer stalled.Close()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)

	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "stalled", RPCHost: stalled.URL, Priority: 1}}, 1)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base(m), bytes.NewReader(body))
	c.Assert(err, IsNil)
	_, err = (&http.Client{Transport: r, Timeout: 5 * time.Second}).Do(req)
	c.Assert(errors.Is(err, context.Canceled), Equals, true, Commentf("%v", err))
	c.Assert(m.Entry("stalled").State(), Equals, endpoints.StateHealthy)

	m2, targets2 := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "stalled", RPCHost: stalled.URL, Priority: 1}}, 1)
	r2, err := endpoints.NewHTTPRouter(m2, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m2.Chain()), Targets: targets2}, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = post(c, &http.Client{Transport: r2, Timeout: 300 * time.Millisecond}, base(m2), "eth_blockNumber")
	c.Assert(err, NotNil)
	c.Assert(m2.Entry("stalled").State(), Equals, endpoints.StateUnhealthy)
}

// An endpoint URL whose path holds an escaped reserved character (a key with %2F) is
// called with that exact path, as the health probe calls it, never the decoded one.
func (s *RouterSuite) TestEscapedEndpointPathIsKept(c *C) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.RequestURI)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer srv.Close()
	m, targets := twoEndpointMgr(c, common.ETHChain, []config.ChainEndpointConfig{{Name: "keyed", RPCHost: srv.URL + "/rpc/KEY%2Fx", Priority: 1}}, 1)
	r, err := endpoints.NewHTTPRouter(m, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(m.Chain()), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	resp, err := post(c, &http.Client{Transport: r, Timeout: 2 * time.Second}, base(m), "eth_blockNumber")
	c.Assert(err, IsNil)
	_ = resp.Body.Close()
	c.Assert(got.Load(), Equals, "/rpc/KEY%2Fx")
}
