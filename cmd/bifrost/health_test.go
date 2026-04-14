package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type MockTssServer struct {
	failToStart   bool
	failToKeyGen  bool
	failToKeySign bool
}

func (mts *MockTssServer) Start() error {
	if mts.failToStart {
		return errors.New("you ask for it")
	}
	return nil
}

func (mts *MockTssServer) Stop() {
}

func (mts *MockTssServer) GetLocalPeerID() string {
	return conversion.GetRandomPeerID().String()
}

func (mts *MockTssServer) GetKnownPeers() []tss.PeerInfo {
	return []tss.PeerInfo{}
}

func (mts *MockTssServer) Keygen(req keygen.Request) (keygen.Response, error) {
	if mts.failToKeyGen {
		return keygen.Response{}, errors.New("you ask for it")
	}
	return keygen.NewResponse(tcommon.SigningAlgoSecp256k1, conversion.GetRandomPubKey(), "whatever", common.Success, blame.Blame{}), nil
}

func (mts *MockTssServer) KeygenAllAlgo(req keygen.Request) ([]keygen.Response, error) {
	if mts.failToKeyGen {
		return []keygen.Response{}, errors.New("you ask for it")
	}
	return []keygen.Response{
		keygen.NewResponse(tcommon.SigningAlgoSecp256k1, conversion.GetRandomPubKey(), "whatever", common.Success, blame.Blame{}),
		keygen.NewResponse(tcommon.SigningAlgoEd25519, conversion.GetRandomPubKey(), "whatever", common.Success, blame.Blame{}),
	}, nil
}

func (mts *MockTssServer) KeySign(req keysign.Request) (keysign.Response, error) {
	if mts.failToKeySign {
		return keysign.Response{}, errors.New("you ask for it")
	}
	return keysign.NewResponse(nil, common.Success, blame.Blame{}), nil
}

type HealthServerTestSuite struct{}

var _ = Suite(&HealthServerTestSuite{})

func (HealthServerTestSuite) TestHealthServer(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	c.Assert(s, NotNil)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Start()
		c.Assert(err, IsNil)
	}()
	time.Sleep(time.Second)
	c.Assert(s.Stop(), IsNil)
}

func (HealthServerTestSuite) TestPingHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	c.Assert(s, NotNil)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	res := httptest.NewRecorder()
	s.pingHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (HealthServerTestSuite) TestGetP2pIDHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	c.Assert(s, NotNil)
	req := httptest.NewRequest(http.MethodGet, "/p2pid", nil)
	res := httptest.NewRecorder()
	s.getP2pIDHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (HealthServerTestSuite) TestClassifyHost(c *C) {
	cases := []struct {
		input    string
		expected string
	}{
		// empty
		{"", ""},

		// loopback IPs
		{"127.0.0.1", "self-hosted"},
		{"127.0.0.1:8332", "self-hosted"},
		{"::1", "self-hosted"},

		// private RFC-1918 ranges
		{"10.0.0.1", "self-hosted"},
		{"10.1.2.3:8545", "self-hosted"},
		{"172.16.0.1", "self-hosted"},
		{"172.31.255.255", "self-hosted"},
		{"192.168.1.100", "self-hosted"},
		{"192.168.0.1:9650", "self-hosted"},

		// link-local
		{"169.254.1.1", "self-hosted"},

		// bare hostnames (no dots)
		{"localhost", "self-hosted"},
		{"LOCALHOST", "self-hosted"},
		{"bitcoin-daemon", "self-hosted"},
		{"thornode", "self-hosted"},

		// local TLDs
		{"node.local", "self-hosted"},
		{"btc.internal", "self-hosted"},
		{"eth.lan", "self-hosted"},
		{"host.home", "self-hosted"},
		{"host.localdomain", "self-hosted"},
		{"HOST.LOCAL", "self-hosted"},

		// URL forms with local addresses
		{"http://localhost:8332", "self-hosted"},
		{"http://127.0.0.1:8332/rpc", "self-hosted"},
		{"http://bitcoin-daemon:8332", "self-hosted"},
		{"http://node.internal:8545/", "self-hosted"},

		// public IP (no provider name available)
		{"203.0.113.5", "external-host"},
		{"203.0.113.5:8545", "external-host"},

		// external providers – bare host
		{"mainnet.infura.io", "infura.io"},
		{"eth-mainnet.alchemyapi.io", "alchemyapi.io"},
		{"rpc.ankr.com", "ankr.com"},
		{"api.trongrid.io", "trongrid.io"},

		// external providers – with port
		{"rpc.ankr.com:443", "ankr.com"},

		// external providers – full URLs
		{"https://mainnet.infura.io/v3/mykey", "infura.io"},
		{"https://eth-mainnet.g.alchemy.com/v2/key", "alchemy.com"},
		{"http://rpc.quicknode.pro:8545/", "quicknode.pro"},
	}

	for _, tc := range cases {
		got := classifyHost(tc.input)
		c.Assert(got, Equals, tc.expected, Commentf("input: %q", tc.input))
	}
}

func (HealthServerTestSuite) TestVersionHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	c.Assert(s, NotNil)
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	res := httptest.NewRecorder()
	s.versionHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
	c.Assert(res.Header().Get("Content-Type"), Equals, "application/json")

	var body struct {
		Version   string `json:"version"`
		GitCommit string `json:"git_commit"`
	}
	c.Assert(json.NewDecoder(res.Body).Decode(&body), IsNil)
	c.Assert(body.Version, Equals, constants.Version)
	c.Assert(body.GitCommit, Equals, constants.GitCommit)
}

func (HealthServerTestSuite) TestProviderStatusHandler(c *C) {
	tssServer := &MockTssServer{}

	// handler returns 500 when payload is nil (marshal failure path)
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	s.providerPayload = nil
	req := httptest.NewRequest(http.MethodGet, "/status/provider", nil)
	res := httptest.NewRecorder()
	s.providerStatus(res, req)
	c.Assert(res.Code, Equals, http.StatusInternalServerError)

	// handler returns pre-computed payload when set
	s.providerPayload = []byte(`{"BTC":{"rpc_host":"self-hosted"}}`)
	req = httptest.NewRequest(http.MethodGet, "/status/provider", nil)
	res = httptest.NewRecorder()
	s.providerStatus(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
	c.Assert(res.Body.String(), Equals, `{"BTC":{"rpc_host":"self-hosted"}}`)
}

type mockChainInfoProvider struct {
	observedChains   []tcommon.Chain
	lastChangeHeight int64
}

func (m *mockChainInfoProvider) GetObservedChains() []tcommon.Chain {
	return m.observedChains
}

func (m *mockChainInfoProvider) GetLastObservedChangeHeight() int64 {
	return m.lastChangeHeight
}

func (HealthServerTestSuite) TestChainsStatusHandler_NoProvider(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	req := httptest.NewRequest(http.MethodGet, "/status/chains", nil)
	res := httptest.NewRecorder()
	s.chainsStatus(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)

	var body ChainsStatusResponse
	c.Assert(json.NewDecoder(res.Body).Decode(&body), IsNil)
	c.Assert(body.ObservedChains, IsNil)
	c.Assert(body.ConfiguredChains, IsNil)
	c.Assert(body.LastObservedChangeHeight, Equals, int64(0))
}

func (HealthServerTestSuite) TestChainsStatusHandler_WithProvider(c *C) {
	tssServer := &MockTssServer{}

	chains := map[tcommon.Chain]chainclients.ChainClient{
		tcommon.BTCChain:  nil,
		tcommon.ETHChain:  nil,
		tcommon.DOGEChain: nil,
	}
	s := NewHealthServer("127.0.0.1:8080", tssServer, chains)
	s.SetChainInfoProvider(&mockChainInfoProvider{
		observedChains:   []tcommon.Chain{tcommon.BTCChain, tcommon.ETHChain},
		lastChangeHeight: 12345,
	})

	req := httptest.NewRequest(http.MethodGet, "/status/chains", nil)
	res := httptest.NewRecorder()
	s.chainsStatus(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)

	var body ChainsStatusResponse
	c.Assert(json.NewDecoder(res.Body).Decode(&body), IsNil)
	c.Assert(body.ObservedChains, DeepEquals, []string{"BTC", "ETH"})
	c.Assert(len(body.ConfiguredChains), Equals, 3)
	c.Assert(body.ConfiguredChains[0], Equals, "BTC")
	c.Assert(body.LastObservedChangeHeight, Equals, int64(12345))
}

func (HealthServerTestSuite) TestChainsStatusHandler_ViaRouter(c *C) {
	tssServer := &MockTssServer{}
	s := NewHealthServer("127.0.0.1:8080", tssServer, nil)
	s.SetChainInfoProvider(&mockChainInfoProvider{
		observedChains:   []tcommon.Chain{tcommon.BTCChain},
		lastChangeHeight: 999,
	})

	handler := s.newHandler()
	req := httptest.NewRequest(http.MethodGet, "/status/chains", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)

	var body ChainsStatusResponse
	c.Assert(json.NewDecoder(res.Body).Decode(&body), IsNil)
	c.Assert(body.ObservedChains, DeepEquals, []string{"BTC"})
	c.Assert(body.LastObservedChangeHeight, Equals, int64(999))
}
