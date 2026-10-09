package monero

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
)

// fullSidecarClientCov is a loopback signer which satisfies NewClient's
// compatibility handshake and serves the handful of read endpoints the
// simple Client accessors use.
type fullSidecarClientCov struct {
	mu          sync.Mutex
	height      int64
	balance     sidecar.Balance
	balanceErr  bool
	exportHex   string
	exportErr   bool
	registered  int32
	clearStatus int
}

func (f *fullSidecarClientCov) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/info":
			_ = json.NewEncoder(w).Encode(failClosedSignerInfo())
		case r.URL.Path == "/v1/monero/sessions/clear":
			if f.clearStatus != 0 {
				http.Error(w, "nope", f.clearStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cleared_sessions": 2, "status": "ok"})
		case r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: f.height})
		case r.URL.Path == "/v1/monero/vaults/register":
			atomic.AddInt32(&f.registered, 1)
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(1))
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance"):
			if f.balanceErr {
				http.Error(w, "balance down", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(f.balance)
		case strings.HasPrefix(r.URL.Path, "/v1/monero/keys/") && strings.HasSuffix(r.URL.Path, "/export"):
			if f.exportErr {
				http.Error(w, "export down", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"key_bytes_hex": f.exportHex})
		default:
			http.Error(w, "unexpected signer call: "+r.URL.Path, http.StatusNotFound)
		}
	}
}

type heightBridgeClientCov struct {
	thorclient.ThorchainBridge
}

func newThorKeysClientCov(t *testing.T) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	cryptocodec.RegisterInterfaces(cdc.InterfaceRegistry())
	kb := cKeys.NewInMemory(cdc, func(options *cKeys.Options) {
		options.SupportedAlgos = append(options.SupportedAlgos, ed25519.Ed25519)
	})
	_, mnemonic, err := kb.NewMnemonic(ed25519.SignerNameEDDSA("cov-client"), cKeys.English, "", "password", ed25519.Ed25519)
	require.NoError(t, err)
	_, err = kb.NewAccount("cov-client", mnemonic, "password", cmd.THORChainHDPath, hd.Secp256k1)
	require.NoError(t, err)
	return thorclient.NewKeysWithKeybase(kb, "cov-client", "password")
}

func newFullClientCov(t *testing.T, fake *fullSidecarClientCov, keys *thorclient.Keys) *Client {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	cfg := failClosedChainConfig(server.URL, failClosedSignerNetwork)
	cfg.MinConfirmations = 3
	chainClient, err := NewClient(keys, cfg, &tssp.TssServer{}, &heightBridgeClientCov{}, failClosedTestMetrics(t))
	require.NoError(t, err)
	client, ok := chainClient.(*Client)
	require.True(t, ok)
	return client
}

func TestNewClientArgumentValidationClientCov(t *testing.T) {
	cfg := failClosedChainConfig("http://127.0.0.1:1", failClosedSignerNetwork)
	m := failClosedTestMetrics(t)

	bad := cfg
	bad.ChainID = common.BTCChain
	_, err := NewClient(nil, bad, &tssp.TssServer{}, &failClosedBridge{}, m)
	require.ErrorContains(t, err, "requires chain id")

	bad = cfg
	bad.BlockScanner.PrefetchBlocks = 2
	_, err = NewClient(nil, bad, &tssp.TssServer{}, &failClosedBridge{}, m)
	require.ErrorContains(t, err, "prefetch must be 0 or 1")

	_, err = NewClient(nil, cfg, &tssp.TssServer{}, nil, m)
	require.ErrorContains(t, err, "bridge is nil")
	_, err = NewClient(nil, cfg, &tssp.TssServer{}, &failClosedBridge{}, nil)
	require.ErrorContains(t, err, "metrics instance is nil")
	_, err = NewClient(nil, cfg, nil, &failClosedBridge{}, m)
	require.ErrorContains(t, err, "tss server is nil")

	bad = cfg
	bad.ChainNetwork = " "
	_, err = NewClient(nil, bad, &tssp.TssServer{}, &failClosedBridge{}, m)
	require.ErrorContains(t, err, "chain_network is required")
}

func TestNewClientWithKeysAndAccessorsClientCov(t *testing.T) {
	balance := uint64(5 * moneroAtomicToThorchain)
	pending := 0
	fake := &fullSidecarClientCov{
		height:      1234,
		exportHex:   "deadbeef",
		clearStatus: 0,
		balance: sidecar.Balance{
			Balance: balance, UnlockedBalance: balance,
			ReconciliationState: "reconciled", PendingOutgoingCount: &pending,
		},
	}
	keys := newThorKeysClientCov(t)
	client := newFullClientCov(t, fake, keys)

	// Local node pubkey and the ed25519 bootstrap were derived from the keys.
	require.False(t, client.localNodePubKey.IsEmpty())
	require.NotNil(t, client.vaultBootstrap)
	require.True(t, client.CustomKeygen())
	require.Equal(t, common.XMRChain, client.GetConfig().ChainID)
	require.Equal(t, uint64(3), client.GetConfig().MinConfirmations)

	height, err := client.GetHeight()
	require.NoError(t, err)
	require.Equal(t, int64(1234), height)

	scannerHeight, err := client.GetBlockScannerHeight()
	require.NoError(t, err)
	require.Equal(t, client.blockScanner.PreviousHeight(), scannerHeight)

	vault, address := testVaultPubKeyAndAddress(t)
	require.Equal(t, address.String(), client.GetAddress(vault))
	require.Empty(t, client.GetAddress(common.PubKey("bogus")))

	// An address with no known vault mapping is refused.
	_, err = client.GetAccount(vault, big.NewInt(1))
	require.ErrorContains(t, err, "no vault pub key found")
	client.setVaultAddressMapping(vault, address)
	account, err := client.GetAccount(vault, big.NewInt(1))
	require.NoError(t, err)
	require.Equal(t, cosmos.NewUint(5), account.Coins.GetCoin(common.XMRAsset).Amount)
	require.GreaterOrEqual(t, atomic.LoadInt32(&fake.registered), int32(1))

	_, err = client.GetAccount(common.PubKey("bogus"), nil)
	require.ErrorContains(t, err, "fail to get monero address from pubkey")

	// Solvency account queries require a reconciled balance.
	fake.mu.Lock()
	fake.balance.ReconciliationState = "pending"
	fake.mu.Unlock()
	_, err = client.getSolvencyAccountByAddress(address.String())
	require.ErrorIs(t, err, errXMRBalanceUnreconciled)
	quarantined := 1
	fake.mu.Lock()
	fake.balance.ReconciliationState = "reconciled"
	fake.balance.QuarantinedOutgoingCount = &quarantined
	fake.mu.Unlock()
	account, err = client.getSolvencyAccountByAddress(address.String())
	require.NoError(t, err)
	require.Equal(t, cosmos.NewUint(5), account.Coins.GetCoin(common.XMRAsset).Amount)

	fake.mu.Lock()
	fake.balanceErr = true
	fake.mu.Unlock()
	_, err = client.GetAccountByAddress(address.String(), nil)
	require.ErrorContains(t, err, "fail to get monero wallet balance")

	_, err = client.GetAccountByAddress(" ", nil)
	require.ErrorContains(t, err, "requires vault address")
	_, err = client.GetAccountByAddress("unknown-address", nil)
	require.ErrorContains(t, err, "no vault pub key found")

	// Keyshare export.
	blob, err := client.ExportKeyshare(vault)
	require.NoError(t, err)
	require.Equal(t, []byte("deadbeef"), blob)
	fake.mu.Lock()
	fake.exportErr = true
	fake.mu.Unlock()
	_, err = client.ExportKeyshare(vault)
	require.ErrorContains(t, err, "fail to export frost keys")

	// Latest tx for vault from the signer cache.
	_, _, err = client.GetLatestTxForVault(vault.String())
	require.Error(t, err, "nothing recorded yet")
	require.NoError(t, client.signerCacheManager.SetSigned("item-hash",
		stypes.InboundCacheKey(vault.String(), common.XMRChain.String()), "in-tx"))
	require.NoError(t, client.signerCacheManager.SetSigned("item-hash-2",
		stypes.BroadcastCacheKey(vault.String(), common.XMRChain.String()), "out-tx"))
	in, out, err := client.GetLatestTxForVault(vault.String())
	require.NoError(t, err)
	require.Equal(t, "in-tx", in)
	require.Equal(t, "out-tx", out)

	// Confirmation readiness.
	require.True(t, client.ConfirmationCountReady(stypes.TxIn{}))
	require.True(t, client.ConfirmationCountReady(stypes.TxIn{MemPool: true, TxArray: []*stypes.TxInItem{{}}}))
	client.scanner.setCurrentHeight(110)
	ready := stypes.TxIn{TxArray: []*stypes.TxInItem{{BlockHeight: 100}}, ConfirmationRequired: 10}
	require.True(t, client.ConfirmationCountReady(ready))
	ready.ConfirmationRequired = 11
	require.False(t, client.ConfirmationCountReady(ready))

	// Vault lock key uses the FROST vault pubkey.
	require.Equal(t, vault.String(), client.GetVaultLockKey(stypes.TxOutItem{VaultPubKeyFrost: vault}))
	require.Empty(t, client.GetVaultLockKey(stypes.TxOutItem{}))

	// Runners exit promptly once stopped.
	close(client.stopchan)
	client.wg.Add(1)
	client.consolidationRunner()
	client.stopchan = make(chan struct{})
	client.Stop()
}

func TestNewClientClearSessionsFailureClientCov(t *testing.T) {
	fake := &fullSidecarClientCov{clearStatus: http.StatusInternalServerError}
	client := newFullClientCov(t, fake, nil)
	require.True(t, client.localNodePubKey.IsEmpty())
	require.Nil(t, client.vaultBootstrap)

	// Without a scanner the height query fails closed.
	bare := &Client{}
	_, err := bare.GetHeight()
	require.ErrorContains(t, err, "scanner is not initialized")
	_, err = bare.ExportKeyshare(common.EmptyPubKey)
	require.ErrorContains(t, err, "sidecar not configured")
	_, err = bare.getAccountByAddress("x", nil, false)
	require.ErrorContains(t, err, "requires sidecar")
}
