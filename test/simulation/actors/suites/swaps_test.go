package suites

import (
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestBootstrapCountsUniquePools(t *testing.T) {
	scheduled := map[string]struct{}{common.NAMI.String(): {}}
	for _, chain := range acommon.SimChains {
		scheduled[chain.GetGasAsset().String()] = struct{}{}
		if chain.IsEVM() && !chain.Equals(common.BSCChain) {
			for asset := range evm.Tokens(chain) {
				scheduled[asset.String()] = struct{}{}
			}
		}
	}
	var allPools []openapi.Pool
	for asset := range scheduled {
		allPools = append(allPools, openapi.Pool{Asset: asset, Status: "Available"})
	}

	for _, tc := range []struct {
		name     string
		existing []openapi.Pool
	}{
		{name: "fresh mocknet"},
		{name: "preseeded NAMI", existing: []openapi.Pool{{Asset: common.NAMI.String(), Status: "Available"}}},
		{name: "all scheduled pools already exist", existing: allPools},
		{name: "unrelated existing pool", existing: []openapi.Pool{{Asset: "THOR.EXTRA", Status: "Available"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finalPools := append([]openapi.Pool(nil), allPools...)
			for _, pool := range tc.existing {
				if _, exists := scheduled[pool.Asset]; !exists {
					finalPools = append(finalPools, pool)
				}
			}
			calls := 0
			a := bootstrap(rand.New(rand.NewPCG(1, 2)), func() ([]openapi.Pool, error) {
				calls++
				if calls == 1 {
					return tc.existing, nil
				}
				return finalPools, nil
			})
			var verify *Actor
			a.WalkDepthFirst(func(actor *Actor) bool {
				if actor.Name == "Bootstrap-Verify" {
					verify = actor
				}
				return true
			})
			if verify == nil {
				t.Fatal("missing bootstrap verification actor")
			}
			if result := verify.Ops[0](nil); result.Error != nil || !result.Finish {
				t.Fatalf("bootstrap verification failed: %+v", result)
			}
			finalPools = finalPools[:len(finalPools)-1]
			if result := verify.Ops[0](nil); result.Error == nil {
				t.Fatal("bootstrap verification accepted a missing pool")
			}
		})
	}
}

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
