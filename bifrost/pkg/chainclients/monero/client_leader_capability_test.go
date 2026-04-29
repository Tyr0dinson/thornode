package monero

import (
	"strings"
	"testing"
	"time"

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

func TestRecordXMRKeyImageLeaderFailureSkipsCurrentEpoch(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	client := &Client{
		chain:             common.XMRChain,
		thorchainBridge:   &mockMoneroBridge{constants: map[string]int64{constants.SigningTransactionPeriod.String(): 40}},
		keyImageWorkStore: newTestXMRKeyImageWorkStore(t),
	}

	item, err := client.ensureXMRKeyImageWorkItem(
		vaultPubKey,
		strings.Repeat("b", 64),
		0,
		20_001,
		xmrWorkKindReplayKeyImage,
		xmrFinalizePhaseKeyImage,
	)
	require.NoError(t, err)
	item, err = client.rotateXMRKeyImageLeaderEpoch(item, 20_033)
	require.NoError(t, err)
	require.Equal(t, int64(20_001), item.LeaderEpochHeight)

	item, err = client.recordXMRKeyImageWorkAttempt(item, 20_033, "leader not reachable")
	require.NoError(t, err)
	stored, ok, err := client.keyImageWorkStore.get(item.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(20_041), stored.LeaderEpochHeight)

	rotated, err := client.rotateXMRKeyImageLeaderEpoch(stored, 20_034)
	require.NoError(t, err)
	require.Equal(t, int64(20_041), rotated.LeaderEpochHeight)
}

func TestXMRWorkRetryBackoffRequiresSixtySeconds(t *testing.T) {
	now := time.Unix(1_000, 0)
	item := xmrWorkItem{LastAttemptAt: now.Unix(), AttemptCount: 1}

	ready, wait := xmrWorkItemRetryReady(item, now.Add(59*time.Second))
	require.False(t, ready)
	require.Equal(t, time.Second, wait)

	ready, wait = xmrWorkItemRetryReady(item, now.Add(60*time.Second))
	require.True(t, ready)
	require.Zero(t, wait)
}

func TestXMRKeyImageBacklogOnlyConflictsWithSameVaultSign(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	otherVaultPubKey := common.PubKey("cthorpub1zcjduepqfffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	activeOutput := uint32(0)
	activeWork := &xmrWorkItem{
		ID:          "active",
		VaultPubKey: vaultPubKey.String(),
		TxHash:      strings.Repeat("c", 64),
		OutputIndex: &activeOutput,
	}

	require.True(t, xmrKeyImageBacklogConflictsWithSign(activeWork, vaultPubKey))
	require.False(t, xmrKeyImageBacklogConflictsWithSign(activeWork, otherVaultPubKey))
	require.False(t, xmrKeyImageBacklogConflictsWithSign(nil, vaultPubKey))
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
