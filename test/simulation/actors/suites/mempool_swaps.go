package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// MempoolSwaps runs 14 scenarios in this fixed fail-fast order:
//
// base (7):
//   - happy-market - speculatively stages a regular `POL.POL -> BTC.BTC`
//     market swap and releases it after finality.
//   - affiliate-revshare-finality - with ordinary affiliate bps set to zero,
//     leaves the THORName REVSHARE collector unchanged during speculation and
//     applies the configured share only after finality.
//   - stream-settle-before-finality - completes and stages a short three-part
//     `POL.POL -> BTC.BTC` stream before finality.
//   - stream-finality-before-settlement - keeps a slower six-part
//     `POL.POL -> BTC.BTC` stream queued across finality and finishes it afterward.
//   - tor-cap-fallback - uses a low network TOR cap to exclude pre-finality
//     speculation, then completes the swap after finality.
//   - pool-cap-fallback - uses a low per-pool TOR cap to exclude pre-finality
//     speculation, then completes the swap after finality.
//   - timeout-rollback - rolls a staged swap back at v1's recovery cadence
//     before a later valid final observation completes normally.
//
// native (6):
//   - native-rune-nonstream - excludes a plain `POL.POL -> THOR.RUNE` swap
//     before finality, then applies one exact internal credit.
//   - native-rune-stream - reserves and stages a `POL.POL -> THOR.RUNE` stream
//     before finality, then applies one exact internal credit.
//   - native-synth-nonstream - excludes a plain `POL.POL -> BTC/BTC` synth swap
//     before finality, then applies one exact synth credit.
//   - native-synth-stream - reserves and stages a `POL.POL -> BTC/BTC` synth
//     stream before finality, then applies one exact synth credit.
//   - native-secured-nonstream - excludes a plain `POL.POL -> BTC-BTC` secured
//     swap before finality, then applies one exact secured credit.
//   - native-secured-stream - stages a `POL.POL -> BTC-BTC` secured stream with
//     `BTC.BTC` escrow, then applies one exact secured credit.
//
// memoless (1):
//   - MempoolSwap-MemolessReferenceExclusion - keeps a literal `r:<ref>` swap
//     outside speculative state and consumes it exactly once after finality.
//
// See test/simulation/README.md, section "Mempool swap lifecycle stage", for
// flows, assertions, setup, and simulation limitations.
func MempoolSwaps(rng *rand.Rand) *Actor {
	base := core.NewMempoolSwapSimulationActor(rng)
	base.Append(core.NewMempoolSwapNativeSimulationActor(rng))
	base.Append(core.NewMempoolSwapMemolessSimulationActor(rng))
	return base
}
