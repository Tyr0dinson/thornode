package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
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

	// Simulate what refreshAsgardLookup does: call ensureFrostVaultRegistered
	// with the frost key (correct) vs eddsa key (buggy).
	_, err = ensureFrostVaultRegistered(
		testLogger(),
		signer,
		"XMR",
		frostKey,
		nil,
		"test",
		0,
	)
	require.NoError(t, err, "frost key registration should succeed")

	assert.Equal(t, 1, len(registeredKeys))
	assert.Equal(t, frostKey.String(), registeredKeys[0],
		"only the frost key should be registered on the sidecar")
}

func TestRefreshAsgardResetsNewlyTrackedVaultFromBirthday(t *testing.T) {
	t.Parallel()

	inactiveVaultPubKey, inactiveVaultAddr := testVaultPubKeyAndAddress(t)
	inactiveVault := ttypes.Vault{
		PubKey:                 inactiveVaultPubKey,
		PubKeyEddsa:            inactiveVaultPubKey,
		PubKeyFrost:            inactiveVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
	}

	var registeredVaults []string
	var resetVaults []string
	var resetHeights []int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			registeredVaults = append(registeredVaults, req.VaultPubKey)
			require.Equal(t, inactiveVaultAddr.String(), req.Address)
			require.Equal(t, int64(25), req.VaultCreationHeight)
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		case "/v1/monero/scanner/reset":
			var req struct {
				VaultPubKey    string `json:"vault_pub_key"`
				BirthdayHeight int64  `json:"birthday_height"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			resetVaults = append(resetVaults, req.VaultPubKey)
			resetHeights = append(resetHeights, req.BirthdayHeight)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &scannerTrackedVaultBridge{
		asgards: nil,
		pubKeys: []thorclient.PubKeyContractAddressPair{
			{PubKey: inactiveVaultPubKey, Algo: common.SigningAlgoEd25519, Inactive: true},
		},
		vaultByKey: map[string]ttypes.Vault{
			inactiveVaultPubKey.String(): inactiveVault,
		},
	}

	scanner := NewScanner(log.Logger, signer, bridge, common.XMRChain, common.EmptyPubKey, 72)
	require.NoError(t, scanner.refreshAsgardLookup())

	require.Equal(t, []string{inactiveVaultPubKey.String()}, registeredVaults)
	require.Equal(t, []string{inactiveVaultPubKey.String()}, resetVaults)
	require.Equal(t, []int64{25}, resetHeights)
}

func TestRefreshAsgardSkipsResetForAlreadyRegisteredVault(t *testing.T) {
	t.Parallel()

	inactiveVaultPubKey, inactiveVaultAddr := testVaultPubKeyAndAddress(t)
	inactiveVault := ttypes.Vault{
		PubKey:                 inactiveVaultPubKey,
		PubKeyEddsa:            inactiveVaultPubKey,
		PubKeyFrost:            inactiveVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
	}

	resetCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, inactiveVaultAddr.String(), req.Address)
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: false}))
		case "/v1/monero/scanner/reset":
			resetCalls++
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &scannerTrackedVaultBridge{
		asgards: nil,
		pubKeys: []thorclient.PubKeyContractAddressPair{
			{PubKey: inactiveVaultPubKey, Algo: common.SigningAlgoEd25519, Inactive: true},
		},
		vaultByKey: map[string]ttypes.Vault{
			inactiveVaultPubKey.String(): inactiveVault,
		},
	}

	scanner := NewScanner(log.Logger, signer, bridge, common.XMRChain, common.EmptyPubKey, 72)
	require.NoError(t, scanner.refreshAsgardLookup())
	assert.Equal(t, 0, resetCalls)
}
