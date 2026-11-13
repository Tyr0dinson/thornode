package solana

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPrewarmConsensusBlockHashWarmsCache proves the pipeline-shortening prewarm fills
// the consensus-blockhash memo off the keysign path: after it runs, the height is a
// cache hit, so the subsequent keysign-path derivation costs no RPC. This is the half
// of the SOL "Blockhash not found" fix that trims the galloping getBlocks/getBlockTime
// derivation out of the anchor->broadcast window the recent-blockhash path must fit
// inside the ~150-block blockhash lifetime.
func TestPrewarmConsensusBlockHashWarmsCache(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())

	// Not cached before the prewarm.
	_, ok := client.lookupConsensusBlockHash(100)
	require.False(t, ok)

	// The prewarm is fire-and-forget (not wg-tracked), so poll the memo until the
	// background derivation lands.
	client.prewarmConsensusBlockHash(100)
	require.Eventually(t, func() bool {
		_, ok := client.lookupConsensusBlockHash(100)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "prewarm must populate the consensus-blockhash memo")

	e, _ := client.lookupConsensusBlockHash(100)
	require.Equal(t, mockBlockhashForSlot(950), e.hash)

	warmSlots := m.count("getSlot")
	require.Equal(t, 1, warmSlots)

	// The keysign-path call is now a pure cache hit: no extra RPC.
	hash, slot, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Equal(t, int64(950), slot)
	require.Equal(t, warmSlots, m.count("getSlot"), "keysign-path call after prewarm must be a cache hit")

	// Guards: non-positive height is a no-op, and a second prewarm for an already-cached
	// height short-circuits before spawning a goroutine, so nothing derives again.
	client.prewarmConsensusBlockHash(0)
	client.prewarmConsensusBlockHash(-5)
	client.prewarmConsensusBlockHash(100) // already cached
	require.Never(t, func() bool {
		return m.count("getSlot") != warmSlots
	}, 200*time.Millisecond, 20*time.Millisecond, "no-op/cached prewarm must not derive again")
}

// TestPrewarmConsensusBlockHashDoesNotCacheAbstention proves a prewarm that cannot yet
// derive (node not finalized past target) leaves the memo empty, so the keysign-path
// call re-derives exactly as it would without the prewarm — the optimisation never
// strands an outbound on a transiently-missing hash.
func TestPrewarmConsensusBlockHashDoesNotCacheAbstention(t *testing.T) {
	// A node whose newest finalized timestamp only equals target is not a strict upper
	// witness -> consensusBlockHash abstains ("",0,nil), which is never memoized.
	sc := happyConsensusScenario()
	sc.tip = 950 // newest == selected slot: no strict upper witness -> abstain
	client, m := newConsensusCacheClient(t, sc)

	client.prewarmConsensusBlockHash(100)
	// First prove the fire-and-forget prewarm actually launched a derivation — its first
	// RPC is GetSlot — so the test is not vacuously green if prewarmConsensusBlockHash
	// were a no-op. (The test thread must NOT drive this itself: it has made no derivation
	// call yet, so a non-zero count is attributable only to the prewarm goroutine.)
	require.Eventually(t, func() bool { return m.count("getSlot") >= 1 }, 2*time.Second, 5*time.Millisecond,
		"prewarm must launch a derivation")
	// Now synchronise on completion: cachedConsensusBlockHash joins the in-flight prewarm
	// via singleflight (or re-derives if it already finished) and returns only once a
	// derivation has completed through the store decision, so the abstention assertions
	// below cannot race it. The abstention result is ("",0,nil) and must NOT be memoized.
	hash, slot, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Empty(t, hash)
	require.Zero(t, slot)

	_, ok := client.lookupConsensusBlockHash(100)
	require.False(t, ok, "an abstaining derivation must not be cached by the prewarm")
}
