package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// migrationSidecarCov is a controllable sidecar fake for authenticated
// migration replay paths.
type migrationSidecarCov struct {
	mu           sync.Mutex
	txID         string
	location     sidecar.CanonicalTxLocationResp
	locationFail bool
	proof        sidecar.Transfer
	markFail     bool
	markCalls    int
}

func (m *migrationSidecarCov) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/monero/vaults/register":
		_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(201))
	case "/v1/monero/chain/tx/" + m.txID + "/location":
		if m.locationFail {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(m.location)
	case "/v1/monero/outputs/mark-observed":
		m.markCalls++
		if m.markFail {
			http.Error(w, "mark failed", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(m.proof)
	default:
		http.NotFound(w, r)
	}
}

func (m *migrationSidecarCov) set(fn func(m *migrationSidecarCov)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

type migrationFixtureCov struct {
	source, destination common.PubKey
	txID                string
	observed            common.ObservedTx
	fake                *migrationSidecarCov
	scanner             *Scanner
	replay              *authenticatedInternalInboundReplayStore
	reorg               *ReorgStore
}

func newMigrationFixtureCov(t *testing.T, label string) *migrationFixtureCov {
	t.Helper()
	source, destination, txID, voter, refs := testXMRCatchupMigration(t, label)
	blockHash := testXMRHash(label + "-block")
	parentHash := testXMRHash(label + "-parent")
	fake := &migrationSidecarCov{
		txID:     txID,
		location: sidecar.CanonicalTxLocationResp{TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash},
		proof: sidecar.Transfer{
			TxID: txID, TxKey: voter.Tx.XmrTxKey,
			VaultPubKey: source.String(), FromAddress: voter.Tx.Tx.FromAddress.String(),
			Address: voter.Tx.Tx.ToAddress.String(), Amount: 2 * moneroAtomicToThorchain,
			Fee: moneroAtomicToThorchain, Height: 100, Note: voter.Tx.Tx.Memo,
			Origin: sidecar.OutgoingOriginVaultCertified, ObservationEligible: true,
			SpentOutputRefs: []sidecar.SpentRef{{
				TxHash: refs[0].TxHash, OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID,
			}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	scanner, replay, reorg := newXMRCatchupReplayScanner(t, signer, source, destination)
	scanner.setCurrentHeight(200)
	observed := common.NewObservedTx(voter.Tx.Tx, 100, source, 100)
	observed.XmrTxKey = voter.Tx.XmrTxKey
	observed.SpentOutputRefs = []common.OutputRef{*refs[0]}
	return &migrationFixtureCov{
		source: source, destination: destination, txID: txID, observed: observed,
		fake: fake, scanner: scanner, replay: replay, reorg: reorg,
	}
}

func TestEnqueueAuthenticatedInternalInboundReplayErrorsCov(t *testing.T) {
	t.Run("not-migration-and-unconfigured", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-memo")
		tx := f.observed
		tx.Tx.Memo = "OUT:" + testXMRHash("x")
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.NoError(t, err)
		require.False(t, queued)

		bare := &Scanner{}
		queued, err = bare.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.NoError(t, err)
		require.False(t, queued)
	})
	t.Run("invalid-spent-refs", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-refs")
		tx := f.observed
		tx.SpentOutputRefs = nil
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.ErrorContains(t, err, "spent refs are empty")
	})
	t.Run("unmined-height", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-height")
		tx := f.observed
		tx.BlockHeight = 0
		// an unmined observation is rejected by ObservedTx validation (so the
		// scanner's own BlockHeight <= 0 guard after it is unreachable) and
		// nothing is queued
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.ErrorContains(t, err, "block height can't be zero")
		require.False(t, queued)
	})
	t.Run("location-failure", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-loc")
		f.fake.set(func(m *migrationSidecarCov) { m.locationFail = true })
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.ErrorContains(t, err, "locate canonical xmr migration")
	})
	t.Run("scanner-behind", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-behind")
		f.scanner.setCurrentHeight(50)
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.ErrorContains(t, err, "waits for scanner height")
	})
	t.Run("reverted", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-reverted")
		quarantineReplayCov(t, f.reorg.db, f.txID)
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.NoError(t, err)
		require.False(t, queued)
	})
	t.Run("source-mismatch", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-source")
		tx := f.observed
		tx.Tx.FromAddress = tx.Tx.ToAddress
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.ErrorContains(t, err, "observed vault")
	})
	t.Run("destination-unregistered", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-dest")
		_, stranger := testVaultPubKeyAndAddress(t)
		tx := f.observed
		tx.Tx.ToAddress = stranger
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.ErrorContains(t, err, "destination vault is not registered")
	})
	t.Run("identical-vaults", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-same")
		tx := f.observed
		tx.Tx.ToAddress = tx.Tx.FromAddress
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.ErrorContains(t, err, "identical")
	})
	t.Run("journal-conflict", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-journal")
		require.NoError(t, f.reorg.Set(&reorgBlockMeta{Height: 100, BlockHash: testXMRHash("other-fork"), PreviousHash: testXMRHash("p")}))
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.ErrorContains(t, err, "conflicts with canonical journal")
	})
	t.Run("existing-journal-and-mark-failure", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-mark")
		require.NoError(t, f.reorg.Set(&reorgBlockMeta{
			Height: 100, BlockHash: f.fake.location.BlockHash, PreviousHash: f.fake.location.PreviousHash,
		}))
		f.fake.set(func(m *migrationSidecarCov) { m.markFail = true })
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.True(t, queued, "the durable record is written before the sidecar call")
		require.ErrorContains(t, err, "mark canonical xmr migration replay")
		meta, err := f.reorg.Get(100)
		require.NoError(t, err)
		require.Contains(t, meta.CustomerTransactions, normalizeTxID(f.txID))
		require.Contains(t, meta.VaultOriginTransactions, normalizeTxID(f.txID))
		n, err := f.replay.Count()
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})
	t.Run("proof-mismatch", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-proof")
		f.fake.set(func(m *migrationSidecarCov) { m.proof.Note = "MIGRATE:999" })
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.True(t, queued)
		require.ErrorContains(t, err, "validate canonical xmr migration replay")
	})
	t.Run("no-fee", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-enqueue-fee")
		tx := f.observed
		tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.ZeroUint())}
		_, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(tx)
		require.Error(t, err)
	})
}

func TestValidateAuthenticatedMigrationProofCov(t *testing.T) {
	f := newMigrationFixtureCov(t, "cov-validate-proof")
	queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
	require.NoError(t, err)
	require.True(t, queued)

	base := f.fake.proof
	require.NoError(t, f.scanner.validateAuthenticatedMigrationProofForObserved(base, f.observed, 100, 2))

	observedCases := map[string]struct {
		mutate func(p *sidecar.Transfer)
		want   string
	}{
		"txid":   {func(p *sidecar.Transfer) { p.TxID = testXMRHash("other") }, "transaction hash mismatch"},
		"height": {func(p *sidecar.Transfer) { p.Height = 101 }, "not mined at the requested height"},
		"locked": {func(p *sidecar.Transfer) { p.Locked = true }, "not mined at the requested height"},
		"amount": {func(p *sidecar.Transfer) { p.Amount = 3 * moneroAtomicToThorchain }, "payment mismatch"},
		"memo":   {func(p *sidecar.Transfer) { p.Note = "MIGRATE:1" }, "memo mismatch"},
		"txkey":  {func(p *sidecar.Transfer) { p.TxKey = "" }, "tx key mismatch"},
		"source": {func(p *sidecar.Transfer) { p.VaultPubKey = f.destination.String(); p.FromAddress = "" }, "source vault mismatch"},
		"nofee":  {func(p *sidecar.Transfer) { p.Fee = 0 }, "zero fee"},
		"fee":    {func(p *sidecar.Transfer) { p.Fee = 5 * moneroAtomicToThorchain }, "fee differs"},
		"origin": {func(p *sidecar.Transfer) { p.Origin = "unknown" }, "unauthenticated origin"},
		"refs":   {func(p *sidecar.Transfer) { p.SpentOutputRefs = nil }, "spent refs mismatch"},
		"spend": {func(p *sidecar.Transfer) {
			p.SpentOutputRefs = append([]sidecar.SpentRef(nil), p.SpentOutputRefs...)
			p.SpentOutputRefs[0].SpendTxHash = testXMRHash("elsewhere")
		}, "names a different spend"},
	}
	for name, tc := range observedCases {
		p := base
		tc.mutate(&p)
		err = f.scanner.validateAuthenticatedMigrationProofForObserved(p, f.observed, 100, 2)
		require.ErrorContains(t, err, tc.want, name)
	}

	claims, err := f.replay.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	record := claims[0].Record
	require.NoError(t, f.replay.Release(claims[0]))
	record.ClaimToken, record.ClaimedAtUnix = "", 0
	require.NoError(t, f.scanner.validateAuthenticatedMigrationProofForRecord(base, record))

	bad := record
	bad.FeeThorchain = 0
	require.Error(t, f.scanner.validateAuthenticatedMigrationProofForRecord(base, bad))

	recordCases := map[string]struct {
		mutate func(p *sidecar.Transfer)
		want   string
	}{
		"locked": {func(p *sidecar.Transfer) { p.Locked = true }, "not durably observed"},
		"amount": {func(p *sidecar.Transfer) { p.Amount = 9 * moneroAtomicToThorchain }, "changed durable replay identity"},
		"txkey": {func(p *sidecar.Transfer) {
			p.TxKey = "zz"
		}, "changed durable replay identity"},
		"origin": {func(p *sidecar.Transfer) { p.Origin = "unknown" }, "unauthenticated origin"},
		"spend": {func(p *sidecar.Transfer) {
			p.SpentOutputRefs = append([]sidecar.SpentRef(nil), p.SpentOutputRefs...)
			p.SpentOutputRefs[0].SpendTxHash = testXMRHash("elsewhere")
		}, "names a different spend"},
	}
	for name, tc := range recordCases {
		p := base
		tc.mutate(&p)
		err = f.scanner.validateAuthenticatedMigrationProofForRecord(p, record)
		require.ErrorContains(t, err, tc.want, name)
	}

	// Invalid vaults in the record.
	badSource := record
	badSource.SourceVaultPubKey = "bogus"
	_, _, err = f.scanner.authenticatedMigrationItemsFromProof(base, badSource)
	require.ErrorContains(t, err, "source vault is invalid")
	badDest := record
	badDest.DestinationVaultPubKey = "bogus"
	_, _, err = f.scanner.authenticatedMigrationItemsFromProof(base, badDest)
	require.ErrorContains(t, err, "destination vault is invalid")
	src, dst, err := f.scanner.authenticatedMigrationItemsFromProof(base, record)
	require.NoError(t, err)
	require.True(t, src.ObservedVaultPubKey.Equals(f.source))
	require.True(t, dst.ObservedVaultPubKey.Equals(f.destination))
	require.Len(t, src.SpentOutputRefs, 1)
	require.Equal(t, record.TxID, src.SpentOutputRefs[0].SpendTxHash)
	require.Empty(t, dst.SpentOutputRefs)
}

func TestReplayAuthenticatedInternalInboundsBranchesCov(t *testing.T) {
	enqueue := func(t *testing.T, f *migrationFixtureCov) {
		t.Helper()
		queued, err := f.scanner.EnqueueAuthenticatedInternalInboundReplay(f.observed)
		require.NoError(t, err)
		require.True(t, queued)
	}
	count := func(t *testing.T, f *migrationFixtureCov) int {
		t.Helper()
		n, err := f.replay.Count()
		require.NoError(t, err)
		return n
	}

	t.Run("unconfigured", func(t *testing.T) {
		s := &Scanner{}
		items, claims, err := s.replayAuthenticatedInternalInbounds(0, 0)
		require.NoError(t, err)
		require.Nil(t, items)
		require.Nil(t, claims)
		f := newMigrationFixtureCov(t, "cov-replay-noreorg")
		f.scanner.reorgStore = nil
		_, _, err = f.scanner.replayAuthenticatedInternalInbounds(0, 0)
		require.ErrorContains(t, err, "requires reverted transaction quarantine")
	})
	t.Run("reverted-is-invalidated", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-reverted")
		enqueue(t, f)
		quarantineReplayCov(t, f.reorg.db, f.txID)
		items, claims, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Empty(t, claims)
		require.Zero(t, count(t, f))
	})
	t.Run("not-mined-is-retained", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-unmined")
		enqueue(t, f)
		f.fake.set(func(m *migrationSidecarCov) { m.locationFail = true })
		items, claims, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Empty(t, claims)
		require.Equal(t, 1, count(t, f))
		// Released: claimable again.
		again, err := f.replay.Claim(1)
		require.NoError(t, err)
		require.Len(t, again, 1)
	})
	t.Run("mark-failure-is-retained", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-markfail")
		enqueue(t, f)
		f.fake.set(func(m *migrationSidecarCov) { m.markFail = true })
		items, _, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Equal(t, 1, count(t, f))
	})
	t.Run("proof-mismatch-is-retained", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-proof")
		enqueue(t, f)
		f.fake.set(func(m *migrationSidecarCov) { m.proof.Amount = 7 * moneroAtomicToThorchain })
		items, _, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Equal(t, 1, count(t, f))
	})
	t.Run("journal-race-is-retained", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-journal")
		enqueue(t, f)
		require.NoError(t, f.reorg.Set(&reorgBlockMeta{
			Height: 100, BlockHash: testXMRHash("replacement"), PreviousHash: f.fake.location.PreviousHash,
		}))
		items, _, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Empty(t, items)
		require.Equal(t, 1, count(t, f))
	})
	t.Run("relocated-to-new-block", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-relocate")
		enqueue(t, f)
		newHash := testXMRHash("relocated-block")
		newParent := testXMRHash("relocated-parent")
		f.fake.set(func(m *migrationSidecarCov) {
			m.location.Height = 105
			m.location.BlockHash = newHash
			m.location.PreviousHash = newParent
			m.proof.Height = 105
		})
		require.NoError(t, f.reorg.WatchRelocation(scannerRelocationWatch{TxID: f.txID, OriginalHeight: 100, InPool: true}))
		items, claims, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, int64(105), claims[0].Record.BlockHeight)
		require.Len(t, items, 2)
		meta, err := f.reorg.Get(105)
		require.NoError(t, err)
		require.NotNil(t, meta)
		require.Equal(t, newHash, meta.BlockHash)
		require.Contains(t, meta.VaultOriginTransactions, normalizeTxID(f.txID))
		watches, err := f.reorg.ListRelocationWatches(10)
		require.NoError(t, err)
		require.Len(t, watches, 1)
		require.True(t, watches[0].VaultOrigin)
		for _, item := range items {
			item.OnObservedTxStored()
		}
		require.Zero(t, count(t, f))
	})
	t.Run("existing-journal-binds", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-bind")
		enqueue(t, f)
		// Replace the journal so the replay must append the tx.
		require.NoError(t, f.reorg.Set(&reorgBlockMeta{
			Height: 100, BlockHash: f.fake.location.BlockHash, PreviousHash: f.fake.location.PreviousHash,
		}))
		items, _, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.NoError(t, err)
		require.Len(t, items, 2)
		meta, err := f.reorg.Get(100)
		require.NoError(t, err)
		require.Contains(t, meta.CustomerTransactions, normalizeTxID(f.txID))
		require.Contains(t, meta.VaultOriginTransactions, normalizeTxID(f.txID))
		// Customer-only entry gains vault-origin provenance on replay.
		require.NoError(t, f.reorg.Set(&reorgBlockMeta{
			Height: 100, BlockHash: f.fake.location.BlockHash, PreviousHash: f.fake.location.PreviousHash,
			CustomerTransactions: []string{normalizeTxID(f.txID)},
		}))
		items[0].OnObservedTxRejected(nil)
		items, _, err = f.scanner.replayAuthenticatedInternalInbounds(202, 202)
		require.NoError(t, err)
		require.Len(t, items, 2)
		meta, err = f.reorg.Get(100)
		require.NoError(t, err)
		require.Contains(t, meta.VaultOriginTransactions, normalizeTxID(f.txID))
	})
	t.Run("journal-load-failure", func(t *testing.T) {
		f := newMigrationFixtureCov(t, "cov-replay-journal-corrupt")
		enqueue(t, f)
		require.NoError(t, f.reorg.db.Put([]byte(reorgBlockMetaKey(100)), []byte("{"), nil))
		_, _, err := f.scanner.replayAuthenticatedInternalInbounds(201, 201)
		require.ErrorContains(t, err, "load xmr migration replay journal")
		require.Equal(t, 1, count(t, f))
	})
}
