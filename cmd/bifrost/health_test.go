package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/config"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
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

func (mts *MockTssServer) FrostKeySign(req tss.FrostKeySignRequest, sidecar tss.FrostSidecarClient) (tss.FrostKeySignResponse, error) {
	if mts.failToKeySign {
		return tss.FrostKeySignResponse{}, errors.New("you ask for it")
	}
	return tss.FrostKeySignResponse{Status: common.Success}, nil
}

func (mts *MockTssServer) FrostKeygen(req tss.FrostKeygenRequest, sidecar tss.FrostSidecarKeygenClient) (tss.FrostKeygenResponse, error) {
	if mts.failToKeyGen {
		return tss.FrostKeygenResponse{}, errors.New("you ask for it")
	}
	return tss.FrostKeygenResponse{Status: common.Success}, nil
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

// fakeManagedChain is a ChainClient stub that also exposes an endpoint manager.
type fakeManagedChain struct {
	chainclients.ChainClient // nil; only GetConfig / GetChain are called
	cfg                      config.BifrostChainConfiguration
	mgr                      *endpoints.Manager
}

func (f *fakeManagedChain) GetConfig() config.BifrostChainConfiguration { return f.cfg }
func (f *fakeManagedChain) GetChain() tcommon.Chain                     { return f.cfg.ChainID }
func (f *fakeManagedChain) EndpointManager() *endpoints.Manager         { return f.mgr }

func (HealthServerTestSuite) TestProviderStatusHandlerIsLive(c *C) {
	cfg := config.BifrostChainConfiguration{ChainID: tcommon.BTCChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "local", RPCHost: "http://127.0.0.1:8332", Priority: 1},
		{Name: "ankr", RPCHost: "https://rpc.ankr.com/btc/KEY", Priority: 2},
	}}
	mgr, err := endpoints.NewManager(tcommon.BTCChain, cfg.Endpoints, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("genesis")
	}
	chains := map[tcommon.Chain]chainclients.ChainClient{tcommon.BTCChain: &fakeManagedChain{cfg: cfg, mgr: mgr}}
	s := NewHealthServer("127.0.0.1:8080", &MockTssServer{}, chains)

	get := func(path string) map[string]ProviderEntry {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		s.newHandler().ServeHTTP(res, req)
		c.Assert(res.Code, Equals, http.StatusOK)
		body := res.Body.String()
		c.Assert(body, Not(Matches), "(?s).*KEY.*") // never leak the URL
		var out map[string]ProviderEntry
		c.Assert(json.NewDecoder(strings.NewReader(body)).Decode(&out), IsNil)
		return out
	}
	out := get("/status/provider")
	c.Assert(out["BTC"].Providers, HasLen, 2)
	c.Assert(out["BTC"].Providers[0].Name, Equals, "local")
	c.Assert(out["BTC"].Providers[0].State, Equals, "healthy")
	c.Assert(out["BTC"].Providers[0].Active, Equals, true)
	c.Assert(out["BTC"].Providers[1].Host, Equals, "ankr.com")
	c.Assert(out["BTC"].Providers[1].IdentityVerified, Equals, true)

	for i := 0; i < 5; i++ {
		mgr.RecordError("local", endpoints.ErrorKindTransport)
	}
	out = get("/status/provider/BTC")
	c.Assert(out["BTC"].Providers[0].State, Equals, "unhealthy")
	c.Assert(out["BTC"].Providers[0].LastErrorKind, Equals, "transport")
	c.Assert(out["BTC"].Providers[1].Active, Equals, true)

	req := httptest.NewRequest(http.MethodGet, "/status/provider/DOGE", nil)
	res := httptest.NewRecorder()
	s.newHandler().ServeHTTP(res, req)
	c.Assert(res.Code, Equals, http.StatusNotFound)
}
