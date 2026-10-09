package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
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

func mempoolServerCov(t *testing.T, status int, outgoing []sidecar.Transfer) *sidecar.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/transfers" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "mempool down", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sidecar.Transfers{Outgoing: outgoing})
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	return client
}

func TestScannerFetchMemPoolFilteringCov(t *testing.T) {
	t.Parallel()

	// No sidecar configured.
	txIn, err := NewScanner(testLogger(), nil, nil, common.XMRChain, common.EmptyPubKey, 8).FetchMemPool(1)
	require.NoError(t, err)
	require.True(t, txIn.MemPool)
	require.Empty(t, txIn.TxArray)

	// Sidecar failure.
	failing := NewScanner(testLogger(), mempoolServerCov(t, http.StatusInternalServerError, nil), nil, common.XMRChain, common.EmptyPubKey, 8)
	_, err = failing.FetchMemPool(1)
	require.ErrorContains(t, err, "fail to get monero mempool transfers")

	// Empty mempool.
	empty := NewScanner(testLogger(), mempoolServerCov(t, http.StatusOK, nil), nil, common.XMRChain, common.EmptyPubKey, 8)
	txIn, err = empty.FetchMemPool(1)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray)

	vault, vaultAddr := testVaultPubKeyAndAddress(t)
	dest := common.XMRChecksummedDummyAddress('8')
	good := sidecar.Transfer{
		TxID: testXMRHash("mempool-good"), Address: dest, FromAddress: vaultAddr.String(),
		VaultPubKey: vault.String(), Amount: 5 * moneroAtomicToThorchain, Fee: moneroAtomicToThorchain,
	}
	noTo := good
	noTo.TxID = testXMRHash("mempool-no-to")
	noTo.Address = " "
	badTo := good
	badTo.TxID = testXMRHash("mempool-bad-to")
	badTo.Address = "not-an-address"
	noSender := good
	noSender.TxID = testXMRHash("mempool-no-sender")
	noSender.FromAddress = ""
	dust := good
	dust.TxID = testXMRHash("mempool-dust")
	dust.Amount = 1

	scanner := NewScanner(testLogger(), mempoolServerCov(t, http.StatusOK, []sidecar.Transfer{noTo, badTo, noSender, dust, good, good}),
		&scannerMemoBridge{asgards: ttypes.Vaults{{PubKey: vault, PubKeyEddsa: vault, PubKeyFrost: vault}}},
		common.XMRChain, common.EmptyPubKey, 8)
	txIn, err = scanner.FetchMemPool(7)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1, "only the valid transfer is emitted, and only once")
	require.Equal(t, good.TxID, txIn.TxArray[0].Tx)
	require.True(t, txIn.TxArray[0].ObservedVaultPubKey.Equals(vault))
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
		require.Error(t, err)
	})
	t.Run("future-scan-owns-delivery", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-future")
		tx := outbound(f)
		f.scanner.setCurrentHeight(10)
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.NoError(t, err)
		require.False(t, queued)
	})
	t.Run("mark-failure", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-mark")
		tx := outbound(f)
		f.fake.set(func(m *migrationSidecarCov) { m.markFail = true })
		_, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.Error(t, err)
	})
	t.Run("proof-mismatch", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-proof")
		tx := outbound(f)
		f.fake.set(func(m *migrationSidecarCov) { m.proof.Amount = 9 * moneroAtomicToThorchain })
		_, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.ErrorContains(t, err, "payment mismatch")
	})
	t.Run("success-and-ineligible", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-out-ok")
		tx := outbound(f)
		queued, err := f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.NoError(t, err)
		require.True(t, queued)
		receipts, err := f.reorg.ListObservationReceipts()
		require.NoError(t, err)
		require.Len(t, receipts, 1)
		require.Equal(t, int64(100), receipts[0].entry.Height)

		f.fake.set(func(m *migrationSidecarCov) { m.proof.ObservationEligible = false })
		_, err = f.scanner.EnqueueAuthenticatedOutboundReplay(tx)
		require.ErrorContains(t, err, "not eligible")
	})
}
