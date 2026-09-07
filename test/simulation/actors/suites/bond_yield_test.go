package suites

import (
	rand "math/rand/v2"
	"testing"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestBondYieldRestoresMimirsAfterChurnOut(t *testing.T) {
	a := BondYield(rand.New(rand.NewPCG(5, 6)))
	a.InitRoot()

	var cleanup *Actor
	a.WalkDepthFirst(func(actor *Actor) bool {
		if actor.Name == "BondYield-Cleanup" {
			cleanup = actor
		}
		return true
	})
	if cleanup == nil {
		t.Fatal("expected Bond Yield suite to include Mimir cleanup")
	}

	for parent := range cleanup.Parents() {
		if parent.Name == "BondYield-ChurnOut" {
			return
		}
	}
	t.Fatal("expected Bond Yield Mimir cleanup to run after churn-out verification")
}
