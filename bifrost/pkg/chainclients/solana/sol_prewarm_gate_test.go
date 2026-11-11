package solana

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPrewarmConsensusBlockHashCap covers solMaxOutstandingPrewarms: at the cap the
// prewarm must launch no new derivation (so a stalled GetBlockTimestamp cannot leak one
// worker per boundary), and below the cap it launches and the in-flight counter returns
// to zero once the derivation completes.
func TestPrewarmConsensusBlockHashCap(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())

	// At the cap: no derivation, counter untouched.
	client.prewarmInFlight.Store(solMaxOutstandingPrewarms)
	client.prewarmConsensusBlockHash(100)
	require.Never(t, func() bool { return m.count("getSlot") > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"at the cap, prewarm must not launch a derivation")
	require.Equal(t, solMaxOutstandingPrewarms, client.prewarmInFlight.Load(), "counter unchanged when capped")

	// Below the cap: it launches, and the counter returns to 0 after the derivation ends.
	client.prewarmInFlight.Store(0)
	client.prewarmConsensusBlockHash(101)
	require.Eventually(t, func() bool {
		return m.count("getSlot") >= 1 && client.prewarmInFlight.Load() == 0
	}, 2*time.Second, 5*time.Millisecond, "below the cap: derive, then the in-flight counter returns to 0")
}
