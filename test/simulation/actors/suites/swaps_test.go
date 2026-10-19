package suites

import (
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/solana"
)

func TestSwapsAlwaysScheduleBothZECAddressFormats(t *testing.T) {
	for seed := uint64(0); seed < 10; seed++ {
		names := actorNames(Swaps(rand.New(rand.NewPCG(seed, 42))))
		for _, format := range []string{"transparent", "TEX"} {
			found := false
			for name := range names {
				if strings.HasSuffix(name, " => ZEC.ZEC ("+format+")") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("seed %d has no required ZEC swap with %s destination", seed, format)
			}
		}
	}
}

func TestSwapsScheduleSPLCoverage(t *testing.T) {
	for asset := range solana.Tokens() {
		for seed := uint64(0); seed < 10; seed++ {
			names := actorNames(Swaps(rand.New(rand.NewPCG(seed, 42))))
			for _, want := range []string{
				// the pool rule for Solana assets always swaps them against BTC
				"Swap " + asset.String() + " => BTC.BTC",
				"Swap BTC.BTC => " + asset.String(),
				"Swap BTC.BTC => " + asset.String() + " (fresh SPL recipient)",
				"SPL refund " + asset.String(),
			} {
				if _, ok := names[want]; !ok {
					t.Fatalf("seed %d does not schedule %q", seed, want)
				}
			}
		}
	}
}
