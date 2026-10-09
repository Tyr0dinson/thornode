package monero

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type errConsolidationBridgeClientCov struct {
	*consolidationBridge
	heightErr error
	statusErr error
	asgardErr error
	mimirErr  error
	constErr  error
}

func (b *errConsolidationBridgeClientCov) GetBlockHeight() (int64, error) {
	if b.heightErr != nil {
		return 0, b.heightErr
	}
	return b.consolidationBridge.GetBlockHeight()
}

func (b *errConsolidationBridgeClientCov) FetchNodeStatus() (ttypes.NodeStatus, error) {
	if b.statusErr != nil {
		return ttypes.NodeStatus_Unknown, b.statusErr
	}
	return b.consolidationBridge.FetchNodeStatus()
}

func (b *errConsolidationBridgeClientCov) GetAsgards() (ttypes.Vaults, error) {
	if b.asgardErr != nil {
		return nil, b.asgardErr
	}
	return b.consolidationBridge.GetAsgards()
}

func (b *errConsolidationBridgeClientCov) GetMimir(key string) (int64, error) {
	if b.mimirErr != nil {
		return 0, b.mimirErr
	}
	return b.consolidationBridge.GetMimir(key)
}

func (b *errConsolidationBridgeClientCov) GetConstants() (map[string]int64, error) {
	if b.constErr != nil {
		return nil, b.constErr
	}
	return b.consolidationBridge.GetConstants()
}

type consolidationFixtureClientCov struct {
	client  *Client
	bridge  *errConsolidationBridgeClientCov
	tss     *consolidationTSS
	sidecar *consolidationSidecar
	vault   ttypes.Vault
	local   common.PubKey
}

func newConsolidationFixtureClientCov(t *testing.T) *consolidationFixtureClientCov {
	t.Helper()
	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}
	base := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants:   map[string]int64{constants.ChurnInterval.String(): 1000},
	}
	bridge := &errConsolidationBridgeClientCov{consolidationBridge: base}
	tss := &consolidationTSS{}
	ss := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {
				Balance: 10 * 1_000_000_000_000, UnlockedBalance: 10 * 1_000_000_000_000,
				SpendableBalance: uint64Ptr(10 * 1_000_000_000_000),
			},
		},
		outputsByVault: map[string][]sidecar.Output{vaultPubKey.String(): unlockedOutputs(1, 1)},
		feeRate:        20,
	}
	client := newConsolidationClient(t, base, tss, ss, localNodePubKey, vaultPubKey, vaultAddr)
	client.thorchainBridge = bridge
	return &consolidationFixtureClientCov{client: client, bridge: bridge, tss: tss, sidecar: ss, vault: vault, local: localNodePubKey}
}

func TestRunConsolidationPassSkipsClientCov(t *testing.T) {
	t.Parallel()

	// Unconfigured clients are no-ops.
	require.NoError(t, (&Client{}).runConsolidationPass())

	f := newConsolidationFixtureClientCov(t)
	c := f.client

	// Missing local node key.
	saved := c.localNodePubKey
	c.localNodePubKey = common.EmptyPubKey
	require.NoError(t, c.runConsolidationPass())
	c.localNodePubKey = saved

	f.bridge.heightErr = errors.New("height down")
	require.ErrorContains(t, c.runConsolidationPass(), "fail to get thorchain block height")
	f.bridge.heightErr = nil

	f.bridge.blockHeight = 0
	require.NoError(t, c.runConsolidationPass())
	require.Equal(t, int64(0), c.lastConsolidationHeight.Load())

	f.bridge.blockHeight = 21
	f.bridge.statusErr = errors.New("status down")
	require.ErrorContains(t, c.runConsolidationPass(), "fail to get node status")
	require.Equal(t, int64(21), c.lastConsolidationHeight.Load())
	// Same height is skipped.
	require.NoError(t, c.runConsolidationPass())
	f.bridge.statusErr = nil

	f.bridge.blockHeight = 22
	f.bridge.nodeStatus = ttypes.NodeStatus_Standby
	require.NoError(t, c.runConsolidationPass())
	f.bridge.nodeStatus = ttypes.NodeStatus_Active

	f.bridge.blockHeight = 23
	f.bridge.asgardErr = errors.New("asgard down")
	require.ErrorContains(t, c.runConsolidationPass(), "fail to get asgards")
	f.bridge.asgardErr = nil

	// No vaults.
	f.bridge.blockHeight = 24
	f.bridge.asgards = nil
	require.NoError(t, c.runConsolidationPass())

	// Vault without the local node, without XMR, near churn, retiring, and not periodic.
	foreign := f.vault
	foreign.Membership = []string{"someone-else"}
	noXMR := f.vault
	noXMR.Chains = []string{common.BTCChain.String()}
	retiring := f.vault
	retiring.Status = ttypes.VaultStatus_RetiringVault
	f.bridge.asgards = ttypes.Vaults{foreign, noXMR, retiring}
	f.bridge.blockHeight = 30
	require.NoError(t, c.runConsolidationPass())

	f.bridge.asgards = ttypes.Vaults{f.vault}
	f.bridge.blockHeight = 31 // not a multiple of the check interval (10)
	require.NoError(t, c.runConsolidationPass())

	// Near churn: StatusSince + churn interval close to height.
	f.bridge.constants = map[string]int64{constants.ChurnInterval.String(): 15}
	f.bridge.blockHeight = 40
	require.NoError(t, c.runConsolidationPass())
	f.bridge.constErr = errors.New("constants down")
	require.Positive(t, c.getChurnInterval(), "falls back to the default churn interval")

	// Disabled by mimir.
	f.bridge.mimir = map[string]int64{mimirXMRConsolidationEnabled: 0}
	c.consolidationSettingsMu.Lock()
	c.hasConsolidationConfig = false
	c.consolidationSettingsMu.Unlock()
	f.bridge.blockHeight = 50
	require.NoError(t, c.runConsolidationPass())

	require.Equal(t, 0, f.tss.callCount(), "no pass reached FROST")
	require.Equal(t, 0, f.sidecar.broadcasts())

	// tryConsolidationPass is a no-op while another pass is in progress.
	c.consolidateInProgress.Store(true)
	f.bridge.heightErr = errors.New("must not be called")
	c.tryConsolidationPass()
	require.True(t, c.consolidateInProgress.Load())
	c.consolidateInProgress.Store(false)
	c.tryConsolidationPass()
	require.False(t, c.consolidateInProgress.Load())
}

func TestLoadConsolidationSettingsClampsClientCov(t *testing.T) {
	t.Parallel()

	f := newConsolidationFixtureClientCov(t)
	c := f.client
	f.bridge.mimir = map[string]int64{
		mimirXMRConsolidationMaxInputs:  100000,
		mimirXMRConsolidationMaxLockBps: 50000,
	}
	settings := c.loadConsolidationSettings()
	require.Equal(t, int64(common.MaxXMRSpentOutputRefs), settings.MaxInputs)
	require.Equal(t, int64(10000), settings.MaxLockBps)

	// Mimir errors fall back to the configured values.
	f.bridge.mimirErr = errors.New("mimir down")
	c.consolidationSettingsMu.Lock()
	c.hasConsolidationConfig = false
	c.consolidationSettingsMu.Unlock()
	settings = c.loadConsolidationSettings()
	require.True(t, settings.Enabled)
	require.Equal(t, int64(8), settings.MaxInputs)
	require.Equal(t, int64(1000), settings.MaxLockBps)
	_, ok := c.mimirInt64Raw("anything")
	require.False(t, ok)
	_, ok = (&Client{}).mimirInt64Raw("anything")
	require.False(t, ok)
}

func TestConsolidateVaultSkipsClientCov(t *testing.T) {
	t.Parallel()

	// Start from the Client field's zero value rather than naming the
	// consolidationSettings type: a later reference to the type in this package
	// makes govet's shadow check flag the local of that name in client.go.
	settings := (&Client{}).consolidationSettings
	settings.Enabled = true
	settings.CheckIntervalBlocks, settings.CooldownBlocks = 10, 5
	settings.MinUnlockedOutputs, settings.TargetOutputs, settings.MinUnlockedAtomic = 2, 1, 1
	settings.ReserveBps, settings.ChurnLookahead, settings.MaxRetries = 50, 1, 1
	settings.MaxInputs, settings.MaxLockBps = 8, 1000

	t.Run("empty-vault", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.client.consolidateVault(ttypes.Vault{}, 100, settings, true)
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("cooldown", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		key := f.vault.PubKeyFrost.String()
		f.client.setConsolidationVaultState(key, consolidationVaultState{LastAttemptHeight: 98, ConsecutiveFails: 1})
		f.client.consolidateVault(f.vault, 100, settings, true)
		require.Equal(t, int64(98), f.client.getConsolidationVaultState(key).LastAttemptHeight)
	})
	t.Run("bad-vault-address", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		vault := f.vault
		vault.PubKeyFrost = common.PubKey("bogus")
		f.client.consolidateVault(vault, 100, settings, true)
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("mempool-outgoing", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.sidecar.mempool = []sidecar.Transfer{{VaultPubKey: f.vault.PubKeyFrost.String()}}
		f.client.consolidateVault(f.vault, 100, settings, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
		// Matching by sender address also counts.
		addr := f.client.vaultAddressMap[f.vault.PubKeyFrost.String()]
		f.sidecar.mempool = []sidecar.Transfer{{FromAddress: addr.String()}}
		pending, err := f.client.hasMempoolOutgoingForVault(f.vault.PubKeyFrost, addr.String())
		require.NoError(t, err)
		require.True(t, pending)
	})
	t.Run("balance-missing", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.sidecar.balanceByVault = map[string]sidecar.Balance{}
		f.client.consolidateVault(f.vault, 100, settings, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("balance-below-threshold", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		s := settings
		s.MinUnlockedAtomic = ^uint64(0)
		f.client.consolidateVault(f.vault, 100, s, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("too-few-outputs", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.client.consolidateVault(f.vault, 100, settings, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("at-target", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.sidecar.outputsByVault[f.vault.PubKeyFrost.String()] = unlockedOutputs(1, 3)
		s := settings
		s.TargetOutputs = 3
		f.client.consolidateVault(f.vault, 100, s, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("zero-lock-budget", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.sidecar.outputsByVault[f.vault.PubKeyFrost.String()] = unlockedOutputs(1, 3)
		f.sidecar.balanceByVault[f.vault.PubKeyFrost.String()] = sidecar.Balance{Balance: 1, UnlockedBalance: 1}
		f.client.consolidateVault(f.vault, 100, settings, true)
		require.Equal(t, 0, f.tss.callCount())
		require.Empty(t, f.client.consolidationState)
	})
	t.Run("reserve-exceeds-spendable", func(t *testing.T) {
		f := newConsolidationFixtureClientCov(t)
		f.sidecar.outputsByVault[f.vault.PubKeyFrost.String()] = unlockedOutputs(1, 3)
		// Spendable is below the minimum reserve, so the loop never signs and
		// the attempt is recorded as a failure.
		f.sidecar.balanceByVault[f.vault.PubKeyFrost.String()] = sidecar.Balance{Balance: 1_000_000, UnlockedBalance: 1_000_000}
		s := settings
		s.MaxLockBps = 10000
		f.client.consolidateVault(f.vault, 100, s, true)
		require.Equal(t, 0, f.tss.callCount())
		state := f.client.getConsolidationVaultState(f.vault.PubKeyFrost.String())
		require.Equal(t, int64(100), state.LastAttemptHeight)
		require.Equal(t, int64(1), state.ConsecutiveFails)
	})
}

func TestConsolidationHelpersClientCov(t *testing.T) {
	t.Parallel()

	require.Zero(t, countUnlockedOutputs(nil, 10))
	outputs := []sidecar.Output{{Height: 1}, {Height: 95}, {Height: 1, Spent: true}}
	require.Equal(t, int64(1), countUnlockedOutputs(outputs, 100))
	require.Equal(t, int64(2), countUnlockedOutputs(outputs, 0))

	require.Equal(t, defaultXMRConsolidationMinReserveAtomic, initialConsolidationReserve(0, 0))
	require.Equal(t, scaleUint64ByBasisPoints(1000, defaultXMRConsolidationMaxLockBps), xmrConsolidationMaxLockAtomic(1000, 0))
	require.Equal(t, uint64(1000), xmrConsolidationMaxLockAtomic(1000, 20000))
	require.Equal(t, ^uint64(0), bumpConsolidationReserve(^uint64(0)-1, 0))
	require.Zero(t, scaleUint64ByBasisPoints(0, 10))
	require.Equal(t, ^uint64(0), scaleUint64ByBasisPoints(^uint64(0), 20000))
	require.Equal(t, int64(0), scaledConsolidationCooldown(0, 5))
	require.Equal(t, int64(10), scaledConsolidationCooldown(5, 2))
	require.Equal(t, int64(1<<62), scaledConsolidationCooldown(1<<62, 1))
	require.Equal(t, int64(^uint64(0)>>1), scaledConsolidationCooldown(1<<62, 4))
	require.False(t, isRetryableConsolidationErr(nil))
	require.True(t, isRetryableConsolidationErr(errors.New("Insufficient funds")))
	require.True(t, isRetryableConsolidationErr(errors.New("fee too low")))
	require.False(t, isRetryableConsolidationErr(errors.New("other")))
}
