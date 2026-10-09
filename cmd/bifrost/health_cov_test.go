package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// chainClientCov is a minimal chain client for the health handlers.
type chainClientCov struct {
	chainclients.ChainClient
	cfg           config.BifrostChainConfiguration
	height        int64
	heightErr     error
	scanner       int64
	scannerErr    error
	healthy       bool
	account       common.Account
	accountErr    error
	lastObserved  string
	lastBroadcast string
	latestErr     error
}

func (c *chainClientCov) GetConfig() config.BifrostChainConfiguration { return c.cfg }
func (c *chainClientCov) GetHeight() (int64, error)                   { return c.height, c.heightErr }
func (c *chainClientCov) GetBlockScannerHeight() (int64, error)       { return c.scanner, c.scannerErr }
func (c *chainClientCov) IsBlockScannerHealthy() bool                 { return c.healthy }

func (c *chainClientCov) GetAccount(_ common.PubKey, _ *big.Int) (common.Account, error) {
	return c.account, c.accountErr
}

func (c *chainClientCov) GetLatestTxForVault(_ string) (string, string, error) {
	return c.lastObserved, c.lastBroadcast, c.latestErr
}

// tssPeersCov returns a fixed set of known peers.
type tssPeersCov struct {
	MockTssServer
	peers []tss.PeerInfo
}

func (t *tssPeersCov) GetKnownPeers() []tss.PeerInfo { return t.peers }

// setChainHostCov points the global bifrost config at the given host.
func setChainHostCov(t *testing.T, host string) {
	t.Helper()
	t.Setenv("CHAIN_API", host)
	config.Init()
	require.Equal(t, host, config.GetBifrost().Thorchain.ChainHost)
}

// thornodeCov serves the THORNode endpoints used by the health server.
func thornodeCov(t *testing.T, nodes []openapi.Node, vaults []openapi.Vault, height string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/thorchain/nodes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("grpc-metadata-x-cosmos-block-height", height)
		require.NoError(t, json.NewEncoder(w).Encode(nodes))
	})
	mux.HandleFunc(thorclient.AsgardVault, func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(vaults))
	})
	mux.HandleFunc("/thorchain/lastblock", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("grpc-metadata-x-cosmos-block-height", height)
		_, _ = w.Write([]byte("[]"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

// closedHostCov returns a host:port nothing listens on.
func closedHostCov(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func TestNewHealthServerProvidersCov(t *testing.T) {
	chains := map[common.Chain]chainclients.ChainClient{
		common.BTCChain: &chainClientCov{cfg: config.BifrostChainConfiguration{
			RPCHost: "http://127.0.0.1:8332",
			APIHost: "https://mainnet.infura.io/v3/key",
		}},
		common.GAIAChain: &chainClientCov{cfg: config.BifrostChainConfiguration{
			ChainHost:      "gaia-daemon:1317",
			CosmosGRPCHost: "grpc.cosmos.network:443",
		}},
	}
	s := NewHealthServer("127.0.0.1:0", &MockTssServer{}, chains)

	var got ProviderResponse
	require.NoError(t, json.Unmarshal(s.providerPayload, &got))
	require.Equal(t, ProviderResponse{
		"BTC":  {RPCHost: "self-hosted", APIHost: "infura.io"},
		"GAIA": {ChainHost: "self-hosted", GRPCHost: "cosmos.network"},
	}, got)

	// served through the router
	res := httptest.NewRecorder()
	s.s.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/status/provider", nil))
	require.Equal(t, http.StatusOK, res.Code)
	require.JSONEq(t, string(s.providerPayload), res.Body.String())
}

func TestP2PStatusCov(t *testing.T) {
	nodes := []openapi.Node{
		{NodeAddress: "thor1a", Status: types.NodeStatus_Active.String(), IpAddress: "127.0.0.1", PeerId: "peer-a"},
		// same IP, also active: reported as reuse
		{NodeAddress: "thor1b", Status: types.NodeStatus_Active.String(), IpAddress: "127.0.0.1", PeerId: "peer-b"},
		// standby node then a ready node on the same IP: the ready one replaces it
		{NodeAddress: "thor1c", Status: types.NodeStatus_Standby.String(), IpAddress: "10.0.0.2"},
		{
			NodeAddress: "thor1d", Status: types.NodeStatus_Standby.String(), IpAddress: "10.0.0.2",
			PreflightStatus: openapi.NodePreflightStatus{Status: types.NodeStatus_Ready.String()},
		},
		// standby again on a ready IP: ignored
		{NodeAddress: "thor1e", Status: types.NodeStatus_Standby.String(), IpAddress: "10.0.0.2"},
	}
	setChainHostCov(t, thornodeCov(t, nodes, nil, "1234"))

	tssSrv := &tssPeersCov{peers: []tss.PeerInfo{
		{ID: "stored-a", Address: "127.0.0.1", Direction: "outbound"},
		{ID: "no-addr", Address: "", Direction: "inbound"},
	}}
	s := NewHealthServer("127.0.0.1:0", tssSrv, nil)
	res := httptest.NewRecorder()
	s.s.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/status/p2p", nil))
	require.Equal(t, http.StatusOK, res.Code)

	var got P2PStatusResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Equal(t, int64(1234), got.ThornodeHeight)
	require.Equal(t, 2, got.PeerCount)
	require.Equal(t, []string{"active node IP reuse: 127.0.0.1"}, got.Errors)
	require.Len(t, got.Peers, 2)
	byID := map[string]P2PStatusPeer{}
	for _, p := range got.Peers {
		byID[p.StoredPeerID] = p
	}
	a := byID["stored-a"]
	require.Equal(t, "thor1a", a.Address)
	require.Equal(t, types.NodeStatus_Active.String(), a.Status)
	require.Equal(t, "peer-a", a.NodesPeerID)
	require.Equal(t, "outbound", a.Direction)
	empty := byID["no-addr"]
	require.Equal(t, "", empty.Address)
	require.False(t, empty.P2PPortOpen)
	require.Equal(t, "inbound", empty.Direction)
}

func TestP2PStatusThornodeErrorsCov(t *testing.T) {
	// thornode unreachable
	setChainHostCov(t, closedHostCov(t))
	s := NewHealthServer("127.0.0.1:0", &tssPeersCov{}, nil)
	res := httptest.NewRecorder()
	s.p2pStatus(res, httptest.NewRequest(http.MethodGet, "/status/p2p", nil))
	var got P2PStatusResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Equal(t, int64(0), got.ThornodeHeight)
	require.Empty(t, got.Peers)

	// bad height header and bad body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("grpc-metadata-x-cosmos-block-height", "nan")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	setChainHostCov(t, u.Host)
	res = httptest.NewRecorder()
	s.p2pStatus(res, httptest.NewRequest(http.MethodGet, "/status/p2p", nil))
	require.Equal(t, http.StatusOK, res.Code)
	got = P2PStatusResponse{}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Equal(t, int64(0), got.ThornodeHeight)
	require.Empty(t, got.Errors)
}

func TestCurrentSigningCov(t *testing.T) {
	pk := types.GetRandomPubKey().String()
	vaults := []openapi.Vault{
		{PubKey: &pk, Status: types.VaultStatus_ActiveVault.String(), Chains: []string{"btc", "eth", "doge", "unknown"}},
	}
	setChainHostCov(t, thornodeCov(t, nil, vaults, "1"))

	chains := map[common.Chain]chainclients.ChainClient{
		common.BTCChain: &chainClientCov{
			account:      common.Account{Sequence: 7},
			lastObserved: "obs-tx", lastBroadcast: "bcast-tx",
		},
		// account lookup fails: chain skipped
		common.ETHChain: &chainClientCov{accountErr: errors.New("boom")},
		// latest tx lookup fails: still reported
		common.DOGEChain: &chainClientCov{account: common.Account{Sequence: 3}, latestErr: errors.New("nope")},
	}
	s := NewHealthServer("127.0.0.1:0", &MockTssServer{}, chains)
	res := httptest.NewRecorder()
	s.s.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/status/signing", nil))
	require.Equal(t, http.StatusOK, res.Code)

	var got []VaultResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Len(t, got, 1)
	require.Equal(t, common.PubKey(pk), got[0].Pubkey)
	require.Equal(t, types.VaultStatus_ActiveVault, got[0].Status)
	require.Equal(t, []signingChain{
		{Chain: "btc", LatestBroadcastedTx: "bcast-tx", LatestObservedTx: "obs-tx", CurrentSequence: 7},
		{Chain: "doge", CurrentSequence: 3},
	}, got[0].ChainDetails)

	// unreachable thornode yields an empty list
	setChainHostCov(t, closedHostCov(t))
	res = httptest.NewRecorder()
	s.currentSigning(res, httptest.NewRequest(http.MethodGet, "/status/signing", nil))
	require.Equal(t, http.StatusOK, res.Code)
	require.JSONEq(t, "[]", res.Body.String())
}

func TestChainScannerCov(t *testing.T) {
	setChainHostCov(t, thornodeCov(t, nil, nil, "555"))
	chains := map[common.Chain]chainclients.ChainClient{
		common.BTCChain:  &chainClientCov{height: 100, scanner: 90, healthy: true},
		common.ETHChain:  &chainClientCov{heightErr: errors.New("x"), scanner: 5},
		common.DOGEChain: &chainClientCov{height: 10, scannerErr: errors.New("y")},
	}
	s := NewHealthServer("127.0.0.1:0", &MockTssServer{}, chains)
	res := httptest.NewRecorder()
	s.s.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/status/scanner", nil))
	require.Equal(t, http.StatusOK, res.Code)

	var got map[string]ScannerResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Len(t, got, 4)

	btc := got["BTC"]
	require.Equal(t, int64(100), btc.ChainHeight)
	require.Equal(t, int64(90), btc.BlockScannerHeight)
	require.Equal(t, int64(10), btc.ScannerHeightDiff)
	require.NotNil(t, btc.Healthy)
	require.True(t, *btc.Healthy)

	eth := got["ETH"]
	require.Equal(t, int64(-1), eth.ChainHeight)
	require.Equal(t, int64(-1), eth.ScannerHeightDiff)
	require.False(t, *eth.Healthy)

	doge := got["DOGE"]
	require.Equal(t, int64(-1), doge.BlockScannerHeight)
	require.Equal(t, int64(-1), doge.ScannerHeightDiff)

	thor := got["THOR"]
	require.Equal(t, int64(555), thor.ChainHeight)
	require.Equal(t, int64(-1), thor.BlockScannerHeight)
	require.Nil(t, thor.Healthy)

	// thornode unreachable: no THOR entry
	setChainHostCov(t, closedHostCov(t))
	res = httptest.NewRecorder()
	s.chainScanner(res, httptest.NewRequest(http.MethodGet, "/status/scanner", nil))
	got = nil
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &got))
	require.Len(t, got, 3)
	require.NotContains(t, got, "THOR")
}

func TestCheckPortOpenCov(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.True(t, checkPortOpen("127.0.0.1", tcpAddr.Port))

	closed := closedHostCov(t)
	_, portStr, err := net.SplitHostPort(closed)
	require.NoError(t, err)
	p, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	require.False(t, checkPortOpen("127.0.0.1", p))
}

func TestHealthServerStartStopCov(t *testing.T) {
	// nil server instance
	require.Error(t, (&HealthServer{}).Start())

	// invalid address fails to listen
	s := NewHealthServer("bad-address-no-port", &MockTssServer{}, nil)
	require.ErrorContains(t, s.Start(), "fail to start http server")
}

// failingWriterCov is a ResponseWriter whose writes always fail.
type failingWriterCov struct {
	header http.Header
	code   int
}

func (f *failingWriterCov) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failingWriterCov) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (f *failingWriterCov) WriteHeader(code int)      { f.code = code }

func TestHandlersWriteErrorsCov(t *testing.T) {
	setChainHostCov(t, closedHostCov(t))
	s := NewHealthServer("127.0.0.1:0", &tssPeersCov{}, nil)
	s.providerPayload = []byte("{}")
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// write failures are logged and do not change the status code
	for name, h := range map[string]http.HandlerFunc{
		"version":  s.versionHandler,
		"p2pid":    s.getP2pIDHandler,
		"p2p":      s.p2pStatus,
		"signing":  s.currentSigning,
		"scanner":  s.chainScanner,
		"provider": s.providerStatus,
	} {
		w := &failingWriterCov{}
		h(w, req)
		require.Equal(t, 0, w.code, name)
	}
}

func TestClassifyHostEdgeCasesCov(t *testing.T) {
	// unparsable URL
	require.Equal(t, "self-hosted", classifyHost("http://[::1"))
	// URL without a host
	require.Equal(t, "self-hosted", classifyHost("http://:8080"))
}
