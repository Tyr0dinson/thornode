package thorclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "gopkg.in/check.v1"
)

type FreshQuerySuite struct{}

var _ = Suite(&FreshQuerySuite{})

func newFreshQueryBridge(c *C, server *httptest.Server) *thorchainBridge {
	cfg, _, kb := SetupThorchainForTest(c)
	cfg.ChainHost = strings.TrimPrefix(server.URL, "http://")
	cfg.ChainRPC = cfg.ChainHost

	bridge, err := NewThorchainBridge(cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	c.Assert(err, IsNil)
	concrete, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	concrete.httpClient.RetryMax = 0
	return concrete
}

func (s *FreshQuerySuite) TestFreshVoterBypassesButDoesNotReplaceCache(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requests++
		reverted := requests > 1
		_, err := fmt.Fprintf(rw, `{"tx_id":"%s","reverted":%t}`, strings.Repeat("A", 64), reverted)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	txID := strings.Repeat("A", 64)

	cached, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(cached.Reverted, Equals, false)

	fresh, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(fresh, NotNil)
	c.Assert(fresh.Reverted, Equals, true)

	stillCached, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(stillCached.Reverted, Equals, false)
	c.Assert(requests, Equals, 2)
}

func (s *FreshQuerySuite) TestOutboundVoterUsesSeparateCacheAndFreshLookup(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		height := 11
		if req.URL.Query().Get("outbound") == "true" {
			height = requests * 11
		}
		_, err := fmt.Fprintf(w, `{"tx_id":"%s","consensus_height":%d}`, strings.Repeat("A", 64), height)
		c.Assert(err, IsNil)
	}))
	defer server.Close()
	bridge := newFreshQueryBridge(c, server)
	txID := strings.Repeat("A", 64)
	inbound, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Height, Equals, int64(11))
	outbound, err := bridge.GetObservedTxOutVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(outbound.Height, Equals, int64(22))
	fresh, found, err := bridge.GetObservedTxOutVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(fresh.Height, Equals, int64(33))
	stillCached, err := bridge.GetObservedTxOutVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(stillCached.Height, Equals, int64(22))
	inbound, err = bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Height, Equals, int64(11))
	c.Assert(requests, Equals, 3)
}

func (s *FreshQuerySuite) TestOutboundVoterQueryFailures(c *C) {
	txID := strings.Repeat("B", 64)
	for _, test := range []struct {
		status    int
		body      string
		wantError bool
	}{
		{http.StatusInternalServerError, fmt.Sprintf(`{"message":"tx: %s doesn't exist"}`, txID), false},
		{http.StatusNotFound, "", true},
		{http.StatusServiceUnavailable, "unavailable", true},
		{http.StatusOK, "invalid json", true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.Assert(req.URL.Query().Get("outbound"), Equals, "true")
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		bridge := newFreshQueryBridge(c, server)
		voter, found, err := bridge.GetObservedTxOutVoterUncached(txID)
		c.Assert(err != nil, Equals, test.wantError)
		c.Assert(found, Equals, false)
		c.Assert(voter, IsNil)
		server.Close()
	}
}

func (s *FreshQuerySuite) TestFreshVoterReportsBareNotFoundAsFailure(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(strings.Repeat("B", 64))
	c.Assert(err, NotNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsTransactionSpecificNotFoundAsAbsent(c *C) {
	txID := strings.Repeat("B", 64)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
		_, err := fmt.Fprintf(rw, `{"message":"tx: %s doesn't exist"}`, txID)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsCosmosMissingQueryAsAbsent(c *C) {
	txID := strings.Repeat("D", 64)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
		_, err := fmt.Fprintf(rw, `{"code":13,"message":"tx: %s doesn't exist"}`, txID)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsServerFailure(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(strings.Repeat("C", 64))
	c.Assert(err, NotNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshBlockHeightBypassesButDoesNotReplaceCache(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requests++
		_, err := fmt.Fprintf(rw, `[{"chain":"THOR","thorchain":%d}]`, 40+requests)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	cached, err := bridge.GetBlockHeight()
	c.Assert(err, IsNil)
	c.Assert(cached, Equals, int64(41))

	fresh, err := bridge.GetBlockHeightUncached()
	c.Assert(err, IsNil)
	c.Assert(fresh, Equals, int64(42))

	stillCached, err := bridge.GetBlockHeight()
	c.Assert(err, IsNil)
	c.Assert(stillCached, Equals, int64(41))
	c.Assert(requests, Equals, 2)
}
