package chainclients

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// pubKeyValidatorCov wraps a real PubKeyManager and counts registered callbacks.
type pubKeyValidatorCov struct {
	*pubkeymanager.PubKeyManager
	callbacks atomic.Int32
}

func (p *pubKeyValidatorCov) RegisterCallback(cb pubkeymanager.OnNewPubKey) {
	p.callbacks.Add(1)
	p.PubKeyManager.RegisterCallback(cb)
}

// metrics register prometheus collectors globally, so build them only once
var (
	metricsOnceCov sync.Once
	metricsCov     *metrics.Metrics
	metricsErrCov  error
)

func newKeysCov(t *testing.T) *thorclient.Keys {
	t.Helper()
	ttypes.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic("bob", cKeys.English, cmd.THORChainHDPath, "password", hd.Secp256k1)
	require.NoError(t, err)
	return thorclient.NewKeysWithKeybase(kb, "bob", "password")
}

func readFixtureCov(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// newFakeNodeCov serves a minimal UTXO JSON-RPC daemon on "/" and the
// thornode endpoints the bridge needs on every other path.
func newFakeNodeCov(t *testing.T) *httptest.Server {
	t.Helper()
	rpcs := map[string]map[string]any{
		"getnetworkinfo": readFixtureCov(t, "../../../test/fixtures/doge/getnetworkinfo.json"),
		"importaddress":  readFixtureCov(t, "../../../test/fixtures/doge/importaddress.json"),
		"getblockcount":  readFixtureCov(t, "../../../test/fixtures/doge/blockcount.json"),
	}
	lastblock, err := os.ReadFile("../../../test/fixtures/endpoints/lastblock/doge.json")
	require.NoError(t, err)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.RequestURI == "/":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Method string `json:"method"`
				ID     any    `json:"id"`
			}
			_ = json.Unmarshal(body, &req)
			resp, ok := rpcs[req.Method]
			if !ok {
				resp = map[string]any{"result": nil, "error": nil}
			}
			out := map[string]any{}
			for k, v := range resp {
				out[k] = v
			}
			out["id"] = req.ID
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write(lastblock)
		case strings.HasPrefix(r.RequestURI, "/thorchain/mimir"):
			_, _ = w.Write([]byte("-1"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLoadChainsCov(t *testing.T) {
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
	m := metricsCov

	srv := newFakeNodeCov(t)
	defer srv.Close()

	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       srv.Listener.Addr().String(),
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: t.TempDir(),
	}, m, keys)
	require.NoError(t, err)

	pkm, err := pubkeymanager.NewPubKeyManager(bridge, m)
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
			ChainID:            common.DOGEChain,
			StartBlockHeight:   1,
			GasPriceResolution: 500_000,
		},
	}
	doge.UTXO.TransactionBatchSize = 100
	doge.UTXO.MaxMempoolBatches = 10
	doge.BlockScanner.MaxReorgRescanBlocks = 1

	// every other chain type points at a closed port. Constructors that need
	// a pool manager or TSS server fail (and are skipped in the result); the
	// others construct lazily and load anyway.
	badHost := "http://127.0.0.1:1"
	bad := func(chain common.Chain) config.BifrostChainConfiguration {
		return config.BifrostChainConfiguration{
			ChainID: chain,
			RPCHost: badHost,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            chain,
				StartBlockHeight:   1,
				HTTPRequestTimeout: 50 * time.Millisecond,
			},
		}
	}

	cfg := map[common.Chain]config.BifrostChainConfiguration{
		common.DOGEChain: doge,
		common.ETHChain:  bad(common.ETHChain),
		common.AVAXChain: bad(common.AVAXChain),
		common.GAIAChain: bad(common.GAIAChain),
		common.TRONChain: bad(common.TRONChain),
		common.SOLChain:  bad(common.SOLChain),
		common.XRPChain:  bad(common.XRPChain),
		common.XMRChain:  bad(common.XMRChain),
		common.ZECChain:  bad(common.ZECChain),
		// disabled chains are skipped entirely
		common.BTCChain: {ChainID: common.BTCChain, Disabled: true},
	}

	chains, restart := LoadChains(keys, cfg, nil, bridge, m, pkv, nil)
	require.NotNil(t, restart)

	// DOGE loaded, is a utxo client, and registered its pubkey callback
	require.Contains(t, chains, common.DOGEChain)
	_, ok := chains[common.DOGEChain].(*utxo.Client)
	require.True(t, ok)
	require.Equal(t, int32(1), pkv.callbacks.Load())

	// disabled chain is never loaded
	require.NotContains(t, chains, common.BTCChain)

	// constructors that do not dial at construction time still load
	for _, chain := range []common.Chain{common.GAIAChain, common.TRONChain, common.XRPChain, common.ZECChain} {
		require.Contains(t, chains, chain, chain.String())
	}
	// ZEC is a utxo client but does not register a pubkey callback
	_, ok = chains[common.ZECChain].(*utxo.Client)
	require.True(t, ok)

	// failing chains (nil pool manager / nil tss server) are not returned
	for _, chain := range []common.Chain{common.ETHChain, common.AVAXChain, common.SOLChain, common.XMRChain} {
		require.NotContains(t, chains, chain, chain.String())
	}
	require.Len(t, chains, 5)

	// the restart channel stays open while no failed chain recovers
	select {
	case <-restart:
		t.Fatal("restart channel unexpectedly closed")
	default:
	}
}
