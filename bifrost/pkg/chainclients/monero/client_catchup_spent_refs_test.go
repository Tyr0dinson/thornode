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

// TestCatchupSpentOutputRefsReadsFromVaultAndCallsSidecar verifies the
// periodic catchup runner reads vault.SpentOutputRefsXmr from thorchain
// state and forwards them to the sidecar via mark-spent. Non-signing nodes
// rely on this path to keep their output state consistent.
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

	assert.Equal(t, frostKey.String(), capturedVault,
		"catchup should call sidecar with the frost key")
	assert.Equal(t, "catchup", capturedTxHash,
		"catchup uses 'catchup' as the tx_hash sentinel")
	assert.True(t, skipVerification,
		"catchup must set skip_verification so the sidecar doesn't try to fetch a real tx")
	require.Equal(t, 3, len(capturedRefs))
	assert.Equal(t, "spent_a", capturedRefs[0].TxHash)
	assert.Equal(t, "spent_b", capturedRefs[1].TxHash)
	assert.Equal(t, "spent_c", capturedRefs[2].TxHash)
}

// TestCatchupSpentOutputRefsSkipsVaultsWithoutFrostOrEddsa verifies that
// vaults without any usable pubkey are skipped (not registered or queried).
func TestCatchupSpentOutputRefsSkipsVaultsWithoutFrostOrEddsa(t *testing.T) {
	t.Parallel()

	var registerCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registerCalls++
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
				"empty": {
					// No PubKeyFrost, no PubKeyEddsa
					SpentOutputRefsXmr: []*common.OutputRef{
						{TxHash: "x", OutputIndex: 0},
					},
				},
			},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, registerCalls,
		"vault without frost or eddsa key should be skipped (no sidecar calls)")
}

// TestCatchupSpentOutputRefsSkipsVaultsWithNoRefs verifies vaults with no
// spent refs don't generate sidecar calls.
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
					// No SpentOutputRefsXmr
				},
			},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, calls, "vault with no spent refs should not generate sidecar calls")
}
