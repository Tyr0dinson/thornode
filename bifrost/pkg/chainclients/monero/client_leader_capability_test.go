package monero

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newTestXMRKeyImageWorkStore(t *testing.T) *xmrKeyImageWorkStateStore {
	t.Helper()

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
	})

	store, err := newXMRKeyImageWorkStateStore(db)
	require.NoError(t, err)
	return store
}

func TestRotateXMRKeyImageLeaderEpochUsesThorSigningPeriod(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	client := &Client{
		chain:             common.XMRChain,
		thorchainBridge:   &mockMoneroBridge{constants: map[string]int64{constants.SigningTransactionPeriod.String(): 40}},
		keyImageWorkStore: newTestXMRKeyImageWorkStore(t),
	}

	item, err := client.ensureXMRKeyImageWorkItem(
		vaultPubKey,
		strings.Repeat("a", 64),
		1,
		3_656_801,
		xmrWorkKindFinalizeObservation,
		xmrFinalizePhaseKeyImage,
	)
	require.NoError(t, err)
	require.Equal(t, int64(3_656_801), item.LeaderEpochHeight)

	rotated, err := client.rotateXMRKeyImageLeaderEpoch(item, 20_033)
	require.NoError(t, err)
	require.Equal(t, int64(20_001), rotated.LeaderEpochHeight)

	stored, ok, err := client.keyImageWorkStore.get(rotated.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(20_001), stored.LeaderEpochHeight)
}

func TestXMRLocalCapabilityAllowsStandbySignerForRetiringAndInactiveVaults(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	client := &Client{
		thorchainBridge: &mockMoneroBridge{nodeStatus: ttypes.NodeStatus_Standby},
		localNodePubKey: localNodePubKey,
	}

	members := []string{localNodePubKey.String()}

	retiring, err := client.xmrLocalCapability(members, xmrSyncStateAtTip, ttypes.VaultStatus_RetiringVault)
	require.NoError(t, err)
	require.Equal(t, xmrNodeRoleStandbyVaultSigner, retiring.Role)
	require.True(t, retiring.IsVaultSigner)
	require.True(t, retiring.CanOriginate)
	require.False(t, retiring.CanAttest)

	inactive, err := client.xmrLocalCapability(members, xmrSyncStateAtTip, ttypes.VaultStatus_InactiveVault)
	require.NoError(t, err)
	require.Equal(t, xmrNodeRoleStandbyVaultSigner, inactive.Role)
	require.True(t, inactive.CanOriginate)

	active, err := client.xmrLocalCapability(members, xmrSyncStateAtTip, ttypes.VaultStatus_ActiveVault)
	require.NoError(t, err)
	require.Equal(t, xmrNodeRoleStandbyWatchOnly, active.Role)
	require.False(t, active.CanOriginate)
}
