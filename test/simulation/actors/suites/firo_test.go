package suites

import (
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestStandardSuitesIncludeFIRO(t *testing.T) {
	for _, tc := range []struct {
		name           string
		build          func(*rand.Rand) *Actor
		prefix, suffix string
	}{
		{"swap in", Swaps, "Swap FIRO.FIRO => ", ""},
		{"swap out", Swaps, "Swap ", " => FIRO.FIRO"},
		{"memoless swap", MemolessSwaps, "SwapMemoless FIRO.FIRO => ", ""},
		{"inactive vault refund", features.InactiveVaultRefunds, "InactiveVaultRefund-FIRO.FIRO", ""},
		{"consolidation", features.Consolidate, "Feature-Consolidate-FIRO.FIRO", ""},
		{"ragnarok", Ragnarok, "Ragnarok-FIRO.FIRO", ""},
	} {
		found := false
		for name := range actorNames(tc.build(rand.New(rand.NewPCG(1, 2)))) {
			if strings.HasPrefix(name, tc.prefix) && strings.HasSuffix(name, tc.suffix) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: missing FIRO coverage", tc.name)
		}
	}
}
