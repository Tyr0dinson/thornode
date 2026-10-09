package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func p2pCovCapture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

// p2pCovHostPort strips the scheme from an httptest server URL.
func p2pCovHostPort(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func p2pCovStatusBody(height string) string {
	return fmt.Sprintf(`{"result":{"sync_info":{"latest_block_height":%q}}}`, height)
}

func TestExtractIPAddressCov(t *testing.T) {
	ip, err := extractIPAddress("/ip4/1.2.3.4/tcp/5040")
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", ip)

	for _, bad := range []string{"", "/ip4", "/ip6/::1/tcp/5040", "/dns4/foo/tcp/1"} {
		_, err = extractIPAddress(bad)
		require.EqualError(t, err, "invalid format", bad)
	}
}

func TestGetEnvOrDefaultCov(t *testing.T) {
	const key = "P2P_CHECK_COV_TEST_KEY"
	require.Equal(t, "fallback", getEnvOrDefault(key, "fallback"))
	t.Setenv(key, "")
	require.Equal(t, "", getEnvOrDefault(key, "fallback"), "set-but-empty wins")
	t.Setenv(key, "value")
	require.Equal(t, "value", getEnvOrDefault(key, "fallback"))
}

func TestFetchNodeCov(t *testing.T) {
	nodes := []openapi.Node{
		{PeerId: "a", NodeAddress: "thor1a"},
		{PeerId: "b", NodeAddress: "thor1b"},
	}
	require.Equal(t, "thor1b", fetchNode("b", nodes).NodeAddress)
	require.Equal(t, openapi.Node{}, fetchNode("missing", nodes))
	require.Equal(t, openapi.Node{}, fetchNode("a", nil))
}

func TestCheckPortOpenCov(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port
	require.True(t, checkPortOpen("127.0.0.1", port))

	require.NoError(t, ln.Close())
	require.False(t, checkPortOpen("127.0.0.1", port))
}

func TestFetchBlockHeightCov(t *testing.T) {
	var retries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/status":
			_, _ = io.WriteString(w, p2pCovStatusBody("12345"))
		case "/retry/status":
			if retries.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, p2pCovStatusBody("7"))
		case "/badjson/status":
			_, _ = io.WriteString(w, "{not json")
		case "/badheight/status":
			_, _ = io.WriteString(w, p2pCovStatusBody("abc"))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	h, err := fetchBlockHeight(srv.URL + "/ok")
	require.NoError(t, err)
	require.Equal(t, int64(12345), h)

	h, err = fetchBlockHeight(srv.URL + "/retry")
	require.NoError(t, err)
	require.Equal(t, int64(7), h)
	require.Equal(t, int32(2), retries.Load(), "503 is retried once")

	_, err = fetchBlockHeight(srv.URL + "/other")
	require.EqualError(t, err, "thornode status bad request: 400")

	_, err = fetchBlockHeight(srv.URL + "/badjson")
	require.ErrorContains(t, err, "failed to unmarshal tendermint status")

	_, err = fetchBlockHeight(srv.URL + "/badheight")
	require.ErrorContains(t, err, "invalid syntax")

	// connection error
	_, err = fetchBlockHeight("http://127.0.0.1:1")
	require.Error(t, err)
}

func TestFetchPeerIdCov(t *testing.T) {
	var retries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, "peer-1")
		case "/retry":
			if retries.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "peer-1")
		case "/redirect":
			w.WriteHeader(http.StatusMultipleChoices) // 300 is not > 300
			_, _ = io.WriteString(w, "peer-1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// fetchPeerId formats "http://<addr>:6040/p2pid"; push the fixed port and
	// path into a query string so the request lands on the test server.
	addr := func(path string) string { return p2pCovHostPort(srv) + path + "?x=" }

	id, err := fetchPeerId(addr("/ok"), "peer-1")
	require.NoError(t, err)
	require.Equal(t, "peer-1", id)

	id, err = fetchPeerId(addr("/retry"), "peer-1")
	require.NoError(t, err)
	require.Equal(t, "peer-1", id)
	require.Equal(t, int32(2), retries.Load(), "503 is retried once")

	id, err = fetchPeerId(addr("/redirect"), "peer-1")
	require.NoError(t, err)
	require.Equal(t, "peer-1", id)

	_, err = fetchPeerId(addr("/ok"), "peer-2")
	require.EqualError(t, err, "peer id mismatch: peer-1 != peer-2")

	_, err = fetchPeerId(addr("/missing"), "peer-1")
	require.EqualError(t, err, "bifrost p2pid bad request: 404")

	// connection refused
	_, err = fetchPeerId("127.0.0.1:1/x?y=", "peer-1")
	require.Error(t, err)
}

func TestGetPeersConnectFailureCov(t *testing.T) {
	// A syntactically valid multiaddr pointing at a closed local port: the
	// host and DHT start, but the dial fails so no peers are recorded.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port
	require.NoError(t, ln.Close())

	in := map[string]string{"existing": "/ip4/9.9.9.9/tcp/5040"}
	src := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", port, p2pCovPeerID)
	out, err := getPeers(in, src)
	require.Error(t, err)
	require.Equal(t, map[string]string{"existing": "/ip4/9.9.9.9/tcp/5040"}, out)
}

// a well-formed libp2p peer id (no host is listening for it)
const p2pCovPeerID = "QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N"

// p2pCovUnsealSDKConfig resets the cosmos-sdk global config seal so main(),
// which sets bech32 prefixes and seals, can be invoked more than once.
func p2pCovUnsealSDKConfig(t *testing.T) {
	t.Helper()
	v := reflect.ValueOf(cosmos.GetConfig()).Elem()
	sealed := v.FieldByName("sealed")
	reflect.NewAt(sealed.Type(), unsafe.Pointer(sealed.UnsafeAddr())).Elem().SetBool(false) //nolint:gosec
	ch := v.FieldByName("sealedch")
	reflect.NewAt(ch.Type(), unsafe.Pointer(ch.UnsafeAddr())).Elem(). //nolint:gosec
										Set(reflect.ValueOf(make(chan struct{})))
}

func p2pCovRunMain(t *testing.T) (out string, recovered any) {
	t.Helper()
	p2pCovUnsealSDKConfig(t)
	out = p2pCovCapture(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	return out, recovered
}

func TestMainCov(t *testing.T) {
	nodes := []openapi.Node{
		{
			// ready: discovery is attempted against 127.0.0.1:5040 and fails
			PeerId:          p2pCovPeerID,
			IpAddress:       "127.0.0.1",
			NodeAddress:     "thor1ready",
			PreflightStatus: openapi.NodePreflightStatus{Status: types.NodeStatus_Ready.String()},
		},
		{
			// not ready: skipped
			PeerId:          "QmNotReady",
			IpAddress:       "127.0.0.2",
			NodeAddress:     "thor1standby",
			PreflightStatus: openapi.NodePreflightStatus{Status: types.NodeStatus_Standby.String()},
		},
	}
	nodesJSON, err := json.Marshal(nodes)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good/thorchain/nodes":
			_, _ = w.Write(nodesJSON)
		case "/badjson/thorchain/nodes":
			_, _ = io.WriteString(w, "[{")
		case "/rpc/status":
			_, _ = io.WriteString(w, p2pCovStatusBody("100"))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	t.Setenv("RPC", srv.URL+"/rpc")

	// happy path: one ready node whose discovery fails, nothing to probe
	t.Setenv("THORNODE", srv.URL+"/good")
	out, rec := p2pCovRunMain(t)
	require.Nil(t, rec)
	require.Contains(t, out, "Discovering IP addresses for nodes...\n")
	require.Contains(t, out, "Error ")
	require.Contains(t, out, "Remaining: 1\n")
	require.Contains(t, out, "\nTesting p2p health...\n")
	require.True(t, strings.HasSuffix(out, "OK\n"), out)

	// node list cannot be decoded: no discovery, still reports OK
	t.Setenv("THORNODE", srv.URL+"/badjson")
	out, rec = p2pCovRunMain(t)
	require.Nil(t, rec)
	require.Contains(t, out, "fail to decode thornode status")
	require.NotContains(t, out, "Remaining:")
	require.True(t, strings.HasSuffix(out, "OK\n"), out)

	// node list request fails outright
	t.Setenv("THORNODE", "http://127.0.0.1:1")
	out, rec = p2pCovRunMain(t)
	require.Nil(t, rec)
	require.Contains(t, out, "fail to get thornode status")
	require.True(t, strings.HasSuffix(out, "OK\n"), out)

	// tip height cannot be fetched: panics
	t.Setenv("RPC", srv.URL+"/nope")
	_, rec = p2pCovRunMain(t)
	recErr, ok := rec.(error)
	require.True(t, ok, "expected error panic, got %v", rec)
	require.EqualError(t, recErr, "thornode status bad request: 400")

	// main seals the bech32 config with the mainnet prefixes
	require.Equal(t, "thor", cosmos.GetConfig().GetBech32AccountAddrPrefix())
}

// TestMainDiscoveryCov runs a real libp2p host on 127.0.0.1:5040 (the port
// main hard-codes) so discovery succeeds and the per-node health probe runs.
// getPeers sleeps 5s after connecting, so this is the one slow test here.
func TestMainDiscoveryCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/5040"))
	if err != nil {
		t.Skipf("cannot listen on 127.0.0.1:5040: %v", err)
	}
	defer remote.Close()
	remoteID := remote.ID().String()

	nodes := []openapi.Node{{
		PeerId:              remoteID,
		IpAddress:           "127.0.0.1",
		NodeAddress:         "thor1nodeaddressabcd",
		NodeOperatorAddress: "thor1operatoraddress",
		Status:              "Active",
		PreflightStatus:     openapi.NodePreflightStatus{Status: types.NodeStatus_Ready.String()},
	}}
	nodesJSON, err := json.Marshal(nodes)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thorchain/nodes":
			_, _ = w.Write(nodesJSON)
		case "/status":
			_, _ = io.WriteString(w, p2pCovStatusBody("100"))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	t.Setenv("THORNODE", srv.URL)
	t.Setenv("RPC", srv.URL)

	// Discovery finds 127.0.0.1 for the node; the bifrost p2pid probe on
	// 127.0.0.1:6040 then fails (nothing serving the node's id there), so the
	// node is reported as failing.
	out, rec := p2pCovRunMain(t)
	require.Nil(t, rec)
	require.Contains(t, out, "Connected to: "+remoteID+"\n")
	require.Contains(t, out, "Remaining: 0\n")
	require.Contains(t, out, "Node\t Operator\t Status\t Error\t\n")
	require.Contains(t, out, "abcd \t ress \t\t Active \t ")
	require.Contains(t, out, "Count failing: 1\n")
	require.NotContains(t, out, "OK\n")
}
