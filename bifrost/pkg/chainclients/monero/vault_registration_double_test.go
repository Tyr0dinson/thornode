package monero

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestResolveVaultAddressOnlyMapsFrostKey verifies that resolveVaultAddress
// only stores the frost key in the address map, not the eddsa key. If the
// eddsa key leaks into the map, GetAccountByAddress will call
// ensureFrostVaultRegistration with the eddsa key, causing the sidecar to
// return 500 (conflicting registration).
func TestResolveVaultAddressOnlyMapsFrostKey(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-resolve-addr")
	eddsaKey := testVaultPubKey(t, "eddsa-key-resolve-addr")
	ecdsaKey := testVaultPubKey(t, "ecdsa-key-resolve-addr")

	c := &Client{
		chain:           common.XMRChain,
		logger:          testLogger(),
		vaultAddressMap: make(map[string]common.Address),
		vaultAddrToKey:  make(map[string]string),
	}

	vault := ttypes.Vault{
		PubKey:      ecdsaKey,
		PubKeyEddsa: eddsaKey,
		PubKeyFrost: frostKey,
	}

	addr := c.resolveVaultAddress(vault)
	require.False(t, addr.IsEmpty(), "resolveVaultAddress should return a valid address")

	// The frost key should be in the address map.
	_, frostMapped := c.vaultAddressMap[frostKey.String()]
	assert.True(t, frostMapped, "frost key should be mapped to the vault address")

	// The eddsa key must NOT be in the address map.
	_, eddsaMapped := c.vaultAddressMap[eddsaKey.String()]
	assert.False(t, eddsaMapped,
		"BUG: eddsa key was stored in address map — this causes double registration on the sidecar")

	// The reverse lookup must point to the frost key, not eddsa.
	pubKey, found := c.findVaultPubKeyByAddress(addr.String())
	require.True(t, found)
	assert.Equal(t, frostKey, pubKey,
		"findVaultPubKeyByAddress should return the frost key, not eddsa")
}

// TestTxVaultPubKeyNeverReturnsEddsa verifies that txVaultPubKey returns the
// frost key when VaultPubKeyFrost is set, preventing eddsa-keyed sidecar calls.
func TestTxVaultPubKeyNeverReturnsEddsa(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-for-txvault")
	eddsaKey := testVaultPubKey(t, "eddsa-for-txvault")
	ecdsaKey := testVaultPubKey(t, "ecdsa-for-txvault")

	// When all keys are set, frost must be returned.
	tx := stypes.TxOutItem{
		VaultPubKey:      ecdsaKey,
		VaultPubKeyEddsa: eddsaKey,
		VaultPubKeyFrost: frostKey,
	}
	result := txVaultPubKey(tx)
	assert.Equal(t, frostKey, result, "must return frost key, not eddsa")
	assert.NotEqual(t, eddsaKey, result, "must NEVER return eddsa when frost is set")
}
