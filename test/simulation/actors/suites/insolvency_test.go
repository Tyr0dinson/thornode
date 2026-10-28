package suites

import (
	"fmt"
	rand "math/rand/v2"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
)

func TestInsolvencyExcludesFROSTVaultAndKeepsOtherChains(t *testing.T) {
	previous := acommon.SimChains
	defer func() { acommon.SimChains = previous }()

	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("xmr_enabled_%v", enabled), func(t *testing.T) {
			acommon.SetXMRChainEnabled(enabled)
			names := actorNames(Insolvency(rand.New(rand.NewPCG(1, 2))))
			if names["Insolvency-XMR"] {
				t.Fatal("direct-signing insolvency suite must not schedule a FROST vault spend")
			}
			for _, chain := range acommon.SimChains {
				if chain.Equals(common.XMRChain) {
					continue
				}
				if name := fmt.Sprintf("Insolvency-%s", chain); !names[name] {
					t.Fatalf("missing supported chain actor %q", name)
				}
			}
			for _, name := range []string{"Insolvency-POL", "Insolvency-ZEC", "Insolvency-Watcher-Reset"} {
				if !names[name] {
					t.Fatalf("XMR exclusion must retain later suite step %q", name)
				}
			}
			if got := acommon.XMRChainEnabled(); got != enabled {
				t.Fatalf("suite changed global XMR availability to %v, want %v", got, enabled)
			}
		})
	}
}
