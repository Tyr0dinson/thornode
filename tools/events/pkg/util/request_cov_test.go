package util

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

// fakeTransportCov serves canned responses keyed by URL path (ignoring query) and
// records every request it receives.
type fakeTransportCov struct {
	mu       sync.Mutex
	routes   map[string]fakeRouteCov
	requests []*http.Request
}

type fakeRouteCov struct {
	status int
	body   string
}

func (f *fakeTransportCov) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	route, ok := f.routes[req.URL.Host+req.URL.Path]
	if !ok {
		route = fakeRouteCov{status: http.StatusNotFound, body: "not found"}
	}
	return &http.Response{
		StatusCode: route.status,
		Body:       io.NopCloser(bytes.NewBufferString(route.body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (f *fakeTransportCov) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.URL.Path == path {
			n++
		}
	}
	return n
}

type UtilCovSuite struct {
	origConfig    config.Config
	origTransport http.RoundTripper
	fake          *fakeTransportCov
}

var _ = Suite(&UtilCovSuite{})

const covThornodeHost = "thornode.cov.test"

func (s *UtilCovSuite) SetUpTest(c *C) {
	s.origConfig = config.Get()
	s.origTransport = http.DefaultClient.Transport

	cfg := config.Get()
	cfg.MaxRetries = 0
	cfg.Endpoints.Thornode = "http://" + covThornodeHost
	config.Set(cfg)

	s.fake = &fakeTransportCov{routes: map[string]fakeRouteCov{}}
	http.DefaultClient.Transport = s.fake
	InitCache()
}

func (s *UtilCovSuite) TearDownTest(c *C) {
	config.Set(s.origConfig)
	http.DefaultClient.Transport = s.origTransport
}

func (s *UtilCovSuite) route(path string, status int, body any) {
	var str string
	switch b := body.(type) {
	case string:
		str = b
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		str = string(raw)
	}
	s.fake.routes[covThornodeHost+"/"+path] = fakeRouteCov{status: status, body: str}
}

func (s *UtilCovSuite) TestThornodeCachedRetryGetCaches(c *C) {
	s.route("thorchain/thing", http.StatusOK, map[string]string{"a": "b"})

	var out map[string]string
	c.Assert(ThornodeCachedRetryGet("thorchain/thing", 7, &out), IsNil)
	c.Assert(out["a"], Equals, "b")
	c.Assert(s.fake.count("/thorchain/thing"), Equals, 1)

	// the request self-identifies and carries the height
	req := s.fake.requests[0]
	c.Assert(req.Header.Get("X-Client-ID"), Equals, "events")
	c.Assert(req.URL.Query().Get("height"), Equals, "7")

	// second call at the same height is served from cache
	out = nil
	c.Assert(ThornodeCachedRetryGet("thorchain/thing", 7, &out), IsNil)
	c.Assert(out["a"], Equals, "b")
	c.Assert(s.fake.count("/thorchain/thing"), Equals, 1)

	// a different height is a different cache key
	c.Assert(ThornodeCachedRetryGet("thorchain/thing", 8, &out), IsNil)
	c.Assert(s.fake.count("/thorchain/thing"), Equals, 2)
}

func (s *UtilCovSuite) TestThornodeCachedRetryGetEmptyCacheRefetches(c *C) {
	s.route("thorchain/empty", http.StatusOK, `{"x":1}`)
	u := "http://" + covThornodeHost + "/thorchain/empty?height=3"
	cache.Add(u, []byte{})

	var out map[string]int
	c.Assert(ThornodeCachedRetryGet("thorchain/empty", 3, &out), IsNil)
	c.Assert(out["x"], Equals, 1)
	c.Assert(s.fake.count("/thorchain/empty"), Equals, 1)
}

func (s *UtilCovSuite) TestThornodeCachedRetryGetErrors(c *C) {
	// non-200 status
	s.route("thorchain/bad", http.StatusInternalServerError, "boom")
	var out map[string]string
	err := ThornodeCachedRetryGet("thorchain/bad", 1, &out)
	c.Assert(err, ErrorMatches, "status code 500")

	// invalid json body
	s.route("thorchain/notjson", http.StatusOK, "nope")
	err = ThornodeCachedRetryGet("thorchain/notjson", 1, &out)
	c.Assert(err, NotNil)

	// invalid url
	cfg := config.Get()
	cfg.Endpoints.Thornode = "http://bad host\x7f"
	config.Set(cfg)
	err = ThornodeCachedRetryGet("x", 1, &out)
	c.Assert(err, NotNil)
}

func (s *UtilCovSuite) TestRetryGetErrors(c *C) {
	s.fake.routes["ext.cov.test/bad"] = fakeRouteCov{status: http.StatusTeapot, body: ""}
	var out map[string]string
	err := RetryGet("http://ext.cov.test/bad", &out)
	c.Assert(err, ErrorMatches, ".*status code 418")

	// transport-level error (unsupported scheme)
	http.DefaultClient.Transport = s.origTransport
	err = RetryGet("bogus://nowhere", &out)
	c.Assert(err, NotNil)
}

func (s *UtilCovSuite) TestInitCachePanicsOnBadSize(c *C) {
	cfg := config.Get()
	cfg.Endpoints.CacheSize = 0
	config.Set(cfg)
	c.Assert(InitCache, PanicMatches, ".*failed to initialize cache.*")
}

func (s *UtilCovSuite) TestRuneValue(c *C) {
	// rune is returned directly in whole units
	v := RuneValue(1, common.NewCoin(common.RuneNative, cosmos.NewUint(250000000)))
	c.Assert(v, Equals, 2.5)

	// TOR uses the network price
	s.route("thorchain/network", http.StatusOK, openapi.NetworkResponse{TorPriceInRune: "50000000"})
	v = RuneValue(1, common.NewCoin(common.TOR, cosmos.NewUint(400000000)))
	c.Assert(v, Equals, 2.0)

	// pool assets use pool depths
	s.route("thorchain/pools", http.StatusOK, []openapi.Pool{
		{Asset: "ETH.ETH", BalanceRune: "1000", BalanceAsset: "1000"},
		{Asset: "BTC.BTC", BalanceRune: "2000", BalanceAsset: "1000"},
	})
	v = RuneValue(1, common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000)))
	c.Assert(v, Equals, 2.0)

	// synth resolves to the layer1 pool
	v = RuneValue(1, common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(300000000)))
	c.Assert(v, Equals, 6.0)

	// derived asset maps its symbol to the pool chain
	derived, err := common.NewAsset("THOR.BTC")
	c.Assert(err, IsNil)
	v = RuneValue(1, common.NewCoin(derived, cosmos.NewUint(100000000)))
	c.Assert(v, Equals, 2.0)

	// unknown pool returns zero
	v = RuneValue(1, common.NewCoin(common.DOGEAsset, cosmos.NewUint(100000000)))
	c.Assert(v, Equals, 0.0)
}

func (s *UtilCovSuite) TestRuneValuePanics(c *C) {
	// network fetch fails
	c.Assert(func() { RuneValue(2, common.NewCoin(common.TOR, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to get network.*")

	// bad network price
	s.route("thorchain/network", http.StatusOK, openapi.NetworkResponse{TorPriceInRune: "abc"})
	c.Assert(func() { RuneValue(3, common.NewCoin(common.TOR, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to parse network rune price.*")

	// pools fetch fails
	c.Assert(func() { RuneValue(4, common.NewCoin(common.BTCAsset, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to get pools.*")
}

func (s *UtilCovSuite) TestUSDValue(c *C) {
	// TOR is 1:1 with USD
	c.Assert(USDValue(1, common.NewCoin(common.TOR, cosmos.NewUint(300000000))), Equals, 3.0)

	// rune uses network price
	s.route("thorchain/network", http.StatusOK, openapi.NetworkResponse{RunePriceInTor: "200000000"})
	c.Assert(USDValue(1, common.NewCoin(common.RuneNative, cosmos.NewUint(150000000))), Equals, 3.0)

	s.route("thorchain/pools", http.StatusOK, []openapi.Pool{
		{Asset: "ETH.ETH", AssetTorPrice: ""},
		{Asset: "BTC.BTC", AssetTorPrice: "5000000000000"},
	})
	c.Assert(USDValue(1, common.NewCoin(common.BTCAsset, cosmos.NewUint(200000000))), Equals, 100000.0)
	c.Assert(USDValueString(1, common.NewCoin(common.BTCAsset, cosmos.NewUint(200000000))), Equals, "$100,000.00")

	// derived asset
	derived, err := common.NewAsset("THOR.BTC")
	c.Assert(err, IsNil)
	c.Assert(USDValue(1, common.NewCoin(derived, cosmos.NewUint(100000000))), Equals, 50000.0)

	// pool without price is zero
	c.Assert(USDValue(1, common.NewCoin(common.ETHAsset, cosmos.NewUint(100000000))), Equals, 0.0)

	// unknown pool is zero
	c.Assert(USDValue(1, common.NewCoin(common.DOGEAsset, cosmos.NewUint(100000000))), Equals, 0.0)
}

func (s *UtilCovSuite) TestUSDValuePanics(c *C) {
	c.Assert(func() { USDValue(2, common.NewCoin(common.RuneNative, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to get network.*")

	s.route("thorchain/network", http.StatusOK, openapi.NetworkResponse{RunePriceInTor: "x"})
	c.Assert(func() { USDValue(3, common.NewCoin(common.RuneNative, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to parse network rune price.*")

	c.Assert(func() { USDValue(4, common.NewCoin(common.BTCAsset, cosmos.NewUint(1))) },
		PanicMatches, ".*failed to get pools.*")
}

func (s *UtilCovSuite) TestExternalUSDValue(c *C) {
	s.fake.routes["min-api.cryptocompare.com/data/price"] = fakeRouteCov{
		status: http.StatusOK, body: `{"USD": 2.5}`,
	}
	c.Assert(ExternalUSDValue(common.NewCoin(common.BTCAsset, cosmos.NewUint(200000000))), Equals, 5.0)
	c.Assert(ExternalUSDValue(common.NewCoin(common.ETHAsset, cosmos.NewUint(400000000))), Equals, 10.0)

	// the fsym parameter matches the asset
	var syms []string
	for _, r := range s.fake.requests {
		q, err := url.ParseQuery(r.URL.RawQuery)
		c.Assert(err, IsNil)
		syms = append(syms, q.Get("fsym")+"/"+q.Get("tsyms"))
	}
	c.Assert(strings.Join(syms, ","), Equals, "BTC/USD,ETH/USD")

	// unsupported asset
	c.Assert(ExternalUSDValue(common.NewCoin(common.DOGEAsset, cosmos.NewUint(1))), Equals, 0.0)

	// request failure yields zero
	s.fake.routes["min-api.cryptocompare.com/data/price"] = fakeRouteCov{status: http.StatusBadGateway}
	c.Assert(ExternalUSDValue(common.NewCoin(common.BTCAsset, cosmos.NewUint(1))), Equals, 0.0)
}

func (s *UtilCovSuite) TestClout(c *C) {
	addr := "thor1clout"
	score := "12345"
	s.route("thorchain/clout/swap/"+addr, http.StatusOK, openapi.SwapperCloutResponse{Score: &score})
	clout := Clout(1, addr)
	c.Assert(clout.Asset, Equals, common.RuneNative)
	c.Assert(clout.Amount.Uint64(), Equals, uint64(12345))

	// missing score / failure yields zero clout
	clout = Clout(1, "thor1missing")
	c.Assert(clout.Amount.IsZero(), Equals, true)
}

func (s *UtilCovSuite) TestGetConfigInt64(c *C) {
	s.route("thorchain/mimir", http.StatusOK, map[string]int64{"CHURNINTERVAL": 77})
	c.Assert(GetConfigInt64(1, constants.ChurnInterval), Equals, int64(77))
	c.Assert(GetConfigInt64(1, constants.MinRunePoolDepth), Equals,
		constants.NewConstantValue().GetInt64Value(constants.MinRunePoolDepth))

	delete(s.fake.routes, covThornodeHost+"/thorchain/mimir")
	c.Assert(func() { GetConfigInt64(2, constants.ChurnInterval) }, PanicMatches, ".*failed to get mimirs.*")
}

func (s *UtilCovSuite) TestOrderedMapString(c *C) {
	om := NewOrderedMap()
	om.Set("a", 1)
	c.Assert(om.String(), Equals, "map[a:1]")
}
