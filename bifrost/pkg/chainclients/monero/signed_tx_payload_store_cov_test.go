package monero

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

func newSignedStoreCov(t *testing.T) (*signedTxPayloadStore, *leveldb.DB) {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return newSignedTxPayloadStore(db), db
}

func signedStorePutCov(t *testing.T, db *leveldb.DB, key string, value any) {
	t.Helper()
	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		var err error
		raw, err = json.Marshal(v)
		require.NoError(t, err)
	}
	require.NoError(t, db.Put([]byte(key), raw, nil))
}

func signedStoreTombstoneCov(txHash string) signedTxObservedLogical {
	return signedTxObservedLogical{Version: 1, TxHash: txHash}
}

func broadcastCov(store *signedTxPayloadStore, plan signedTxPayloadClaimPlan, txHash string, fn func() (string, error)) (string, error) {
	return store.WithBroadcastAuthorization(plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		txHash, plan.MsgID, plan.PlanHash, fn)
}

func TestSignedTxPayloadStoreNilAndInputValidationCov(t *testing.T) {
	t.Parallel()

	var s *signedTxPayloadStore
	plan := standaloneClaimPlan("logical-nil")
	_, err := s.SnapshotObserveEpochs([]string{"a"})
	require.ErrorContains(t, err, "not initialized")
	require.ErrorContains(t, s.Reserve(plan), "not initialized")
	require.ErrorContains(t, s.ReleaseReserved(plan), "not initialized")
	require.ErrorContains(t, s.QuarantineReverted("a"), "not initialized")
	_, err = s.IsLocallySignedTx("a")
	require.ErrorContains(t, err, "not initialized")
	require.ErrorContains(t, s.Set(plan, "a", []byte("{}")), "not initialized")
	payload, ok, err := s.GetOwned("a", "b", "c")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, payload)
	_, ok, err = s.ActiveClaim([]string{"a"}, "o")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.Claim("a")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.CommitUnfinalizedLogical("a", []string{"a"}, "t")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.CommitUnfinalizedExact("a", "o", "t")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.CompleteObservedLogical("a", []string{"a"}, "t")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.CompleteObserved("a", "o", "t")
	require.NoError(t, err)
	require.False(t, ok)
	_, err = broadcastCov(s, plan, "t", func() (string, error) { return "t", nil })
	require.ErrorContains(t, err, "not initialized")
	_, ok, err = s.Get("a", "b")
	require.NoError(t, err)
	require.False(t, ok)

	store, _ := newSignedStoreCov(t)
	_, err = store.SnapshotObserveEpochs([]string{" ", ""})
	require.ErrorContains(t, err, "no observation key")
	require.ErrorContains(t, store.QuarantineReverted(" "), "hash is empty")
	_, err = store.IsLocallySignedTx(" ")
	require.ErrorContains(t, err, "hash is empty")
	_, _, err = store.GetOwned("", "b", "c")
	require.ErrorContains(t, err, "identity is incomplete")
	_, _, err = store.ActiveClaim(nil, "o")
	require.ErrorContains(t, err, "no observation key")
	_, _, err = store.ActiveClaim([]string{"a"}, " ")
	require.ErrorContains(t, err, "owner is empty")
	_, _, err = store.Claim(" ")
	require.ErrorContains(t, err, "logical hash is empty")
	_, _, err = store.CommitUnfinalizedLogical("", []string{"a"}, "t")
	require.ErrorContains(t, err, "identity is incomplete")
	_, _, err = store.CommitUnfinalizedExact("a", "", "t")
	require.ErrorContains(t, err, "identity is incomplete")
	_, _, err = store.CompleteObservedLogical("a", nil, "t")
	require.ErrorContains(t, err, "identity is incomplete")
	_, _, err = store.CompleteObserved("", "o", "t")
	require.ErrorContains(t, err, "identity is incomplete")
	_, err = broadcastCov(store, plan, "t", nil)
	require.ErrorContains(t, err, "identity is incomplete")
	_, _, err = store.Get(" ", "b")
	require.ErrorContains(t, err, "identity is incomplete")

	// Set input validation.
	require.ErrorContains(t, store.Set(plan, "", []byte("{}")), "is empty")
	require.ErrorContains(t, store.Set(plan, "tx", []byte("{")), "decode xmr signed tx payload")
	require.ErrorContains(t, store.Set(plan, "tx", standalonePayload("other")), "does not match index")
	incomplete := plan
	incomplete.MsgID = ""
	require.ErrorContains(t, store.Set(incomplete, "tx", standalonePayload("tx")), "plan is incomplete")
	require.ErrorContains(t, store.ReleaseReserved(incomplete), "plan is incomplete")
}

func TestNormalizeSignedTxClaimPlanCov(t *testing.T) {
	t.Parallel()

	plan := standaloneClaimPlan("Logical")
	plan.ObserveKeys = []string{" B ", "a", "b", ""}
	plan.ObserveEpoch = map[string]uint64{"a": 1, "B": 2, "": 9}
	got, err := normalizeSignedTxClaimPlan(plan)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got.ObserveKeys)
	require.Equal(t, map[string]uint64{"a": 1, "b": 2}, got.ObserveEpoch)
	require.Equal(t, "logical", got.LogicalHash)

	dup := standaloneClaimPlan("l")
	dup.ObserveEpoch = map[string]uint64{"observe-key": 0, "OBSERVE-KEY": 1}
	_, err = normalizeSignedTxClaimPlan(dup)
	require.ErrorContains(t, err, "duplicate observation epochs")

	missing := standaloneClaimPlan("l")
	missing.ObserveEpoch = map[string]uint64{}
	_, err = normalizeSignedTxClaimPlan(missing)
	require.ErrorContains(t, err, "did not snapshot every observation epoch")

	wrong := standaloneClaimPlan("l")
	wrong.ObserveEpoch = map[string]uint64{"other": 0}
	_, err = normalizeSignedTxClaimPlan(wrong)
	require.ErrorContains(t, err, "observation epoch is missing")

	noKeys := standaloneClaimPlan("l")
	noKeys.ObserveKeys = nil
	_, err = normalizeSignedTxClaimPlan(noKeys)
	require.ErrorContains(t, err, "no observation key")

	require.False(t, mapsEqualEpochs(map[string]uint64{"a": 1}, map[string]uint64{}))
	require.False(t, mapsEqualEpochs(map[string]uint64{"a": 1}, map[string]uint64{"a": 2}))
	require.True(t, mapsEqualEpochs(map[string]uint64{"a": 1}, map[string]uint64{"a": 1}))

	_, err = decodeObservationEpoch([]byte("{"))
	require.ErrorContains(t, err, "decode xmr observation epoch")
	_, err = decodeObservationEpoch([]byte(`{"version":2}`))
	require.ErrorContains(t, err, "unsupported xmr observation epoch version")

	_, err = decodeObservedLogical([]byte("{"))
	require.ErrorContains(t, err, "decode observed xmr logical tombstone")
	_, err = decodeObservedLogical([]byte(`{"version":1}`))
	require.ErrorContains(t, err, "malformed")

	_, err = decodeBroadcastAttempt([]byte("{"))
	require.ErrorContains(t, err, "decode xmr broadcast attempt")
	_, err = decodeBroadcastAttempt([]byte(`{"version":1}`))
	require.ErrorContains(t, err, "malformed")

	values, err := normalizeOptionalClaimHashes([]string{"B", " ", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, values)

	token, err := newSignedTxBroadcastToken()
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

func TestDecodeSignedTxPayloadClaimCov(t *testing.T) {
	t.Parallel()

	valid := claimFromPlan(func() signedTxPayloadClaimPlan {
		p, err := normalizeSignedTxClaimPlan(standaloneClaimPlan("decode"))
		require.NoError(t, err)
		return p
	}())
	encode := func(c signedTxPayloadClaim) []byte {
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		return raw
	}
	got, err := decodeSignedTxPayloadClaim(encode(valid))
	require.NoError(t, err)
	require.Equal(t, valid.LogicalHash, got.LogicalHash)

	_, err = decodeSignedTxPayloadClaim([]byte("{"))
	require.ErrorContains(t, err, "decode xmr signed tx claim")

	cases := map[string]struct {
		mutate func(c *signedTxPayloadClaim)
		want   string
	}{
		"version":   {func(c *signedTxPayloadClaim) { c.Version = 1 }, "unsupported xmr signed tx claim version"},
		"state":     {func(c *signedTxPayloadClaim) { c.State = "bogus" }, "unsupported xmr signed tx claim state"},
		"malformed": {func(c *signedTxPayloadClaim) { c.MsgID = "" }, "malformed"},
		"location":  {func(c *signedTxPayloadClaim) { c.CacheHash = "other" }, "payload location is inconsistent"},
		"unsorted": {func(c *signedTxPayloadClaim) {
			c.ObserveKeys = []string{"b", "a"}
			c.ObserveEpoch = map[string]uint64{"a": 0, "b": 0}
		}, "not canonical"},
		"uncanonical": {func(c *signedTxPayloadClaim) {
			c.ObserveKeys = []string{"A"}
			c.ObserveEpoch = map[string]uint64{"A": 0}
		}, "observation key is not canonical"},
		"epoch":    {func(c *signedTxPayloadClaim) { c.ObserveEpoch = map[string]uint64{"x": 0} }, "observation epoch is missing"},
		"signed":   {func(c *signedTxPayloadClaim) { c.State = signedTxClaimSigned }, "signed xmr claim has empty transaction hash"},
		"observed": {func(c *signedTxPayloadClaim) { c.State = signedTxClaimObserved }, "observed xmr claim has empty transaction hash"},
	}
	for name, tc := range cases {
		c := valid
		c.ObserveKeys = append([]string(nil), valid.ObserveKeys...)
		c.ObserveEpoch = cloneEpochs(valid.ObserveEpoch)
		tc.mutate(&c)
		_, err = decodeSignedTxPayloadClaim(encode(c))
		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestSignedTxPayloadStoreReserveBarriersCov(t *testing.T) {
	t.Parallel()

	t.Run("observed-tombstone", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimObserved)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimObserved)
		_, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
		require.ErrorIs(t, err, errXMRSigningClaimObserved)
	})
	t.Run("committed-logical", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimCommitted)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimCommitted)
		_, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
		require.ErrorIs(t, err, errXMRSigningClaimCommitted)
	})
	t.Run("committed-logical-malformed", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), "{")
		require.ErrorContains(t, store.Reserve(plan), "decode committed-unfinalized xmr logical guard")
	})
	t.Run("committed-alias", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("tx"))
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimAliasCommitted)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimAliasCommitted)
		_, _, err := store.ActiveClaim(plan.ObserveKeys, plan.OwnerVault)
		require.ErrorIs(t, err, errXMRSigningClaimAliasCommitted)
	})
	t.Run("committed-alias-malformed", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), "{")
		require.ErrorContains(t, store.Reserve(plan), "decode committed-unfinalized xmr alias guard")
	})
	t.Run("quarantined", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxQuarantinedAliasKey("observe-key"), "tx")
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimQuarantined)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimQuarantined)
		require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimQuarantined)
		_, _, err := store.ActiveClaim(plan.ObserveKeys, plan.OwnerVault)
		require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	})
	t.Run("quarantine-malformed", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxQuarantinedLogicalKey("logical"), " ")
		require.ErrorContains(t, store.Reserve(plan), "quarantine is malformed")
		require.ErrorContains(t, store.ReleaseReserved(plan), "quarantine is malformed")
		_, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
		require.ErrorContains(t, err, "quarantine is malformed")
	})
	t.Run("tx-quarantined-on-set", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		signedStorePutCov(t, db, signedTxQuarantinedTxKey("tx"), "tx")
		require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimQuarantined)
	})
	t.Run("epoch-stale-and-index-conflict", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		// Another logical owns the observation key already.
		signedStorePutCov(t, db, signedTxPayloadObserveKey("observe-key"), "other-logical")
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimConflict)
		require.NoError(t, db.Delete([]byte(signedTxPayloadObserveKey("observe-key")), nil))
		// Observation epoch moved since the snapshot.
		signedStorePutCov(t, db, signedTxObserveEpochKey("observe-key"), signedTxObservationEpoch{Version: 1, Epoch: 3})
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimStale)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimStale)
		snap, err := store.SnapshotObserveEpochs([]string{"OBSERVE-KEY"})
		require.NoError(t, err)
		require.Equal(t, map[string]uint64{"observe-key": 3}, snap)
		signedStorePutCov(t, db, signedTxObserveEpochKey("observe-key"), "{")
		_, err = store.SnapshotObserveEpochs([]string{"observe-key"})
		require.ErrorContains(t, err, "decode xmr observation epoch")
		require.ErrorContains(t, store.Reserve(plan), "decode xmr observation epoch")
	})
	t.Run("existing-claim-paths", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		require.NoError(t, store.Reserve(plan), "exact retry is idempotent")

		other := plan
		other.CacheVault = "other-vault"
		require.ErrorIs(t, store.Reserve(other), errXMRSigningClaimConflict)

		changed := plan
		changed.MsgID = "different"
		require.ErrorIs(t, store.Reserve(changed), errXMRSigningClaimConflict)

		// Missing observation index makes the claim inconsistent.
		require.NoError(t, db.Delete([]byte(signedTxPayloadObserveKey("observe-key")), nil))
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimConflict)
		signedStorePutCov(t, db, signedTxPayloadObserveKey("observe-key"), "logical")

		// Stale epoch on retry.
		signedStorePutCov(t, db, signedTxObserveEpochKey("observe-key"), signedTxObservationEpoch{Version: 1, Epoch: 1})
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimStale)
		require.NoError(t, db.Delete([]byte(signedTxObserveEpochKey("observe-key")), nil))

		// Signed claim cannot be reserved again nor released.
		require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimConflict)
		require.ErrorIs(t, store.ReleaseReserved(plan), errXMRSigningClaimConflict)
		// Idempotent late set; different late result rejected.
		require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
		require.ErrorIs(t, store.Set(plan, "tx2", standalonePayload("tx2")), errXMRSigningClaimConflict)

		// Corrupt claim.
		signedStorePutCov(t, db, signedTxPayloadClaimKey("logical"), "{")
		require.ErrorContains(t, store.Reserve(plan), "decode xmr signed tx claim")
		require.ErrorContains(t, store.ReleaseReserved(plan), "decode xmr signed tx claim")
		require.ErrorContains(t, store.Set(plan, "tx", standalonePayload("tx")), "decode xmr signed tx claim")
		_, _, err := store.Claim("logical")
		require.ErrorContains(t, err, "decode xmr signed tx claim")
		_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
		require.ErrorContains(t, err, "decode xmr signed tx claim")
		_, err = store.IsLocallySignedTx("unrelated")
		require.ErrorContains(t, err, "decode xmr signed tx claim")
		require.ErrorContains(t, store.QuarantineReverted("unrelated"), "decode xmr signed tx claim")
	})
	t.Run("observed-claim", func(t *testing.T) {
		store, _ := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		_, ok, err := store.CompleteObservedLogical("logical", plan.ObserveKeys, "obs")
		require.NoError(t, err)
		require.True(t, ok)
		require.ErrorIs(t, store.Reserve(plan), errXMRSigningClaimObserved)
		require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimObserved)
	})
}

func TestSignedTxPayloadStoreSetLateObservationCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))

	// Missing claim.
	missing := standaloneClaimPlan("missing")
	require.ErrorIs(t, store.Set(missing, "tx", standalonePayload("tx")), errXMRSigningClaimConflict)

	// Committed logical guard blocks set.
	signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimCommitted)
	require.NoError(t, db.Delete([]byte(signedTxCommittedLogicalKey("logical")), nil))

	// Committed alias guard: same hash vs different hash.
	signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("tx"))
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimCommitted)
	require.ErrorIs(t, store.Set(plan, "tx2", standalonePayload("tx2")), errXMRSigningClaimAliasCommitted)
	require.NoError(t, db.Delete([]byte(signedTxCommittedAliasKey("observe-key")), nil))

	// Different plan.
	changed := plan
	changed.PlanHash = "different"
	require.ErrorIs(t, store.Set(changed, "tx", standalonePayload("tx")), errXMRSigningClaimConflict)

	// Existing payload bytes differ.
	signedStorePutCov(t, db, signedTxPayloadKey(plan.CacheVault, plan.CacheHash), "different")
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimConflict)
	require.NoError(t, db.Delete([]byte(signedTxPayloadKey(plan.CacheVault, plan.CacheHash)), nil))

	// Tx hash already indexes another location.
	signedStorePutCov(t, db, signedTxPayloadTxKey("tx"), signedTxPayloadLocation{PayloadKey: "x", LogicalHash: "y"})
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimConflict)
	require.NoError(t, db.Delete([]byte(signedTxPayloadTxKey("tx")), nil))

	// Observed before the signed result: terminalizes the claim.
	signedStorePutCov(t, db, signedTxObservedTxKey("tx"), "observe-key")
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimObserved)
	claim, ok, err := store.Claim("logical")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, signedTxClaimObserved, claim.State)
	require.Equal(t, "tx", claim.ObservedTxHash)
	require.ErrorIs(t, store.Set(plan, "tx", standalonePayload("tx")), errXMRSigningClaimObserved)
}

func TestSignedTxPayloadStoreGetOwnedCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")

	// No claim and no payload.
	payload, ok, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, payload)

	// Unowned legacy payload.
	signedStorePutCov(t, db, signedTxPayloadKey(plan.CacheVault, plan.CacheHash), "legacy")
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	require.NoError(t, db.Delete([]byte(signedTxPayloadKey(plan.CacheVault, plan.CacheHash)), nil))

	// Reserved claim returns nothing.
	require.NoError(t, store.Reserve(plan))
	_, ok, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.NoError(t, err)
	require.False(t, ok)

	// Wrong vault.
	_, _, err = store.GetOwned("other-vault", plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
	payload, ok, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, standalonePayload("tx"), payload)

	// Committed alias: same hash -> committed, different -> alias committed.
	signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("tx"))
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimCommitted)
	signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("other"))
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimAliasCommitted)
	require.NoError(t, db.Delete([]byte(signedTxCommittedAliasKey("observe-key")), nil))

	// Missing payload behind a signed claim.
	require.NoError(t, db.Delete([]byte(signedTxPayloadKey(plan.CacheVault, plan.CacheHash)), nil))
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Claim quarantined via its signed tx hash.
	signedStorePutCov(t, db, signedTxQuarantinedTxKey("tx"), "tx")
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.NoError(t, db.Delete([]byte(signedTxQuarantinedTxKey("tx")), nil))

	// Get accessor.
	require.NoError(t, db.Put([]byte(signedTxPayloadKey(plan.CacheVault, plan.CacheHash)), []byte("p"), nil))
	got, ok, err := store.Get(plan.CacheVault, plan.CacheHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte("p"), got)
	_, ok, err = store.Get("nope", "nope")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSignedTxPayloadStoreActiveClaimCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")

	_, ok, err := store.ActiveClaim([]string{"observe-key"}, "owner-vault")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, store.Reserve(plan))
	claim, ok, err := store.ActiveClaim([]string{"OBSERVE-KEY", "unknown"}, "owner-vault")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "logical", claim.LogicalHash)

	_, _, err = store.ActiveClaim([]string{"observe-key"}, "other-owner")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Two aliases selecting different claims.
	signedStorePutCov(t, db, signedTxPayloadObserveKey("second"), "other-logical")
	_, _, err = store.ActiveClaim([]string{"observe-key", "second"}, "owner-vault")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	require.NoError(t, db.Delete([]byte(signedTxPayloadObserveKey("second")), nil))

	// Logical quarantine.
	signedStorePutCov(t, db, signedTxQuarantinedLogicalKey("logical"), "tx")
	_, _, err = store.ActiveClaim([]string{"observe-key"}, "owner-vault")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.NoError(t, db.Delete([]byte(signedTxQuarantinedLogicalKey("logical")), nil))

	// Committed logical guard.
	signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
	_, _, err = store.ActiveClaim([]string{"observe-key"}, "owner-vault")
	require.ErrorIs(t, err, errXMRSigningClaimCommitted)
	require.NoError(t, db.Delete([]byte(signedTxCommittedLogicalKey("logical")), nil))

	// Claim quarantined via signed tx hash.
	require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
	signedStorePutCov(t, db, signedTxQuarantinedTxKey("tx"), "tx")
	_, _, err = store.ActiveClaim([]string{"observe-key"}, "owner-vault")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.NoError(t, db.Delete([]byte(signedTxQuarantinedTxKey("tx")), nil))

	// Dangling alias -> claim missing.
	signedStorePutCov(t, db, signedTxPayloadObserveKey("dangling"), "nothing")
	_, _, err = store.ActiveClaim([]string{"dangling"}, "owner-vault")
	require.ErrorContains(t, err, "get active xmr claim")
	signedStorePutCov(t, db, signedTxPayloadClaimKey("nothing"), "{")
	_, _, err = store.ActiveClaim([]string{"dangling"}, "owner-vault")
	require.ErrorContains(t, err, "decode xmr signed tx claim")
}

func TestSignedTxPayloadStoreQuarantineAndProvenanceCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))

	// Provenance via payload index.
	signed, err := store.IsLocallySignedTx("TX")
	require.NoError(t, err)
	require.True(t, signed)
	// Provenance via claim scan (payload index removed).
	require.NoError(t, db.Delete([]byte(signedTxPayloadTxKey("tx")), nil))
	signed, err = store.IsLocallySignedTx("tx")
	require.NoError(t, err)
	require.True(t, signed)
	// Provenance via broadcast attempt.
	signedStorePutCov(t, db, signedTxBroadcastAttemptKey("other"), signedTxBroadcastAttempt{
		Version: 1, Token: "t", PayloadKey: signedTxPayloadPrefix + "x", LogicalHash: "other", OwnerVault: "o",
		TxHash: "attempt-tx", MsgID: "m", PlanHash: "p",
	})
	signed, err = store.IsLocallySignedTx("attempt-tx")
	require.NoError(t, err)
	require.True(t, signed)
	signed, err = store.IsLocallySignedTx("unknown")
	require.NoError(t, err)
	require.False(t, signed)

	// Malformed payload index.
	signedStorePutCov(t, db, signedTxPayloadTxKey("bad"), "{}")
	_, err = store.IsLocallySignedTx("bad")
	require.ErrorContains(t, err, "provenance index is malformed")
	require.NoError(t, db.Delete([]byte(signedTxPayloadTxKey("bad")), nil))

	// Quarantine of the attempt tx covers its logical hash.
	signedStorePutCov(t, db, signedTxObserveEventKey("alias-x", "attempt-tx"), "1")
	signedStorePutCov(t, db, signedTxCommittedTxKey("attempt-tx"), "committed-logical")
	require.NoError(t, store.QuarantineReverted("attempt-tx"))
	for _, key := range []string{
		signedTxQuarantinedTxKey("attempt-tx"),
		signedTxQuarantinedLogicalKey("other"),
		signedTxQuarantinedLogicalKey("committed-logical"),
		signedTxQuarantinedAliasKey("committed-logical"),
		signedTxQuarantinedAliasKey("alias-x"),
	} {
		v, getErr := db.Get([]byte(key), nil)
		require.NoError(t, getErr, key)
		require.Equal(t, "attempt-tx", string(v))
	}

	// Quarantine of the signed tx via the claim scan and payload index.
	require.NoError(t, db.Put([]byte(signedTxPayloadTxKey("tx")),
		mustJSONCov(t, signedTxPayloadLocation{PayloadKey: signedTxPayloadKey(plan.CacheVault, plan.CacheHash), LogicalHash: "logical"}), nil))
	require.NoError(t, store.QuarantineReverted("tx"))
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)

	// Corrupt payload location / broadcast attempt break quarantine.
	signedStorePutCov(t, db, signedTxPayloadTxKey("corrupt"), "{")
	require.ErrorContains(t, store.QuarantineReverted("corrupt"), "decode quarantined xmr payload location")
	signedStorePutCov(t, db, signedTxBroadcastAttemptKey("zzz"), "{")
	require.ErrorContains(t, store.QuarantineReverted("another"), "decode xmr broadcast attempt")
	_, err = store.IsLocallySignedTx("another")
	require.ErrorContains(t, err, "decode xmr broadcast attempt")
}

func mustJSONCov(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestSignedTxPayloadStoreUnfinalizedLogicalCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))

	// Quarantined tx.
	signedStorePutCov(t, db, signedTxQuarantinedTxKey("q"), "q")
	_, _, err := store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "q")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	_, _, err = store.CommitUnfinalizedExact("observe-key", "owner-vault", "q")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	_, _, err = store.CompleteObservedLogical("logical", plan.ObserveKeys, "q")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	_, _, err = store.CompleteObservedExact("observe-key", "owner-vault", "q")
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)

	// Visibility-only import of the exact signed tx preserves the claim.
	claim, preserved, err := store.ImportVerifiedLogical("logical", plan.ObserveKeys, "tx")
	require.NoError(t, err)
	require.True(t, preserved)
	require.Equal(t, signedTxClaimSigned, claim.State)

	// Commit an unfinalized observation for the exact tx.
	_, ok, err := store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx")
	require.NoError(t, err)
	require.True(t, ok)
	// Idempotent.
	_, ok, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx")
	require.NoError(t, err)
	require.True(t, ok)
	// A different hash conflicts.
	_, _, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx2")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Completing with a different hash also conflicts.
	_, _, err = store.CompleteObservedLogical("logical", plan.ObserveKeys, "tx2")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Broadcast of the committed exact tx is a no-op success.
	called := false
	hash, err := broadcastCov(store, plan, "tx", func() (string, error) { called = true; return "tx", nil })
	require.NoError(t, err)
	require.Equal(t, "tx", hash)
	require.False(t, called)

	// Finalize.
	claim, ok, err = store.CompleteObservedLogical("logical", plan.ObserveKeys, "tx")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, signedTxClaimObserved, claim.State)

	// Final tombstone: same tx reports observed, different conflicts.
	claim, ok, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, signedTxClaimObserved, claim.State)
	_, _, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx2")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), "{")
	_, _, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx")
	require.ErrorContains(t, err, "decode observed xmr logical tombstone")

	// Observed claim cannot be completed again with a different hash.
	require.NoError(t, db.Delete([]byte(signedTxObservedLogicalKey("logical")), nil))
	require.NoError(t, db.Delete([]byte(signedTxObservedTxKey("tx")), nil))
	_, _, err = store.CompleteObservedLogical("logical", plan.ObserveKeys, "tx3")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
}

func TestSignedTxPayloadStoreUnfinalizedLogicalNoClaimCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	// No local claim: commit records a barrier, completion writes a tombstone.
	claim, ok, err := store.CommitUnfinalizedLogical("logical", []string{"k"}, "tx")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "logical", claim.LogicalHash)

	// The tx is already indexed to another logical.
	signedStorePutCov(t, db, signedTxCommittedTxKey("tx-b"), "other-logical")
	_, _, err = store.CommitUnfinalizedLogical("logical-b", []string{"k2"}, "tx-b")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Corrupt claim under a logical hash.
	signedStorePutCov(t, db, signedTxPayloadClaimKey("logical-c"), "{")
	_, _, err = store.CommitUnfinalizedLogical("logical-c", []string{"k3"}, "tx-c")
	require.ErrorContains(t, err, "decode xmr signed tx claim")
	_, _, err = store.CompleteObservedLogical("logical-c", []string{"k3"}, "tx-c")
	require.ErrorContains(t, err, "decode xmr signed tx claim")

	// Completing logical whose tx is indexed by another finalized logical.
	_, ok, err = store.CompleteObservedLogical("logical-d", []string{"k4"}, "tx-d")
	require.NoError(t, err)
	require.True(t, ok)
	_, _, err = store.CompleteObservedLogical("logical-e", []string{"k5"}, "tx-d")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Reverse owner inconsistent (tombstone points to a different tx).
	signedStorePutCov(t, db, signedTxObservedLogicalKey("logical-d"), signedStoreTombstoneCov("tx-zz"))
	_, _, err = store.CompleteObservedLogical("logical-e", []string{"k5"}, "tx-d")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Malformed reverse index.
	signedStorePutCov(t, db, signedTxObservedTxKey("tx-f"), " ")
	_, _, err = store.CompleteObservedLogical("logical-f", []string{"k6"}, "tx-f")
	require.ErrorContains(t, err, "reverse index is malformed")
	// Existing tombstone with a different tx.
	signedStorePutCov(t, db, signedTxObservedLogicalKey("logical-g"), signedStoreTombstoneCov("tx-other"))
	_, _, err = store.CompleteObservedLogical("logical-g", []string{"k7"}, "tx-g")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Overflowing epoch.
	signedStorePutCov(t, db, signedTxObserveEpochKey("k8"), signedTxObservationEpoch{Version: 1, Epoch: ^uint64(0)})
	_, _, err = store.CompleteObservedLogical("logical-h", []string{"k8"}, "tx-h")
	require.ErrorContains(t, err, "epoch overflow")
	_, _, err = store.CommitUnfinalizedLogical("logical-h", []string{"k8"}, "tx-h")
	require.ErrorContains(t, err, "epoch overflow")

	// Broadcast attempt for a different tx blocks commit/complete.
	signedStorePutCov(t, db, signedTxBroadcastAttemptKey("logical-i"), signedTxBroadcastAttempt{
		Version: 1, Token: "t", PayloadKey: signedTxPayloadPrefix + "x", LogicalHash: "logical-i", OwnerVault: "o",
		TxHash: "attempt", MsgID: "m", PlanHash: "p",
	})
	_, _, err = store.CommitUnfinalizedLogical("logical-i", []string{"k9"}, "tx-i")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	_, _, err = store.CompleteObservedLogical("logical-i", []string{"k9"}, "tx-i")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Inconsistent attempt index.
	signedStorePutCov(t, db, signedTxBroadcastAttemptKey("logical-j"), signedTxBroadcastAttempt{
		Version: 1, Token: "t", PayloadKey: signedTxPayloadPrefix + "x", LogicalHash: "elsewhere", OwnerVault: "o",
		TxHash: "attempt", MsgID: "m", PlanHash: "p",
	})
	_, _, err = store.CompleteObservedLogical("logical-j", []string{"k10"}, "tx-j")
	require.ErrorContains(t, err, "logical index is inconsistent")

	// Committed alias reverse index malformed / inconsistent.
	signedStorePutCov(t, db, signedTxCommittedAliasTxKey("tx-k", "alias"), " ")
	_, _, err = store.CompleteObservedLogical("logical-k", []string{"k11"}, "tx-k")
	require.ErrorContains(t, err, "alias reverse index is malformed")
	signedStorePutCov(t, db, signedTxCommittedAliasTxKey("tx-l", "alias-l"), "alias-l")
	_, _, err = store.CompleteObservedLogical("logical-l", []string{"k12"}, "tx-l")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
}

func TestSignedTxPayloadStoreExactAliasCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))

	// Unmatched alias.
	claim, ok, err := store.CommitUnfinalizedExact("unmatched", "owner-vault", "u")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, claim.LogicalHash)

	// Ambiguous: different owner.
	claim, ok, err = store.CommitUnfinalizedExact("observe-key", "someone-else", "tx")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "logical", claim.LogicalHash)
	// Alias now has a committed guard for tx; a different tx conflicts.
	_, _, err = store.CommitUnfinalizedExact("observe-key", "owner-vault", "other")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Exact import is preserved.
	_, preserved, err := store.ImportVerifiedExact("observe-key", "owner-vault", "tx")
	require.NoError(t, err)
	require.True(t, preserved)

	// Exact commit.
	_, ok, err = store.CommitUnfinalizedExact("observe-key", "owner-vault", "tx")
	require.NoError(t, err)
	require.True(t, ok)

	// Exact completion, then import replay of the final is a no-op.
	claim, ok, err = store.CompleteObservedExact("observe-key", "owner-vault", "tx")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, signedTxClaimObserved, claim.State)
	claim, ok, err = store.ImportVerifiedExact("observe-key", "owner-vault", "tx")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, claim.LogicalHash)
	// Replayed final with no outstanding barrier returns no claim.
	_, ok, err = store.CompleteObservedExact("observe-key", "owner-vault", "tx")
	require.NoError(t, err)
	require.False(t, ok)

	// Malformed final reverse index.
	signedStorePutCov(t, db, signedTxObservedTxKey("bad"), " ")
	_, _, err = store.ImportVerifiedExact("observe-key", "owner-vault", "bad")
	require.ErrorContains(t, err, "reverse index is malformed")
	_, _, err = store.CompleteObservedExact("observe-key", "owner-vault", "bad")
	require.ErrorContains(t, err, "reverse index is malformed")

	// Dangling alias index / corrupt claim.
	signedStorePutCov(t, db, signedTxPayloadObserveKey("dangling"), "missing")
	_, _, err = store.CommitUnfinalizedExact("dangling", "owner-vault", "d1")
	require.ErrorContains(t, err, "get xmr claim for committed-unfinalized observation")
	_, _, err = store.CompleteObserved("dangling", "owner-vault", "d2")
	require.ErrorContains(t, err, "get xmr claim selected by observation")
	signedStorePutCov(t, db, signedTxPayloadClaimKey("missing"), "{")
	_, _, err = store.CommitUnfinalizedExact("dangling", "owner-vault", "d3")
	require.ErrorContains(t, err, "decode xmr signed tx claim")
	_, _, err = store.CompleteObserved("dangling", "owner-vault", "d4")
	require.ErrorContains(t, err, "decode xmr signed tx claim")
}

func TestSignedTxPayloadStoreCompleteObservedAliasCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))

	// Owner mismatch.
	_, _, err := store.CompleteObserved("observe-key", "other-owner", "tx")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)

	// Exact mode with an unsigned claim records an ambiguous barrier.
	claim, ok, err := store.CompleteObservedExact("observe-key", "owner-vault", "wire")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "logical", claim.LogicalHash)
	committed, found, err := func() (signedTxObservedLogical, bool, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.committedAliasLocked("observe-key")
	}()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "wire", committed.TxHash)

	// Legacy completion with a different hash conflicts with the alias barrier.
	_, _, err = store.CompleteObserved("observe-key", "owner-vault", "legacy")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	require.NoError(t, db.Delete([]byte(signedTxCommittedAliasKey("observe-key")), nil))

	// Legacy completion terminalizes.
	claim, ok, err = store.CompleteObserved("observe-key", "owner-vault", "legacy")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, signedTxClaimObserved, claim.State)

	// Unmatched alias with a committed guard for a different tx conflicts.
	signedStorePutCov(t, db, signedTxCommittedAliasKey("lonely"), signedStoreTombstoneCov("x"))
	_, _, err = store.CompleteObserved("lonely", "owner-vault", "y")
	require.ErrorIs(t, err, errXMRSigningClaimConflict)
	// Matching one clears it.
	_, ok, err = store.CompleteObserved("lonely", "owner-vault", "x")
	require.NoError(t, err)
	require.False(t, ok)
	_, err = db.Get([]byte(signedTxCommittedAliasKey("lonely")), nil)
	require.ErrorIs(t, err, leveldb.ErrNotFound)
}

func TestSignedTxPayloadStoreBroadcastCov(t *testing.T) {
	t.Parallel()

	t.Run("missing-claim", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		_, err := broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorContains(t, err, "get xmr claim for broadcast authorization")
		signedStorePutCov(t, db, signedTxPayloadClaimKey("logical"), "{")
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorContains(t, err, "decode xmr signed tx claim")
	})
	t.Run("logical-quarantined", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		signedStorePutCov(t, db, signedTxQuarantinedLogicalKey("logical"), "q")
		_, err := broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	})
	t.Run("alias-quarantined", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
		signedStorePutCov(t, db, signedTxQuarantinedAliasKey("observe-key"), "q")
		_, err := broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	})
	t.Run("identity-mismatch-and-unsigned", func(t *testing.T) {
		store, _ := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		_, err := broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimConflict, "reserved claim has no signed hash")
		require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))
		_, err = broadcastCov(store, plan, "other", func() (string, error) { return "other", nil })
		require.ErrorIs(t, err, errXMRSigningClaimConflict)
	})
	t.Run("outcomes", func(t *testing.T) {
		store, db := newSignedStoreCov(t)
		plan := standaloneClaimPlan("logical")
		require.NoError(t, store.Reserve(plan))
		require.NoError(t, store.Set(plan, "tx", standalonePayload("tx")))

		// Different committed alias.
		signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("other"))
		_, err := broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimAliasCommitted)
		// Same committed alias: success without dispatch.
		signedStorePutCov(t, db, signedTxCommittedAliasKey("observe-key"), signedStoreTombstoneCov("tx"))
		hash, err := broadcastCov(store, plan, "tx", func() (string, error) { t.Fatal("must not dispatch"); return "", nil })
		require.NoError(t, err)
		require.Equal(t, "tx", hash)
		require.NoError(t, db.Delete([]byte(signedTxCommittedAliasKey("observe-key")), nil))

		// Tombstone with a different tx.
		signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), signedStoreTombstoneCov("other"))
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimObserved)
		signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), "{")
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorContains(t, err, "decode observed xmr logical tombstone")
		signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
		hash, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.NoError(t, err)
		require.Equal(t, "tx", hash)
		require.NoError(t, db.Delete([]byte(signedTxObservedLogicalKey("logical")), nil))

		// Committed logical with a different tx.
		signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), signedStoreTombstoneCov("other"))
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimCommitted)
		require.NoError(t, db.Delete([]byte(signedTxCommittedLogicalKey("logical")), nil))

		// Persisted attempt that differs from the claim.
		signedStorePutCov(t, db, signedTxBroadcastAttemptKey("logical"), signedTxBroadcastAttempt{
			Version: 1, Token: "t", PayloadKey: signedTxPayloadPrefix + "x", LogicalHash: "logical", OwnerVault: "o",
			TxHash: "tx", MsgID: "m", PlanHash: "p",
		})
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRSigningClaimConflict)
		require.NoError(t, db.Delete([]byte(signedTxBroadcastAttemptKey("logical")), nil))

		// Token generation returning an empty token.
		store.newBroadcastToken = func() (string, error) { return " ", nil }
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorContains(t, err, "token is empty")
		store.newBroadcastToken = nil
		store.activeBroadcasts = nil

		// Broadcast error and hash mismatch.
		boom := errors.New("boom")
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "", boom })
		require.ErrorIs(t, err, boom)
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "different", nil })
		require.ErrorIs(t, err, errXMRSigningClaimConflict)

		// Quarantine/claim changes racing the dispatch.
		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			signedStorePutCov(t, db, signedTxQuarantinedLogicalKey("logical"), "tx")
			return "tx", nil
		})
		require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
		require.NoError(t, db.Delete([]byte(signedTxQuarantinedLogicalKey("logical")), nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			signedStorePutCov(t, db, signedTxObservedLogicalKey("logical"), signedStoreTombstoneCov("tx"))
			return "tx", nil
		})
		require.NoError(t, err)
		_, err = db.Get([]byte(signedTxBroadcastAttemptKey("logical")), nil)
		require.ErrorIs(t, err, leveldb.ErrNotFound, "completed attempt is cleared")
		require.NoError(t, db.Delete([]byte(signedTxObservedLogicalKey("logical")), nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			signedStorePutCov(t, db, signedTxCommittedLogicalKey("logical"), signedStoreTombstoneCov("other"))
			return "tx", nil
		})
		require.ErrorIs(t, err, errXMRSigningClaimCommitted)
		require.NoError(t, db.Delete([]byte(signedTxCommittedLogicalKey("logical")), nil))

		claimValue, err := db.Get([]byte(signedTxPayloadClaimKey("logical")), nil)
		require.NoError(t, err)
		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			signedStorePutCov(t, db, signedTxPayloadClaimKey("logical"), "{")
			return "tx", nil
		})
		require.ErrorContains(t, err, "decode xmr signed tx claim")
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorContains(t, err, "decode xmr signed tx claim")
		require.NoError(t, db.Put([]byte(signedTxPayloadClaimKey("logical")), claimValue, nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			require.NoError(t, db.Delete([]byte(signedTxPayloadClaimKey("logical")), nil))
			return "tx", nil
		})
		require.ErrorContains(t, err, "get xmr claim after broadcast")
		require.NoError(t, db.Put([]byte(signedTxPayloadClaimKey("logical")), claimValue, nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			changed := signedTxPayloadClaim{}
			require.NoError(t, json.Unmarshal(claimValue, &changed))
			changed.MsgID = "changed"
			signedStorePutCov(t, db, signedTxPayloadClaimKey("logical"), changed)
			return "tx", nil
		})
		require.ErrorIs(t, err, errXMRSigningClaimConflict)
		require.NoError(t, db.Put([]byte(signedTxPayloadClaimKey("logical")), claimValue, nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			require.NoError(t, db.Delete([]byte(signedTxBroadcastAttemptKey("logical")), nil))
			return "tx", nil
		})
		require.ErrorIs(t, err, errXMRBroadcastAttemptStale)

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			signedStorePutCov(t, db, signedTxBroadcastAttemptKey("logical"), "{")
			return "tx", nil
		})
		require.ErrorContains(t, err, "decode xmr broadcast attempt")
		require.NoError(t, db.Delete([]byte(signedTxBroadcastAttemptKey("logical")), nil))

		_, err = broadcastCov(store, plan, "tx", func() (string, error) {
			value, getErr := db.Get([]byte(signedTxBroadcastAttemptKey("logical")), nil)
			require.NoError(t, getErr)
			var attempt signedTxBroadcastAttempt
			require.NoError(t, json.Unmarshal(value, &attempt))
			attempt.MsgID = "changed"
			signedStorePutCov(t, db, signedTxBroadcastAttemptKey("logical"), attempt)
			return "tx", nil
		})
		require.ErrorIs(t, err, errXMRSigningClaimConflict)

		// Already in progress for the same logical hash.
		require.NoError(t, db.Delete([]byte(signedTxBroadcastAttemptKey("logical")), nil))
		store.mu.Lock()
		store.activeBroadcasts["logical"] = "busy"
		store.mu.Unlock()
		_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
		require.ErrorIs(t, err, errXMRBroadcastAlreadyInProgress)
	})
}

func TestSignedTxPayloadStoreReleaseReservedIndexCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	plan.ObserveKeys = []string{"a", "b"}
	plan.ObserveEpoch = map[string]uint64{"a": 0, "b": 0}
	require.NoError(t, store.Reserve(plan))
	// Alias b was reassigned to a different logical; release keeps it.
	signedStorePutCov(t, db, signedTxPayloadObserveKey("b"), "other")
	require.NoError(t, store.ReleaseReserved(plan))
	v, err := db.Get([]byte(signedTxPayloadObserveKey("b")), nil)
	require.NoError(t, err)
	require.Equal(t, "other", string(v))
	_, err = db.Get([]byte(signedTxPayloadObserveKey("a")), nil)
	require.ErrorIs(t, err, leveldb.ErrNotFound)
	_, ok, err := store.Claim("logical")
	require.NoError(t, err)
	require.False(t, ok)
	// Releasing a non-existent reservation is a no-op.
	require.NoError(t, store.ReleaseReserved(plan))
}

func TestSignedTxPayloadStoreClosedDBCov(t *testing.T) {
	t.Parallel()

	store, db := newSignedStoreCov(t)
	plan := standaloneClaimPlan("logical")
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, db.Close())

	_, err := store.SnapshotObserveEpochs([]string{"a"})
	require.Error(t, err)
	require.Error(t, store.Reserve(plan))
	require.Error(t, store.ReleaseReserved(plan))
	require.Error(t, store.QuarantineReverted("tx"))
	_, err = store.IsLocallySignedTx("tx")
	require.Error(t, err)
	require.Error(t, store.Set(plan, "tx", standalonePayload("tx")))
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.Error(t, err)
	_, _, err = store.ActiveClaim(plan.ObserveKeys, plan.OwnerVault)
	require.Error(t, err)
	_, _, err = store.Claim("logical")
	require.Error(t, err)
	_, _, err = store.CommitUnfinalizedLogical("logical", plan.ObserveKeys, "tx")
	require.Error(t, err)
	_, _, err = store.CommitUnfinalizedExact("observe-key", "owner-vault", "tx")
	require.Error(t, err)
	_, _, err = store.ImportVerifiedExact("observe-key", "owner-vault", "tx")
	require.Error(t, err)
	_, _, err = store.CompleteObservedLogical("logical", plan.ObserveKeys, "tx")
	require.Error(t, err)
	_, _, err = store.CompleteObservedExact("observe-key", "owner-vault", "tx")
	require.Error(t, err)
	_, err = broadcastCov(store, plan, "tx", func() (string, error) { return "tx", nil })
	require.Error(t, err)
	_, _, err = store.Get("a", "b")
	require.Error(t, err)
}
