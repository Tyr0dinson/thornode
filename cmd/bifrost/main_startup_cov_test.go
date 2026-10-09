package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	tcrypto "github.com/cometbft/cometbft/crypto"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"
	bkeygen "gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// mainFakesCovEnv, set alongside runMainCovEnv, swaps main's p2p/TSS/chain
// startup seams for in-memory fakes:
//
//	tss-error: p2p starts on an in-memory libp2p host; TSS creation fails
//	no-chains: real TSS (fixture preparams); chain loading yields no chains
//	full:      as no-chains, but one fake chain loads and a restart is
//	           requested at once, so main runs its whole shutdown sequence
const mainFakesCovEnv = "BIFROST_TEST_MAIN_FAKES_COV"

func init() {
	mode := os.Getenv(mainFakesCovEnv)
	if mode == "" || os.Getenv(runMainCovEnv) != "1" {
		return
	}
	startP2P = startP2PFakeCov
	newTss = func(comm *p2p.Communication, sm storage.LocalStateManager, pk, pkEddsa tcrypto.PrivKey, conf gotsscommon.TssConfig, _ *bkeygen.LocalPreParams) (*tss.TssServer, error) {
		if mode == "tss-error" {
			return nil, errors.New("tss unavailable")
		}
		pp, err := fixturePreParamsCov()
		if err != nil {
			return nil, err
		}
		fmt.Printf("TSSCONF keygen=%s keysign=%s monitor=%t\n", conf.KeyGenTimeout, conf.KeySignTimeout, conf.EnableMonitor)
		return tss.NewTss(comm, sm, pk, pkEddsa, conf, pp)
	}
	loadChains = func(_ *thorclient.Keys, cfg map[common.Chain]config.BifrostChainConfiguration, server *tss.TssServer, _ thorclient.ThorchainBridge, _ *metrics.Metrics, _ pubkeymanager.PubKeyValidator, _ thorclient.PoolManager) (map[common.Chain]chainclients.ChainClient, chan struct{}) {
		// main's RPC-host normalisation is visible here
		fmt.Printf("LOADCHAINS gaia_rpc=%s tss=%t\n", cfg[common.GAIAChain].RPCHost, server != nil)
		restart := make(chan struct{})
		if mode != "full" {
			return nil, restart
		}
		close(restart)
		return map[common.Chain]chainclients.ChainClient{
			common.GAIAChain: &fakeChainCov{cfg: cfg[common.GAIAChain]},
		}, restart
	}
}

// startP2PFakeCov builds the real communication layer but backs it with an
// in-memory libp2p host instead of listening on the network.
func startP2PFakeCov(cfg p2p.P2PConfig, _ tcrypto.PrivKey, baseFolder string, bridge thorclient.ThorchainBridge) (*p2p.Communication, *storage.FileStateMgr, error) {
	sm, err := storage.NewFileStateMgr(baseFolder)
	if err != nil {
		return nil, nil, err
	}
	comm, err := p2p.NewCommunication(cfg, sm, bridge)
	if err != nil {
		return nil, nil, err
	}
	h, err := mocknet.New(context.Background()).GenPeer()
	if err != nil {
		return nil, nil, err
	}
	field := reflect.ValueOf(comm).Elem().FieldByName("host")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(h)) //nolint:gosec
	return comm, sm, nil
}

func fixturePreParamsCov() (*bkeygen.LocalPreParams, error) {
	f, err := os.Open("../../bifrost/tss/go-tss/test_data/preParam_test.data")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	bz, err := hex.DecodeString(strings.TrimSpace(line))
	if err != nil {
		return nil, err
	}
	pp := new(bkeygen.LocalPreParams)
	return pp, json.Unmarshal(bz, pp)
}

// fakeChainCov is an idle chain client that records its lifecycle on stdout.
type fakeChainCov struct {
	cfg     config.BifrostChainConfiguration
	started atomic.Bool
}

func (c *fakeChainCov) Start(chan stypes.TxIn, chan stypes.ErrataBlock, chan stypes.Solvency, chan common.NetworkFee) {
	c.started.Store(true)
	fmt.Println("FAKECHAIN started")
}

func (c *fakeChainCov) Stop() {
	fmt.Printf("FAKECHAIN stopped (started=%t)\n", c.started.Load())
}
func (c *fakeChainCov) IsBlockScannerHealthy() bool { return true }
func (c *fakeChainCov) SignTx(stypes.TxOutItem, int64) ([]byte, []byte, *stypes.TxInItem, error) {
	return nil, nil, nil, errors.New("not supported")
}

func (c *fakeChainCov) BroadcastTx(stypes.TxOutItem, []byte) (string, error) {
	return "", errors.New("not supported")
}
func (c *fakeChainCov) GetHeight() (int64, error)       { return 1, nil }
func (c *fakeChainCov) GetAddress(common.PubKey) string { return "" }
func (c *fakeChainCov) GetAccount(common.PubKey, *big.Int) (common.Account, error) {
	return common.Account{}, nil
}

func (c *fakeChainCov) GetAccountByAddress(string, *big.Int) (common.Account, error) {
	return common.Account{}, nil
}
func (c *fakeChainCov) GetChain() common.Chain                             { return common.GAIAChain }
func (c *fakeChainCov) GetConfig() config.BifrostChainConfiguration        { return c.cfg }
func (c *fakeChainCov) OnObservedTxIn(stypes.TxInItem, int64)              {}
func (c *fakeChainCov) GetConfirmationCount(stypes.TxIn) int64             { return 0 }
func (c *fakeChainCov) ConfirmationCountReady(stypes.TxIn) bool            { return true }
func (c *fakeChainCov) GetBlockScannerHeight() (int64, error)              { return 1, nil }
func (c *fakeChainCov) GetLatestTxForVault(string) (string, string, error) { return "", "", nil }
func (c *fakeChainCov) RollbackBlockScanner() error                        { return nil }

// mainStartupEnvCov is mainCovEnv plus a signer whose keyring holds both keys,
// passing every startup check up to the p2p layer.
func mainStartupEnvCov(t *testing.T, mode string, extra ...string) []string {
	t.Helper()
	thornode := thornodeStartupCov(t)
	env := []string{
		mainFakesCovEnv + "=" + mode,
		"SIGNER_NAME=" + mainCovSigner,
		"SIGNER_PASSWD=" + mainCovPassword,
		"BIFROST_THORCHAIN_CHAIN_HOME_FOLDER=" + newKeyringCov(t, true),
		"CHAIN_API=" + thornode,
		"BIFROST_THORCHAIN_CHAIN_RPC=" + thornode,
		"BIFROST_THORCHAIN_CHAIN_EBIFROST=127.0.0.1:1",
		"BIFROST_TSS_INFO_ADDRESS=127.0.0.1:0",
		"BIFROST_SIGNER_KEYGEN_TIMEOUT=1s",
		"BIFROST_SIGNER_KEYSIGN_TIMEOUT=1s",
		// a configured start height skips waiting on the THORChain RPC status
		"BIFROST_SIGNER_BLOCK_SCANNER_START_BLOCK_HEIGHT=1",
		// short scanner back-offs let the signer stop promptly
		"BIFROST_SIGNER_BLOCK_SCANNER_BLOCK_HEIGHT_DISCOVER_BACK_OFF=10ms",
		"BIFROST_SIGNER_BLOCK_SCANNER_BLOCK_RETRY_INTERVAL=10ms",
	}
	return mainCovEnv(t.TempDir(), append(env, extra...)...)
}

// thornodeStartupCov is thornodeMainCov whose node account also carries a
// pubkey set, which the signer waits for before it can be created.
func thornodeStartupCov(t *testing.T) string {
	t.Helper()
	// the parent test process has no bech32 prefixes set, so skip NewPubKey
	pk := common.PubKey("tthorpub1addwnpepqv3e9cvwcurmesn2kztc2qcgsmttlp5wqm9kha9rmfuwxzch9nck2fc9vka")
	na, err := json.Marshal(types.NodeAccount{
		Status:    types.NodeStatus_Active,
		PubKeySet: common.PubKeySet{Secp256k1: pk},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, thorclient.NodeAccountEndpoint+"/"):
			_, _ = w.Write(na)
		case r.URL.Path == thorclient.PubKeysEndpoint:
			_, _ = w.Write([]byte(`{"asgard":[],"inactive":[]}`))
		case r.URL.Path == thorclient.StatusEndpoint:
			// served as the CometBFT RPC too: the signer polls sync status
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

// allRPCHostsCov points every chain at an unused local address (without a
// scheme) so main's RPC-host check passes.
func allRPCHostsCov() []string {
	var env []string
	for _, chain := range []string{
		"BTC", "ETH", "BASE", "POL", "TRON", "AVAX", "BSC", "DOGE", "GAIA",
		"NOBLE", "XRP", "XMR", "LTC", "BCH", "SOL", "ZEC", "SUI", "ADA",
	} {
		env = append(env, "BIFROST_CHAINS_"+chain+"_RPC_HOST=127.0.0.1:1")
	}
	return env
}

// runMainBoundedCov is runMainCov with a deadline: a hung main is sent SIGQUIT
// so its goroutine dump lands in the returned output.
func runMainBoundedCov(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...) // #nosec G204 -- re-executes this test binary
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGQUIT) }
	cmd.Env = env
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

func TestMainTssErrorCov(t *testing.T) {
	out, code := runMainCov(t, mainStartupEnvCov(t, "tss-error"))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "fail to create tss instance")
	require.Contains(t, out, "tss unavailable")
}

func TestMainMissingRPCHostCov(t *testing.T) {
	// the default config enables chains without an RPC host
	out, code := runMainCov(t, mainStartupEnvCov(t, "no-chains"))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "TSSCONF keygen=1s keysign=1s monitor=true")
	require.Contains(t, out, "missing chain RPC host")
	require.NotContains(t, out, "LOADCHAINS")
}

func TestMainNoChainsLoadedCov(t *testing.T) {
	out, code := runMainCov(t, mainStartupEnvCov(t, "no-chains", allRPCHostsCov()...))
	require.Equal(t, 1, code, out)
	// main's "http://" prefixing writes to a copy of the map value, so the
	// configuration handed to LoadChains still has the scheme-less host
	require.Contains(t, out, "LOADCHAINS gaia_rpc=127.0.0.1:1 tss=true")
	require.Contains(t, out, "fail to load any chains")
}

func TestMainFullLifecycleCov(t *testing.T) {
	out, code := runMainBoundedCov(t, mainStartupEnvCov(t, "full", allRPCHostsCov()...))
	if raceEnabledCov && code == 66 {
		// Documents a race in main: the health-server goroutine assigns
		// main's shared err variable while main keeps reusing it, so a race
		// build may report it (exit 66) after an otherwise clean shutdown,
		// depending on whether that goroutine runs before the process exits.
		require.Contains(t, out, "WARNING: DATA RACE")
		require.Contains(t, out, "cmd/bifrost.main.func1()")
	} else {
		require.Equal(t, 0, code, out)
	}
	require.Contains(t, out, "LOADCHAINS gaia_rpc=127.0.0.1:1 tss=true")
	require.Contains(t, out, "FAKECHAIN started")
	require.Contains(t, out, "stop signal received")
	require.Contains(t, out, "FAKECHAIN stopped (started=true)")
	require.Contains(t, out, "signer stopped successfully")
	require.Contains(t, out, "The tss and p2p server has been stopped successfully")
}

func TestMainInvalidLogLevelCov(t *testing.T) {
	// Documents current behaviour: the warning claims a fallback to info, but
	// the failed parse leaves zerolog's global level at NoLevel (which hides
	// every leveled message) and go-log then rejects the level, so bifrost
	// exits 1 without ever printing the "fail to set tss-lib loglevel" fatal.
	out, code := runMainCov(t, mainCovEnv(t.TempDir()), "-l", "chatty")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "chatty is not a valid log-level, falling back to 'info'")
	require.NotContains(t, out, "fail to set tss-lib loglevel")
	require.NotContains(t, out, "signer name is empty", "main never got past initLog")
}

func TestSuperviseLegacyKeyshareRecoveryRetriesCov(t *testing.T) {
	old := legacyRecoveryRetryInitial
	legacyRecoveryRetryInitial = time.Millisecond
	t.Cleanup(func() { legacyRecoveryRetryInitial = old })

	// two failed attempts are logged and retried, the third recovers
	b := &bridgeCov{failFor: 2}
	superviseLegacyKeyshareRecovery(context.Background(), config.Bifrost{}, b)
	require.Equal(t, int32(3), b.calls.Load())
}
