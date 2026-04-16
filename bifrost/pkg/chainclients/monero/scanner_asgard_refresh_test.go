package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestEnsureFrostVaultRegisteredUsesProvidedVaultKey(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-scanner-reg")
	eddsaKey := testVaultPubKey(t, "eddsa-scanner-reg")

	var registeredKeys []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		var req xmrFrostRegisterVaultRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		registeredKeys = append(registeredKeys, req.VaultPubKey)

		if req.VaultPubKey == eddsaKey.String() {
			t.Fatalf("unexpected eddsa key registration: %s", eddsaKey.String())
		}

		_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	_, err = ensureFrostVaultRegistered(
		testLogger(),
		signer,
		common.XMRChain,
		frostKey,
		nil,
		"test",
		0,
	)
	require.NoError(t, err)

	require.Equal(t, []string{frostKey.String()}, registeredKeys)
}

func TestRefreshAsgardLookupTracksDerivedFrostVaultAddresses(t *testing.T) {
	t.Parallel()

	currentVaultPubKey, currentVaultAddr := testVaultPubKeyAndAddress(t)
	inactiveVaultPubKey, inactiveVaultAddr := testVaultPubKeyAndAddress(t)

	currentVault := ttypes.Vault{
		PubKey:                 currentVaultPubKey,
		PubKeyEddsa:            currentVaultPubKey,
		PubKeyFrost:            currentVaultPubKey,
		Status:                 ttypes.VaultStatus_ActiveVault,
		VaultCreationHeightXmr: 50,
	}
	inactiveVault := ttypes.Vault{
		PubKey:                 inactiveVaultPubKey,
		PubKeyEddsa:            inactiveVaultPubKey,
		PubKeyFrost:            inactiveVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
	}

	bridge := &scannerTrackedVaultBridge{
		asgards: ttypes.Vaults{currentVault},
		pubKeys: scannerTrackedVaultPubKeys(currentVaultPubKey, inactiveVaultPubKey),
		vaultByKey: map[string]ttypes.Vault{
			currentVaultPubKey.String():  currentVault,
			inactiveVaultPubKey.String(): inactiveVault,
		},
	}

	scanner := NewScanner(log.Logger, nil, bridge, common.XMRChain, common.EmptyPubKey, 72)
	require.NoError(t, scanner.refreshAsgardLookup())

	currentMapped, ok := scanner.getVaultForAddress(currentVaultAddr.String())
	require.True(t, ok)
	assert.Equal(t, currentVaultPubKey, currentMapped)

	inactiveMapped, ok := scanner.getVaultForAddress(inactiveVaultAddr.String())
	require.True(t, ok)
	assert.Equal(t, inactiveVaultPubKey, inactiveMapped)
}

func TestMapVaultsReturnsDeterministicOrder(t *testing.T) {
	t.Parallel()

	vaultA, _ := testVaultPubKeyAndAddress(t)
	vaultB, _ := testVaultPubKeyAndAddress(t)
	vaultC, _ := testVaultPubKeyAndAddress(t)

	lookup := map[string]ttypes.Vault{
		vaultB.String(): {PubKeyFrost: vaultB},
		vaultC.String(): {PubKeyFrost: vaultC},
		vaultA.String(): {PubKeyFrost: vaultA},
	}

	vaults := mapVaults(lookup)
	require.Len(t, vaults, 3)

	got := []string{
		vaults[0].PubKeyFrost.String(),
		vaults[1].PubKeyFrost.String(),
		vaults[2].PubKeyFrost.String(),
	}
	want := append([]string(nil), vaultA.String(), vaultB.String(), vaultC.String())
	sort.Strings(want)

	assert.Equal(t, want, got)
}

func scannerTrackedVaultPubKeys(keys ...common.PubKey) []thorclient.PubKeyContractAddressPair {
	pairs := make([]thorclient.PubKeyContractAddressPair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, thorclient.PubKeyContractAddressPair{
			PubKey:   key,
			Algo:     common.SigningAlgoEd25519,
			Inactive: false,
		})
	}
	if len(pairs) > 1 {
		pairs[1].Inactive = true
	}
	return pairs
}
