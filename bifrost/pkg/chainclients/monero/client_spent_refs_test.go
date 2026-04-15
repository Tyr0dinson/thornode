package monero

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func testLogger() zerolog.Logger {
	return zerolog.Nop()
}

func TestBuildOutboundObservationIncludesTxKeysOnly(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-spent-refs-obs")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)

	c := &Client{
		chain: common.XMRChain,
	}
	c.vaultAddressMap = map[string]common.Address{
		vaultPubKey.String(): vaultAddr,
	}
	c.vaultAddrToKey = map[string]string{}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyFrost: vaultPubKey,
		Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Memo:             "OUT:inhash",
	}

	txKeys := []string{"11", "22"}
	obs := c.buildOutboundObservation(tx, "txhash123", 0, 5000000, txKeys)
	require.NotNil(t, obs, "observation should not be nil")
	require.Equal(t, txKeys, obs.TxKeys, "observation should carry tx keys")

	assert.Equal(t, vaultPubKey, obs.ObservedVaultPubKey, "observation vault pubkey should be the frost key")
}

func TestBuildOutboundObservationNoTxKeys(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-no-spent-refs")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)

	c := &Client{
		chain: common.XMRChain,
	}
	c.vaultAddressMap = map[string]common.Address{
		vaultPubKey.String(): vaultAddr,
	}
	c.vaultAddrToKey = map[string]string{}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyFrost: vaultPubKey,
		Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Memo:             "OUT:inhash",
	}

	obs := c.buildOutboundObservation(tx, "txhash456", 0, 5000000, nil)
	require.NotNil(t, obs)
	assert.Empty(t, obs.TxKeys)
}

// Test 2c: txVaultPubKey returns frost key when set, else eddsa.
func TestTxVaultPubKeyPreference(t *testing.T) {
	t.Parallel()

	frost := testVaultPubKey(t, "frost-key")
	eddsa := testVaultPubKey(t, "eddsa-key")
	ecdsa := testVaultPubKey(t, "ecdsa-key")

	tx := stypes.TxOutItem{
		VaultPubKey:      ecdsa,
		VaultPubKeyEddsa: eddsa,
		VaultPubKeyFrost: frost,
	}
	assert.Equal(t, frost, txVaultPubKey(tx), "should prefer frost key")

	txNoFrost := stypes.TxOutItem{
		VaultPubKey:      ecdsa,
		VaultPubKeyEddsa: eddsa,
	}
	assert.Equal(t, eddsa, txVaultPubKey(txNoFrost), "should fall back to eddsa")

	txOnlyEcdsa := stypes.TxOutItem{
		VaultPubKey: ecdsa,
	}
	assert.True(t, txVaultPubKey(txOnlyEcdsa).IsEmpty(), "should be empty when only ecdsa set")
}
