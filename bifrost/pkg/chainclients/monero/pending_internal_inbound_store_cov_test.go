package monero

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

func newReplayStoreCov(t *testing.T) (*authenticatedInternalInboundReplayStore, *leveldb.DB) {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return newAuthenticatedInternalInboundReplayStore(db), db
}

func quarantineReplayCov(t *testing.T, db *leveldb.DB, txID string) {
	t.Helper()
	require.NoError(t, db.Put([]byte(scannerRevertedTxPrefix+normalizeTxID(txID)), []byte("1"), nil))
}

func TestAuthenticatedInternalInboundReplayNilStoreCov(t *testing.T) {
	var s *authenticatedInternalInboundReplayStore
	require.ErrorContains(t, s.ResetClaims(), "not initialized")
	require.ErrorContains(t, s.Enqueue(standaloneReplayRecord(t, "nil", 1)), "not initialized")
	claims, err := s.Claim(1)
	require.NoError(t, err)
	require.Nil(t, claims)
	_, err = s.Relocate(authenticatedInternalInboundClaim{Token: "1"}, 1, "", "")
	require.ErrorContains(t, err, "not initialized")
	require.NoError(t, s.Release(authenticatedInternalInboundClaim{Token: "1"}))
	n, err := s.Count()
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestAuthenticatedInternalInboundReplayValidationCov(t *testing.T) {
	base := standaloneReplayRecord(t, "validate", 10)
	require.NoError(t, validateAuthenticatedInternalInboundReplay(base))

	other := standaloneReplayRecord(t, "validate-other", 10)
	cases := map[string]struct {
		mutate func(r *authenticatedInternalInboundReplay)
		want   string
	}{
		"txid":         {func(r *authenticatedInternalInboundReplay) { r.TxID = "zz" }, "tx hash"},
		"txid-nonhex":  {func(r *authenticatedInternalInboundReplay) { r.TxID = strings.Repeat("z", 64) }, "tx hash"},
		"txkey":        {func(r *authenticatedInternalInboundReplay) { r.TxKey = "" }, "tx key"},
		"blockhash":    {func(r *authenticatedInternalInboundReplay) { r.BlockHash = "" }, "block hash"},
		"prevhash":     {func(r *authenticatedInternalInboundReplay) { r.PreviousHash = "" }, "previous block hash"},
		"height":       {func(r *authenticatedInternalInboundReplay) { r.BlockHeight = 0 }, "invalid block height"},
		"amount":       {func(r *authenticatedInternalInboundReplay) { r.AmountThorchain = 0 }, "zero amount"},
		"fee":          {func(r *authenticatedInternalInboundReplay) { r.FeeThorchain = 0 }, "zero fee"},
		"no-refs":      {func(r *authenticatedInternalInboundReplay) { r.SpentOutputRefs = nil }, "spent-ref count"},
		"ref-txhash":   {func(r *authenticatedInternalInboundReplay) { r.SpentOutputRefs[0].TxHash = "" }, "spent-ref tx hash"},
		"ref-keyimage": {func(r *authenticatedInternalInboundReplay) { r.SpentOutputRefs[0].KeyImage = "" }, "spent-ref key image"},
		"dup-ref": {func(r *authenticatedInternalInboundReplay) {
			dup := r.SpentOutputRefs[0]
			dup.KeyImage = standaloneReplayHash("other-ki")
			r.SpentOutputRefs = append(r.SpentOutputRefs, dup)
		}, "duplicate spent ref"},
		"dup-ki": {func(r *authenticatedInternalInboundReplay) {
			dup := r.SpentOutputRefs[0]
			dup.OutputIndex = 9
			r.SpentOutputRefs = append(r.SpentOutputRefs, dup)
		}, "duplicate spent-ref key image"},
		"lease":       {func(r *authenticatedInternalInboundReplay) { r.ClaimToken = "1" }, "inconsistent claim lease"},
		"lease-neg":   {func(r *authenticatedInternalInboundReplay) { r.ClaimedAtUnix = -1 }, "inconsistent claim lease"},
		"memo":        {func(r *authenticatedInternalInboundReplay) { r.Memo = "SWAP:BTC.BTC" }, "invalid migration memo"},
		"source":      {func(r *authenticatedInternalInboundReplay) { r.SourceVaultPubKey = "bogus" }, "invalid source vault"},
		"source-addr": {func(r *authenticatedInternalInboundReplay) { r.SourceAddress = other.SourceAddress }, "source address does not match"},
		"dest":        {func(r *authenticatedInternalInboundReplay) { r.DestinationVaultPubKey = "" }, "invalid destination vault"},
		"dest-addr":   {func(r *authenticatedInternalInboundReplay) { r.ToAddress = other.ToAddress }, "destination address does not match"},
		"same-vault": {func(r *authenticatedInternalInboundReplay) {
			r.DestinationVaultPubKey = r.SourceVaultPubKey
			r.ToAddress = r.SourceAddress
		}, "identical"},
	}
	for name, tc := range cases {
		r := standaloneReplayRecord(t, "validate", 10)
		tc.mutate(&r)
		err := validateAuthenticatedInternalInboundReplay(r)
		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestAuthenticatedInternalSpentRefsEqualCov(t *testing.T) {
	a := authenticatedInternalSpentRef{TxHash: "AA", OutputIndex: 1, KeyImage: "K1"}
	b := authenticatedInternalSpentRef{TxHash: "aa", OutputIndex: 0, KeyImage: "k0"}
	c := authenticatedInternalSpentRef{TxHash: "bb", OutputIndex: 0, KeyImage: "k2"}

	require.False(t, authenticatedInternalSpentRefsEqual([]authenticatedInternalSpentRef{a}, nil))
	// Order and case insensitive.
	require.True(t, authenticatedInternalSpentRefsEqual(
		[]authenticatedInternalSpentRef{c, a, b},
		[]authenticatedInternalSpentRef{
			{TxHash: " aa ", OutputIndex: 0, KeyImage: "K0"},
			{TxHash: "BB", OutputIndex: 0, KeyImage: "K2"},
			{TxHash: "aa", OutputIndex: 1, KeyImage: "k1"},
		}))
	require.False(t, authenticatedInternalSpentRefsEqual(
		[]authenticatedInternalSpentRef{a, b},
		[]authenticatedInternalSpentRef{a, c}))
}

func TestAuthenticatedInternalInboundReplayLifecycleCov(t *testing.T) {
	store, db := newReplayStoreCov(t)
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }

	bad := standaloneReplayRecord(t, "bad", 1)
	bad.AmountThorchain = 0
	require.ErrorContains(t, store.Enqueue(bad), "zero amount")

	rec := standaloneReplayRecord(t, "life", 20)
	require.NoError(t, store.Enqueue(rec))
	// Idempotent retry.
	require.NoError(t, store.Enqueue(rec))
	// Conflicting identity.
	changed := rec
	changed.AmountThorchain++
	require.ErrorContains(t, store.Enqueue(changed), "changed exact identity")

	// Claim with an out-of-range limit is clamped to the max (1).
	claims, err := store.Claim(0)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	claim := claims[0]
	require.NotEmpty(t, claim.Token)

	// Still leased: nothing to claim.
	claims, err = store.Claim(5)
	require.NoError(t, err)
	require.Empty(t, claims)

	// Relocate errors.
	_, err = store.Relocate(authenticatedInternalInboundClaim{Record: rec}, 21, "", "")
	require.ErrorContains(t, err, "token is empty")
	_, err = store.Relocate(authenticatedInternalInboundClaim{Record: rec, Token: "stale"}, 21,
		standaloneReplayHash("b"), standaloneReplayHash("p"))
	require.ErrorContains(t, err, "claim changed during relocation")
	_, err = store.Relocate(claim, 21, "bad", standaloneReplayHash("p"))
	require.ErrorContains(t, err, "block hash")
	missing := authenticatedInternalInboundClaim{Record: standaloneReplayRecord(t, "missing", 3), Token: "1"}
	_, err = store.Relocate(missing, 21, standaloneReplayHash("b"), standaloneReplayHash("p"))
	require.ErrorIs(t, err, leveldb.ErrNotFound)

	// Successful relocation keeps the token and normalises hashes.
	newHash := strings.ToUpper(standaloneReplayHash("relocated-block"))
	relocated, err := store.Relocate(claim, 25, " "+newHash+" ", standaloneReplayHash("relocated-parent"))
	require.NoError(t, err)
	require.Equal(t, claim.Token, relocated.Token)
	require.Equal(t, int64(25), relocated.Record.BlockHeight)
	require.Equal(t, strings.ToLower(newHash), relocated.Record.BlockHash)

	// finishClaim edge cases.
	require.ErrorContains(t, store.Release(authenticatedInternalInboundClaim{Record: rec}), "token is empty")
	require.NoError(t, store.Release(missing))
	require.NoError(t, store.Acknowledge(authenticatedInternalInboundClaim{Record: rec, Token: "stale"}))
	n, err := store.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n, "stale token must not delete the record")

	// Release returns it to the pool.
	require.NoError(t, store.Release(relocated))
	claims, err = store.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotEqual(t, claim.Token, claims[0].Token)

	// Lease expiry makes a claim re-claimable.
	now = now.Add(authenticatedInternalInboundClaimLease + time.Second)
	reclaimed, err := store.Claim(1)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)

	// ResetClaims clears the lease.
	require.NoError(t, store.ResetClaims())
	value, err := db.Get([]byte(rec.key()), nil)
	require.NoError(t, err)
	var stored authenticatedInternalInboundReplay
	require.NoError(t, json.Unmarshal(value, &stored))
	require.Empty(t, stored.ClaimToken)
	require.Zero(t, stored.ClaimedAtUnix)
	// No-op reset.
	require.NoError(t, store.ResetClaims())

	// Invalidate removes the claimed record.
	claims, err = store.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, store.Invalidate(claims[0]))
	n, err = store.Count()
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestAuthenticatedInternalInboundReplayRevertedCov(t *testing.T) {
	store, db := newReplayStoreCov(t)
	rec := standaloneReplayRecord(t, "reverted", 30)
	require.NoError(t, store.Enqueue(rec))
	claims, err := store.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	quarantineReplayCov(t, db, rec.TxID)
	require.ErrorContains(t, store.Enqueue(rec), "permanently reverted")
	_, err = store.Relocate(claims[0], 31, standaloneReplayHash("b"), standaloneReplayHash("p"))
	require.ErrorContains(t, err, "permanently reverted")

	// Claim prunes reverted records.
	got, err := store.Claim(1)
	require.NoError(t, err)
	require.Empty(t, got)
	n, err := store.Count()
	require.NoError(t, err)
	require.Zero(t, n)

	// ResetClaims prunes reverted records too.
	rec2 := standaloneReplayRecord(t, "reverted-2", 31)
	require.NoError(t, store.Enqueue(rec2))
	quarantineReplayCov(t, db, rec2.TxID)
	require.NoError(t, store.ResetClaims())
	n, err = store.Count()
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestAuthenticatedInternalInboundReplayCorruptCov(t *testing.T) {
	store, db := newReplayStoreCov(t)
	rec := standaloneReplayRecord(t, "corrupt", 40)

	// Undecodable value.
	require.NoError(t, db.Put([]byte(rec.key()), []byte("{"), nil))
	_, err := store.Count()
	require.ErrorContains(t, err, "decode authenticated internal inbound replay")
	require.ErrorContains(t, store.ResetClaims(), "decode")
	_, err = store.Claim(1)
	require.ErrorContains(t, err, "decode")
	require.ErrorContains(t, store.Enqueue(rec), "decode")
	require.ErrorContains(t, store.Release(authenticatedInternalInboundClaim{Record: rec, Token: "1"}), "decode")

	// Invalid record.
	invalid := rec
	invalid.FeeThorchain = 0
	encoded, err := json.Marshal(invalid)
	require.NoError(t, err)
	require.NoError(t, db.Put([]byte(rec.key()), encoded, nil))
	_, err = store.Count()
	require.ErrorContains(t, err, "validate authenticated internal inbound replay")

	// Valid record stored under the wrong key.
	encoded, err = json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, db.Delete([]byte(rec.key()), nil))
	require.NoError(t, db.Put([]byte(authenticatedInternalInboundReplayPrefix+strings.Repeat("0", 64)), encoded, nil))
	_, err = store.Count()
	require.ErrorContains(t, err, "key mismatch")
	// Enqueue of a different record must list and fail on the mismatched key.
	require.ErrorContains(t, store.Enqueue(standaloneReplayRecord(t, "other", 41)), "key mismatch")
}

func TestAuthenticatedInternalInboundReplayOutboxFullCov(t *testing.T) {
	store, db := newReplayStoreCov(t)
	template := standaloneReplayRecord(t, "full", 50)
	for i := 0; i < maxAuthenticatedInternalInboundReplays; i++ {
		r := template
		r.TxID = standaloneReplayHash("full-tx-" + string(rune('a'+i%26)) + strings.Repeat("x", i/26))
		encoded, err := json.Marshal(r)
		require.NoError(t, err)
		require.NoError(t, db.Put([]byte(r.key()), encoded, nil))
	}
	n, err := store.Count()
	require.NoError(t, err)
	require.Equal(t, maxAuthenticatedInternalInboundReplays, n)
	require.ErrorContains(t, store.Enqueue(standaloneReplayRecord(t, "overflow", 51)), "outbox is full")
}

func TestAuthenticatedInternalInboundReplayClosedDBCov(t *testing.T) {
	store, db := newReplayStoreCov(t)
	rec := standaloneReplayRecord(t, "closed", 60)
	require.NoError(t, store.Enqueue(rec))
	claims, err := store.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, db.Close())

	_, err = store.Count()
	require.ErrorContains(t, err, "list authenticated internal inbound replays")
	require.Error(t, store.Enqueue(standaloneReplayRecord(t, "closed-2", 61)))
	require.Error(t, store.Release(claims[0]))
	_, err = store.Relocate(claims[0], 61, standaloneReplayHash("b"), standaloneReplayHash("p"))
	require.Error(t, err)
}
