package monero

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestScannerManualVaultAddressCov(t *testing.T) {
	t.Parallel()

	scanner := NewScanner(testLogger(), nil, &scannerMemoBridge{}, common.XMRChain, common.EmptyPubKey, 8)
	vault, address := testVaultPubKeyAndAddress(t)
	_, other := testVaultPubKeyAndAddress(t)

	addr, ok := scanner.ManualVaultAddress(common.EmptyPubKey)
	require.False(t, ok)
	require.Empty(t, addr)

	// Ignored inputs.
	scanner.SetManualVaultAddress(common.EmptyPubKey, address.String())
	scanner.SetManualVaultAddress(vault, " ")
	scanner.SetManualVaultAddress(common.PubKey("bogus"), address.String())
	// Mismatching address is rejected.
	scanner.SetManualVaultAddress(vault, other.String())
	_, ok = scanner.ManualVaultAddress(vault)
	require.False(t, ok)

	scanner.SetManualVaultAddress(vault, " "+address.String()+" ")
	addr, ok = scanner.ManualVaultAddress(vault)
	require.True(t, ok)
	require.Equal(t, address.String(), addr)

	require.Equal(t, int64(0), (&Scanner{}).CurrentHeight())
	scanner.setCurrentHeight(42)
	require.Equal(t, int64(42), scanner.CurrentHeight())
}

type asgardErrBridgeCov struct {
	scannerMemoBridge
	asgardsErr error
	pubKeysErr error
}

func (b *asgardErrBridgeCov) GetAsgards() (ttypes.Vaults, error) {
	if b.asgardsErr != nil {
		return nil, b.asgardsErr
	}
	return b.scannerMemoBridge.GetAsgards()
}

func (b *asgardErrBridgeCov) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	if b.pubKeysErr != nil {
		return nil, b.pubKeysErr
	}
	return b.scannerMemoBridge.GetPubKeys()
}

func TestScannerRefreshAsgardLookupErrorsCov(t *testing.T) {
	t.Parallel()

	bridge := &asgardErrBridgeCov{asgardsErr: errors.New("thornode down")}
	scanner := NewScanner(testLogger(), nil, bridge, common.XMRChain, common.EmptyPubKey, 8)
	require.ErrorContains(t, scanner.refreshAsgardLookup(1), "fail to get asgards")

	bridge.asgardsErr = nil
	bridge.pubKeysErr = errors.New("pubkeys down")
	require.ErrorContains(t, scanner.refreshAsgardLookup(1), "fail to get vault pubkeys")

	// Vaults without a FROST key and pubkeys the bridge cannot resolve are
	// skipped rather than failing the refresh.
	vault := testVaultPubKey(t, "refresh-skip")
	bridge.pubKeysErr = nil
	bridge.asgards = ttypes.Vaults{{PubKey: vault}}
	require.NoError(t, scanner.refreshAsgardLookup(1))
	_, ok := scanner.vaultPubKeyForAddress("unknown")
	require.False(t, ok)
}

func TestEnqueueAuthenticatedOutboundReplayErrorsCov(t *testing.T) {
	outbound := func(f *migrationFixtureCov) common.ObservedTx {
		tx := f.observed
		tx.Tx.Memo = "OUT:" + testXMRHash("cov-outbound-inbound")
		f.fake.set(func(m *migrationSidecarCov) { m.proof.Note = tx.Tx.Memo })
		return tx
	}

	t.Run("non-outbound-and-unconfigured", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-memo")
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
		require.NoError(t, err)
		require.False(t, queued)
		bare := &Scanner{}
		_, err = bare.EnqueueAuthenticatedOutboundReplay(outbound(f))
		require.ErrorContains(t, err, "unavailable")
	})
	t.Run("invalid-refs", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-refs")
		tx := outbound(f)
		tx.SpentOutputRefs = nil
		_, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.ErrorContains(t, err, "spent refs are empty")
	})
	t.Run("location-failure", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-loc")
		tx := outbound(f)
		f.fake.set(func(m *migrationSidecarCov) { m.locationFail = true })
		_, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.ErrorContains(t, err, "fail to locate canonical monero transaction")
	})
	t.Run("future-scan-owns-delivery", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-future")
		tx := outbound(f)
		f.scanner.setCurrentHeight(10)
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.NoError(t, err)
		require.False(t, queued)
	})
	// Enrollment only persists the payout; MarkObserved, proof validation and
	// the receipt handoff run when the scan loop drains the replay queue, which
	// releases (retains) the claim on any of those failures.
	drainRetains := func(t *testing.T, label string, mutate func(m *migrationSidecarCov)) {
		t.Helper()
		f := newMigrationFixtureCov(t, label)
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(outbound(f))
		require.NoError(t, err)
		require.True(t, queued)
		f.fake.set(mutate)
		items, claims, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Empty(t, claims)
		pending, err := f.replay.Count()
		require.NoError(t, err)
		require.Equal(t, 1, pending)
		receipts, err := f.reorg.ListObservationReceipts()
		require.NoError(t, err)
		require.Empty(t, receipts)
	}
	t.Run("mark-failure", func(t *testing.T) {
		drainRetains(t, "cov-out-mark", func(m *migrationSidecarCov) { m.markFail = true })
	})
	t.Run("proof-mismatch", func(t *testing.T) {
		drainRetains(t, "cov-out-proof", func(m *migrationSidecarCov) { m.proof.Amount = 9 * moneroAtomicToThorchain })
	})
	t.Run("ineligible", func(t *testing.T) {
		drainRetains(t, "cov-out-ineligible", func(m *migrationSidecarCov) { m.proof.ObservationEligible = false })
	})
	t.Run("success", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-ok")
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(outbound(f))
		require.NoError(t, err)
		require.True(t, queued)
		items, claims, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Empty(t, claims)
		receipts, err := f.reorg.ListObservationReceipts()
		require.NoError(t, err)
		require.Len(t, receipts, 1)
		require.Equal(t, int64(100), receipts[0].entry.Height)
		pending, err := f.replay.Count()
		require.NoError(t, err)
		require.Zero(t, pending)
	})
}
