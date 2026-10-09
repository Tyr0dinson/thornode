package chainclients

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	loadChainsCovUnsupportedEnv = "LOADCHAINS_COV_UNSUPPORTED_CHAIN"
	loadChainsCovRetryEnv       = "LOADCHAINS_COV_RETRY"
)

func TestMain(m *testing.M) {
	if os.Getenv(loadChainsCovUnsupportedEnv) == "1" {
		// an unsupported chain is fatal: log.Fatal exits the process
		LoadChains(nil, map[common.Chain]config.BifrostChainConfiguration{
			common.THORChain: {ChainID: common.THORChain},
		}, nil, nil, nil, nil, nil)
		os.Exit(0) // not reached
	}
	if os.Getenv(loadChainsCovRetryEnv) == "1" {
		// set before any test starts so the retry goroutine reads it race-free
		failedChainRetryInterval = 5 * time.Millisecond
	}
	os.Exit(m.Run())
}

// loadChainsCovSubprocess re-executes this test binary running only the named
// test with the given env flag set, and returns its combined output.
func loadChainsCovSubprocess(t *testing.T, env string, args ...string) (string, int) {
	t.Helper()
	// share the coverage counter dir so the subprocess's coverage is merged
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	cmd := exec.Command(os.Args[0], args...) // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), env+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, out.String())
	return out.String(), exitErr.ExitCode()
}

func TestLoadChainsUnsupportedChainCov(t *testing.T) {
	out, code := loadChainsCovSubprocess(t, loadChainsCovUnsupportedEnv)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "chain THOR is not supported")
}

// TestLoadChainsRetryCov runs TestLoadChainsRetryInnerCov in a subprocess so
// the shortened retry interval cannot race with retry goroutines leaked by
// other tests in this package.
func TestLoadChainsRetryCov(t *testing.T) {
	out, code := loadChainsCovSubprocess(t, loadChainsCovRetryEnv, "-test.run=^TestLoadChainsRetryInnerCov$", "-test.v")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "--- PASS: TestLoadChainsRetryInnerCov")
	require.Contains(t, out, "chain loaded, restarting bifrost")
}

// TestLoadChainsRetryInnerCov loads a DOGE client whose daemon rejects the node
// address import: the chain is reported as failed and retried on every tick
// until the daemon recovers, at which point the restart channel is closed.
func TestLoadChainsRetryInnerCov(t *testing.T) {
	if os.Getenv(loadChainsCovRetryEnv) != "1" {
		t.Skip("run via TestLoadChainsRetryCov")
	}
	keys := newKeysCov(t)
	metricsOnceCov.Do(func() {
		metricsCov, metricsErrCov = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.DOGEChain},
		})
	})
	require.NoError(t, metricsErrCov)

	backend := newFakeNodeCov(t)
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	var failing atomic.Bool
	var importCalls atomic.Int32
	failing.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req struct {
				Method string `json:"method"`
				ID     any    `json:"id"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Method == "importaddress" {
				importCalls.Add(1)
				if failing.Load() {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id":     req.ID,
						"result": nil,
						"error":  map[string]any{"code": -4, "message": "wallet unavailable"},
					})
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       srv.Listener.Addr().String(),
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: t.TempDir(),
	}, metricsCov, keys)
	require.NoError(t, err)
	pkm, err := pubkeymanager.NewPubKeyManager(bridge, metricsCov)
	require.NoError(t, err)
	pkv := &pubKeyValidatorCov{PubKeyManager: pkm}

	doge := config.BifrostChainConfiguration{
		ChainID:     common.DOGEChain,
		RPCHost:     srv.Listener.Addr().String(),
		UserName:    "bob",
		Password:    "password",
		DisableTLS:  true,
		HTTPostMode: true,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:              common.DOGEChain,
			StartBlockHeight:     1,
			GasPriceResolution:   500_000,
			MaxReorgRescanBlocks: 1,
			DBPath:               t.TempDir(),
		},
	}
	doge.UTXO.TransactionBatchSize = 100
	doge.UTXO.MaxMempoolBatches = 10

	chains, restart := LoadChains(keys, map[common.Chain]config.BifrostChainConfiguration{
		common.DOGEChain: doge,
	}, nil, bridge, metricsCov, pkv, nil)
	require.Empty(t, chains)
	require.Zero(t, pkv.callbacks.Load(), "failed chain registers no pubkey callback")
	require.GreaterOrEqual(t, importCalls.Load(), int32(1))

	// the failed chain keeps being retried while the daemon is down
	require.Eventually(t, func() bool { return importCalls.Load() >= 3 }, 10*time.Second, time.Millisecond)
	select {
	case <-restart:
		t.Fatal("restart closed while the chain still fails")
	default:
	}

	// once the daemon recovers the next retry succeeds and requests a restart
	failing.Store(false)
	select {
	case <-restart:
	case <-time.After(10 * time.Second):
		t.Fatal("restart channel not closed after the chain recovered")
	}
	// retries stop after the restart is requested
	n := importCalls.Load()
	time.Sleep(15 * time.Millisecond) // three retry intervals
	require.Equal(t, n, importCalls.Load())
}
