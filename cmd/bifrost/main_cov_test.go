package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// runMainCovEnv makes the test binary run bifrost's main instead of the tests,
// so main's startup checks (which exit the process) can be exercised.
const runMainCovEnv = "BIFROST_TEST_RUN_MAIN_COV"

func TestMain(m *testing.M) {
	if os.Getenv(runMainCovEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// bridgeCov is a minimal thorchain bridge for the keyshare recovery helpers.
type bridgeCov struct {
	thorclient.ThorchainBridge
	calls   atomic.Int32
	failFor int32 // number of initial GetNodeAccount calls that fail
	onCall  func(n int32)
}

func (b *bridgeCov) GetContext() client.Context { return client.Context{} }

func (b *bridgeCov) GetNodeAccount(string) (*types.NodeAccount, error) {
	n := b.calls.Add(1)
	if b.onCall != nil {
		b.onCall(n)
	}
	if n <= b.failFor {
		return nil, errors.New("thornode unavailable")
	}
	// no signer membership: nothing to recover
	return &types.NodeAccount{Status: types.NodeStatus_Active}, nil
}

func fastBlockTimeCov(t *testing.T, d time.Duration) {
	t.Helper()
	old := constants.ThorchainBlockTime
	constants.ThorchainBlockTime = d
	t.Cleanup(func() { constants.ThorchainBlockTime = old })
}

func TestRecoverLegacyKeysharesBeforeStartupCov(t *testing.T) {
	fastBlockTimeCov(t, time.Millisecond)

	// immediate success
	b := &bridgeCov{}
	start, degraded := recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, b)
	require.True(t, start)
	require.False(t, degraded)
	require.Equal(t, int32(1), b.calls.Load())

	// transient failures are retried in the foreground until they succeed
	b = &bridgeCov{failFor: 2}
	start, degraded = recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, b)
	require.True(t, start)
	require.False(t, degraded)
	require.Equal(t, int32(3), b.calls.Load())
}

func TestRecoverLegacyKeysharesInterruptedCov(t *testing.T) {
	// a long block time keeps the retry loop waiting; a SIGINT delivered while
	// it waits (the handler is registered before the first attempt) aborts it
	fastBlockTimeCov(t, time.Hour)
	b := &bridgeCov{failFor: 1 << 30, onCall: func(n int32) {
		if n == 1 {
			require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		}
	}}
	done := make(chan [2]bool, 1)
	go func() {
		s, d := recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, b)
		done <- [2]bool{s, d}
	}()
	select {
	case res := <-done:
		require.False(t, res[0], "startup must not proceed after a shutdown signal")
		require.False(t, res[1])
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not stop on SIGINT")
	}
	require.Equal(t, int32(1), b.calls.Load())
}

func TestSuperviseFrostKeyshareRecoveryCov(t *testing.T) {
	fastBlockTimeCov(t, time.Millisecond)

	// returns once recovery succeeds
	b := &bridgeCov{failFor: 2}
	superviseFrostKeyshareRecovery(context.Background(), config.Bifrost{}, b)
	require.Equal(t, int32(3), b.calls.Load())

	// returns when the context ends
	fastBlockTimeCov(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	b = &bridgeCov{failFor: 1 << 30, onCall: func(int32) { cancel() }}
	superviseFrostKeyshareRecovery(ctx, config.Bifrost{}, b)
	require.Equal(t, int32(1), b.calls.Load())
}

func TestSuperviseLegacyKeyshareRecoveryCanceledCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &bridgeCov{}
	superviseLegacyKeyshareRecovery(ctx, config.Bifrost{}, b)
	// the first attempt waits a full backoff interval, so nothing ran
	require.Equal(t, int32(0), b.calls.Load())
}

func TestInitLogCov(t *testing.T) {
	oldLevel := zerolog.GlobalLevel()
	oldLogger := log.Logger
	t.Cleanup(func() {
		zerolog.SetGlobalLevel(oldLevel)
		log.Logger = oldLogger
	})

	for level, want := range map[string]zerolog.Level{
		"debug": zerolog.DebugLevel,
		"info":  zerolog.InfoLevel,
		"warn":  zerolog.WarnLevel,
		"error": zerolog.ErrorLevel,
		"fatal": zerolog.FatalLevel,
		"panic": zerolog.PanicLevel,
	} {
		initLog(level, level == "debug")
		require.Equal(t, want, zerolog.GlobalLevel(), level)
	}
}

// ---------------------------------------------------------------------------
// main() startup checks, run in a subprocess
// ---------------------------------------------------------------------------

const (
	mainCovSigner   = "bifrostcov"
	mainCovPassword = "password1234"
)

// mainCovEnv returns an environment that keeps bifrost's startup hermetic:
// metrics off, all db paths under dir and bootstrap peers set (so no seed
// lookup happens).
func mainCovEnv(dir string, extra ...string) []string {
	env := append(os.Environ(),
		runMainCovEnv+"=1",
		"HOME="+dir,
		"BIFROST_METRICS_ENABLED=false",
		"PEER=127.0.0.1",
		"BIFROST_SIGNER_SIGNER_DB_PATH="+filepath.Join(dir, "signer"),
		"SIGNER_NAME=",
		"SIGNER_PASSWD=",
	)
	for _, chain := range []string{
		"btc", "eth", "base", "pol", "tron", "avax", "bsc", "doge", "gaia",
		"noble", "xrp", "xmr", "ltc", "bch", "sol", "zec", "sui", "ada",
	} {
		env = append(env, "BIFROST_CHAINS_"+strings.ToUpper(chain)+"_BLOCK_SCANNER_DB_PATH="+filepath.Join(dir, "observer", chain))
	}
	return append(env, extra...)
}

func runMainCov(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...) // #nosec G204 -- re-executes this test binary
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, out.String())
		code = exitErr.ExitCode()
	}
	return out.String(), code
}

// newKeyringCov creates a file keyring holding the signer key and, optionally,
// its eddsa key.
func newKeyringCov(t *testing.T, withEddsa bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".thornode")
	buf := bytes.NewBufferString(mainCovPassword + "\n" + mainCovPassword + "\n")
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb, err := ckeys.New(cosmos.KeyringServiceName(), ckeys.BackendFile, dir, buf, codec.NewProtoCodec(registry),
		func(o *ckeys.Options) { o.SupportedAlgos = ckeys.SigningAlgoList{hd.Secp256k1, ed25519.Ed25519} })
	require.NoError(t, err)
	_, mnemonic, err := kb.NewMnemonic(mainCovSigner, ckeys.English, "m/44'/931'/0'/0/0", "", hd.Secp256k1)
	require.NoError(t, err)
	if withEddsa {
		_, err = kb.NewAccount(ed25519.SignerNameEDDSA(mainCovSigner), mnemonic, "", ed25519.HDPath, ed25519.Ed25519)
		require.NoError(t, err)
	}
	return dir
}

// thornodeMainCov serves the THORNode endpoints bifrost hits during startup:
// the node account is active (whitelisted) and has nothing to recover.
func thornodeMainCov(t *testing.T) string {
	t.Helper()
	na, err := json.Marshal(types.NodeAccount{Status: types.NodeStatus_Active})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, thorclient.NodeAccountEndpoint+"/"):
			_, _ = w.Write(na)
		case r.URL.Path == thorclient.PubKeysEndpoint:
			_, _ = w.Write([]byte(`{"asgard":[],"inactive":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

func TestMainVersionCov(t *testing.T) {
	out, code := runMainCov(t, mainCovEnv(t.TempDir()), "--version")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "bifrost v"+constants.Version)
}

func TestMainStartupChecksCov(t *testing.T) {
	dir := t.TempDir()
	host := thornodeMainCov(t)
	secpOnly := newKeyringCov(t, false)
	withEddsa := newKeyringCov(t, true)
	signer := []string{"SIGNER_NAME=" + mainCovSigner, "SIGNER_PASSWD=" + mainCovPassword}
	withKeys := func(home string, extra ...string) []string {
		return append(append(append([]string{}, signer...),
			"BIFROST_THORCHAIN_CHAIN_HOME_FOLDER="+home, "CHAIN_API="+host), extra...)
	}

	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{name: "no signer name", args: []string{"-l", "debug", "-p"}, want: "signer name is empty"},
		{name: "no signer password", env: []string{"SIGNER_NAME=" + mainCovSigner}, want: "signer password is empty"},
		{
			name: "missing keyring key",
			env:  append(append([]string{}, signer...), "BIFROST_THORCHAIN_CHAIN_HOME_FOLDER="+filepath.Join(dir, "empty")),
			want: "fail to get keyring keybase",
		},
		{name: "no eddsa key", env: withKeys(secpOnly), want: "fail to get eddsa private key"},
		{
			name: "keygen timeout above jail time",
			env:  withKeys(withEddsa, "BIFROST_SIGNER_KEYGEN_TIMEOUT=1000h"),
			want: "keygen timeout must be shorter than jail time",
		},
		{
			name: "keysign timeout above jail time",
			env:  withKeys(withEddsa, "BIFROST_SIGNER_KEYGEN_TIMEOUT=1s", "BIFROST_SIGNER_KEYSIGN_TIMEOUT=1000h"),
			want: "keysign timeout must be shorter than jail time",
		},
		{
			// an invalid p2p port fails before anything listens
			name: "invalid p2p port",
			env:  withKeys(withEddsa, "BIFROST_SIGNER_KEYGEN_TIMEOUT=1s", "BIFROST_SIGNER_KEYSIGN_TIMEOUT=1s", "BIFROST_TSS_P2P_PORT=99999"),
			want: "fail to start p2p",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each run is a separate process with its own home dir
			out, code := runMainCov(t, mainCovEnv(t.TempDir(), tc.env...), tc.args...)
			require.Equal(t, 1, code, out)
			require.Contains(t, out, tc.want)
		})
	}
}
