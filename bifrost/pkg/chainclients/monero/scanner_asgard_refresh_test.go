package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestRefreshAsgardRegistersOnlyFrostKey validates that refreshAsgardLookup
// calls ensureFrostVaultRegistered with the frost key, not the eddsa key.
//
// The bug: if the eddsa key is registered, the sidecar returns 500 (conflict
// with the existing frost key registration). refreshAsgardLookup returns an
// error. fetchTransfersByHeight previously returned (nil, nil) on this error,
// causing the bifrost to silently advance past the block without scanning it.
// Outputs in that block are lost forever.
//
// This test verifies the registration path uses the correct key by checking
// which vault_pub_key arrives at the sidecar's register endpoint.
func TestRefreshAsgardRegistersOnlyFrostKey(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-scanner-reg")
	eddsaKey := testVaultPubKey(t, "eddsa-scanner-reg")

	var registeredKeys []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			var req xmrFrostRegisterVaultRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			registeredKeys = append(registeredKeys, req.VaultPubKey)

			if req.VaultPubKey == eddsaKey.String() {
				t.Errorf("BUG: eddsa key %s was sent to sidecar register-vault — "+
					"this will cause a 500 conflict and block the scanner", eddsaKey.String())
				http.Error(w, `{"error":"already registered with different mapping"}`, http.StatusInternalServerError)
				return
			}

			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:      testVaultPubKey(t, "ecdsa-scanner-reg"),
				PubKeyEddsa: eddsaKey,
				PubKeyFrost: frostKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(), "frost key registration should succeed")

	assert.Equal(t, 1, len(registeredKeys))
	assert.Equal(t, frostKey.String(), registeredKeys[0],
		"only the frost key should be registered on the sidecar")
}

func TestRefreshAsgardSkipsVaultWithoutPubKeyFrost(t *testing.T) {
	t.Parallel()

	eddsaKey := testVaultPubKey(t, "eddsa-scanner-fallback")

	var registerCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			registerCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:      testVaultPubKey(t, "ecdsa-scanner-fallback"),
				PubKeyEddsa: eddsaKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(), "vaults without frost keys should be ignored, not registered")

	assert.Equal(t, 0, registerCalls, "eddsa-only xmr vaults should not hit sidecar registration")
}
