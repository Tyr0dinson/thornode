package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	relayCovSigner   = "relaycov"
	relayCovPassword = "password1234"
)

// relayCovServer is a fake node relay service.
type relayCovServer struct {
	mu     sync.Mutex
	prefix string
	posted []NodeRelay
}

func (s *relayCovServer) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/uuid_prefix":
		_, _ = io.WriteString(w, s.prefix)
	case r.Method == http.MethodPost && r.URL.Path == "/msg":
		var nr NodeRelay
		if err := json.NewDecoder(r.Body).Decode(&nr); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.posted = append(s.posted, nr)
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, "ok:%s", nr.Msg.UUID)
	default:
		http.NotFound(w, r)
	}
}

func setupRelayCovConfig(t *testing.T, url string) {
	t.Helper()
	t.Setenv("THOR_NODE_RELAY_URL", url)
	config.Init()
	require.Equal(t, url, config.GetThornode().NodeRelayURL)
}

func setupRelayCovKeyring(t *testing.T) types.PubKey {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".thornode")
	buf := bytes.NewBufferString(relayCovPassword + "\n" + relayCovPassword + "\n")
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb, err := ckeys.New(cosmos.KeyringServiceName(), ckeys.BackendFile, dir, buf, cdc)
	require.NoError(t, err)
	rec, _, err := kb.NewMnemonic(relayCovSigner, ckeys.English, "m/44'/931'/0'/0/0", "", hd.Secp256k1)
	require.NoError(t, err)
	pk, err := rec.GetPubKey()
	require.NoError(t, err)

	t.Setenv(cosmos.EnvChainHome, dir)
	t.Setenv(cosmos.EnvSignerName, relayCovSigner)
	t.Setenv(cosmos.EnvSignerPassword, relayCovPassword)
	return pk
}

func TestNewNodeRelayCov(t *testing.T) {
	nr := NewNodeRelay("general", "hello")
	require.Equal(t, "general", nr.Msg.Channel)
	require.Equal(t, "hello", nr.Msg.Text)
	require.Empty(t, nr.Msg.UUID)
	require.Empty(t, nr.Signature)
	require.Empty(t, nr.PubKey)
}

func TestNodeRelayPrepareAndBroadcastCov(t *testing.T) {
	fake := &relayCovServer{prefix: "abcd1234"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	setupRelayCovConfig(t, srv.URL)
	pk := setupRelayCovKeyring(t)

	nr := NewNodeRelay("chan", "some text")
	require.NoError(t, nr.Prepare())

	// uuid keeps the 5-part shape but the first part is replaced by the server prefix
	parts := strings.Split(nr.Msg.UUID, "-")
	require.Len(t, parts, 5)
	require.Equal(t, "abcd1234", parts[0])

	// signature verifies against the keyring key over uuid|channel|text
	pubBytes, err := base64.StdEncoding.DecodeString(nr.PubKey)
	require.NoError(t, err)
	require.Equal(t, pk.Bytes(), pubBytes)
	sig, err := base64.StdEncoding.DecodeString(nr.Signature)
	require.NoError(t, err)
	msg := fmt.Sprintf("%s|%s|%s", nr.Msg.UUID, "chan", "some text")
	require.True(t, pk.VerifySignature([]byte(msg), sig))

	resp, err := nr.Broadcast()
	require.NoError(t, err)
	require.Equal(t, "ok:"+nr.Msg.UUID, resp)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.posted, 1)
	require.Equal(t, *nr, fake.posted[0])
}

func TestNodeRelayPrepareSignFailsCov(t *testing.T) {
	fake := &relayCovServer{prefix: "ffff0000"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	setupRelayCovConfig(t, srv.URL)
	setupRelayCovKeyring(t)
	t.Setenv(cosmos.EnvSignerName, "unknown-signer")

	nr := NewNodeRelay("chan", "text")
	err := nr.Prepare()
	require.Error(t, err)
	// uuid was fetched before signing failed
	require.True(t, strings.HasPrefix(nr.Msg.UUID, "ffff0000-"))
	require.Empty(t, nr.Signature)
	require.Empty(t, nr.PubKey)
}

func TestNodeRelayNetworkErrorsCov(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // closed server: connections are refused
	setupRelayCovConfig(t, url)

	nr := NewNodeRelay("chan", "text")
	require.Error(t, nr.Prepare())
	require.Empty(t, nr.Msg.UUID)

	resp, err := nr.Broadcast()
	require.Error(t, err)
	require.Empty(t, resp)
}
