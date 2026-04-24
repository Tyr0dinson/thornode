package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestCatchupSpentOutputRefsReadsFromVaultAndCallsSidecar(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-test")

	var mu sync.Mutex
	var capturedVault, capturedTxHash string
	var capturedRefs []sidecar.SpentRef
	var skipVerification bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedTxHash = req.TxHash
			capturedRefs = req.SpentRefs
			skipVerification = req.SkipVerification
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKey:      testVaultPubKey(t, "ecdsa-catchup-test"),
		PubKeyEddsa: testVaultPubKey(t, "eddsa-catchup-test"),
		PubKeyFrost: frostKey,
		SpentOutputRefsXmr: []*common.OutputRef{
			{TxHash: "spent_a", OutputIndex: 0, KeyImage: "ki_a", SpendTxHash: "spend_tx"},
			{TxHash: "spent_b", OutputIndex: 1, KeyImage: "ki_b", SpendTxHash: "spend_tx"},
			{TxHash: "spent_c", OutputIndex: 2, KeyImage: "ki_c", SpendTxHash: "spend_tx"},
		},
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				vault.PubKey.String(): vault,
			},
		},
	}

	c.catchupSpentOutputRefs()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, frostKey.String(), capturedVault)
	assert.Equal(t, "spend_tx", capturedTxHash)
	assert.False(t, skipVerification)
	require.Equal(t, 3, len(capturedRefs))
	assert.Equal(t, "spent_a", capturedRefs[0].TxHash)
	assert.Equal(t, "ki_a", capturedRefs[0].KeyImage)
	assert.Equal(t, "spent_b", capturedRefs[1].TxHash)
	assert.Equal(t, "ki_b", capturedRefs[1].KeyImage)
	assert.Equal(t, "spent_c", capturedRefs[2].TxHash)
	assert.Equal(t, "ki_c", capturedRefs[2].KeyImage)
}

func TestCatchupSpentOutputRefsIncludesInactiveVaultFromPubkeys(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-inactive-catchup")
	primaryKey := testVaultPubKey(t, "ecdsa-inactive-catchup")

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			calls++
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKey:      primaryKey,
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_InactiveVault,
		SpentOutputRefsXmr: []*common.OutputRef{
			{TxHash: "inactive_spent", OutputIndex: 0, KeyImage: "ki_inactive", SpendTxHash: "inactive_spend_tx"},
		},
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				primaryKey.String(): vault,
			},
			pubKeys: []thorclient.PubKeyContractAddressPair{
				{PubKey: primaryKey, Algo: common.SigningAlgoSecp256k1, Inactive: true},
			},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 1, calls)
}

func TestCatchupSpentOutputRefsSkipsVaultsWithoutFrost(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 0})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				"eddsa-only": {
					PubKeyEddsa: testVaultPubKey(t, "eddsa-only-catchup"),
					SpentOutputRefsXmr: []*common.OutputRef{
						{TxHash: "x", OutputIndex: 0, KeyImage: "ki_x", SpendTxHash: "spend_tx"},
					},
				},
			},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, calls)
}

func TestCatchupSpentOutputRefsSkipsVaultsWithNoRefs(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 0})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				"v1": {
					PubKeyFrost: testVaultPubKey(t, "frost-norefs"),
				},
			},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, calls)
}
