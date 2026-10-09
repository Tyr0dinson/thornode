package cli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	signerNameCov   = "covsigner"
	signerPasswdCov = "password1234"
)

// setupSignerKeyringCov creates a file keyring holding a signer key in a
// temp dir and points the signing env vars at it. It returns the signer's
// public key.
func setupSignerKeyringCov(t *testing.T) *secp256k1.PubKey {
	t.Helper()
	dir := t.TempDir()
	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	cdc := codec.NewProtoCodec(reg)
	kb, err := ckeys.New(cosmos.KeyringServiceName(), ckeys.BackendFile, dir,
		strings.NewReader(signerPasswdCov+"\n"+signerPasswdCov+"\n"), cdc)
	require.NoError(t, err)
	rec, _, err := kb.NewMnemonic(signerNameCov, ckeys.English, sdk.FullFundraiserPath, "", hd.Secp256k1)
	require.NoError(t, err)
	pk, err := rec.GetPubKey()
	require.NoError(t, err)

	t.Setenv(cosmos.EnvChainHome, dir)
	t.Setenv(cosmos.EnvSignerName, signerNameCov)
	t.Setenv(cosmos.EnvSignerPassword, signerPasswdCov)
	secpPk, ok := pk.(*secp256k1.PubKey)
	require.True(t, ok)
	return secpPk
}

func verifySignedMsgCov(t *testing.T, pk *secp256k1.PubKey, line string, wantMsg string) {
	t.Helper()
	var signed SignedMsg
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(line)), &signed), line)
	require.Equal(t, wantMsg, signed.Msg)
	pkBz, err := base64.StdEncoding.DecodeString(signed.Pubkey)
	require.NoError(t, err)
	require.Equal(t, pk.Bytes(), pkBz)
	sig, err := base64.StdEncoding.DecodeString(signed.Signature)
	require.NoError(t, err)
	require.True(t, pk.VerifySignature([]byte(wantMsg), sig))
}

func TestGetUtilCmdCov(t *testing.T) {
	cmd := GetUtilCmd()
	require.Equal(t, "util", cmd.Use)
	names := []string{}
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	require.ElementsMatch(t, []string{"sign-file", "sign-string"}, names)
}

func TestSignStringAndFileCov(t *testing.T) {
	pk := setupSignerKeyringCov(t)

	// sign-string
	cmd := GetCmdSignString()
	cmd.SetArgs([]string{"hello thorchain"})
	var err error
	stdout := captureStdoutCov(t, func() { err = cmd.Execute() })
	require.NoError(t, err)
	verifySignedMsgCov(t, pk, stdout, "hello thorchain")

	// sign-file
	file := filepath.Join(t.TempDir(), "msg.txt")
	require.NoError(t, os.WriteFile(file, []byte("file contents"), 0o600))
	cmd = GetCmdSignFile()
	cmd.SetArgs([]string{file})
	stdout = captureStdoutCov(t, func() { err = cmd.Execute() })
	require.NoError(t, err)
	verifySignedMsgCov(t, pk, stdout, "file contents")
}

func TestSignErrorsCov(t *testing.T) {
	setupSignerKeyringCov(t)

	// missing file
	cmd := GetCmdSignFile()
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "missing")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.ErrorIs(t, cmd.Execute(), os.ErrNotExist)

	// wrong arg count
	cmd = GetCmdSignString()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.ErrorContains(t, cmd.Execute(), "accepts 1 arg(s)")

	// unknown signer key
	t.Setenv(cosmos.EnvSignerName, "nobody")
	cmd = GetCmdSignString()
	cmd.SetArgs([]string{"x"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var err error
	stdout := captureStdoutCov(t, func() { err = cmd.Execute() })
	require.Error(t, err)
	require.Empty(t, stdout)
}

func TestGetQueryCmdCov(t *testing.T) {
	cmd := GetQueryCmd()
	require.Equal(t, types.ModuleName, cmd.Use)
	names := []string{}
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	require.ElementsMatch(t, []string{"version", "discord-relay"}, names)
}

func TestVersionCmdCov(t *testing.T) {
	// the command forces json output even when text is requested
	ctx := txClientCtxCov(nil, nil).WithOutputFormat("text")
	txOut, _, err := runCmdCov(t, GetCmdGetVersion(), ctx)
	require.NoError(t, err)
	var v ver
	require.NoError(t, json.Unmarshal([]byte(txOut), &v), txOut)
	require.Equal(t, constants.SWVersion.String(), v.Version)
	require.Equal(t, constants.GitCommit, v.GitCommit)
	require.Equal(t, constants.BuildTime, v.BuildTime)
	require.Equal(t, v.Version, v.String())

	// bad --node fails before printing
	txOut, _, err = runCmdCov(t, GetCmdGetVersion(), ctx, "--node", "tcp://bad\x7fhost:1")
	require.Error(t, err)
	require.Empty(t, txOut)
}

func TestDiscordRelayCmdCov(t *testing.T) {
	pk := setupSignerKeyringCov(t)

	var mu sync.Mutex
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uuid_prefix":
			_, _ = io.WriteString(w, "abcd1234")
		case "/msg":
			bz, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(bz, &posted)
			mu.Unlock()
			_, _ = io.WriteString(w, "relayed")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("THOR_NODE_RELAY_URL", srv.URL)
	config.Init()
	require.Equal(t, srv.URL, config.GetThornode().NodeRelayURL)

	ctx := txClientCtxCov(nil, nil)
	out, _, err := runCmdCov(t, GetCmdGetNORelay(), ctx, "general", "hello world")
	require.NoError(t, err)
	require.Contains(t, out, "relayed")

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, posted)
	bz, err := json.Marshal(posted)
	require.NoError(t, err)
	body := string(bz)
	require.Contains(t, body, "hello world")
	require.Contains(t, body, "general")
	// uuid uses the server supplied prefix
	require.Contains(t, body, "abcd1234-")
	require.Contains(t, body, base64.StdEncoding.EncodeToString(pk.Bytes()))

	// wrong arg count
	_, _, err = runCmdCov(t, GetCmdGetNORelay(), ctx, "only-one")
	require.ErrorContains(t, err, "accepts 2 arg(s)")

	// bad --node fails before relaying
	_, _, err = runCmdCov(t, GetCmdGetNORelay(), ctx, "a", "b", "--node", "tcp://bad\x7fhost:1")
	require.Error(t, err)
}
