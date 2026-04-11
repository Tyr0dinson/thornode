package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestCatchupSpentOutputRefsReadsFromVaultAndCallsSidecar(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-test")

	var mu sync.Mutex
	var capturedVault, capturedTxHash string
	var capturedRefs []xmrFrostOutputRef
	var skipVerification bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			var req xmrFrostMarkSpentRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedTxHash = req.TxHash
			capturedRefs = req.SpentRefs
			skipVerification = req.SkipVerification
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostMarkSpentResponse{Marked: len(req.SpentRefs)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKey:      testVaultPubKey(t, "ecdsa-catchup-test"),
		PubKeyEddsa: testVaultPubKey(t, "eddsa-catchup-test"),
		PubKeyFrost: frostKey,
		SpentOutputRefsXmr: []*common.OutputRef{
			{TxHash: "spent_a", OutputIndex: 0},
			{TxHash: "spent_b", OutputIndex: 1},
			{TxHash: "spent_c", OutputIndex: 2},
		},
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
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
	assert.Equal(t, "catchup", capturedTxHash)
	assert.True(t, skipVerification)
	require.Equal(t, 3, len(capturedRefs))
	assert.Equal(t, "spent_a", capturedRefs[0].TxHash)
	assert.Equal(t, "spent_b", capturedRefs[1].TxHash)
	assert.Equal(t, "spent_c", capturedRefs[2].TxHash)
}

func TestCatchupSpentOutputRefsSkipsVaultsWithoutFrost(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(xmrFrostMarkSpentResponse{Marked: 0})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				"eddsa-only": {
					PubKeyEddsa: testVaultPubKey(t, "eddsa-only-catchup"),
					SpentOutputRefsXmr: []*common.OutputRef{
						{TxHash: "x", OutputIndex: 0},
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
		_ = json.NewEncoder(w).Encode(xmrFrostMarkSpentResponse{Marked: 0})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
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
