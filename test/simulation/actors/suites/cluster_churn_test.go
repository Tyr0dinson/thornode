package suites

import (
	rand "math/rand/v2"
	"testing"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestClusterChurnRestoresMimirsAfterChurnOut(t *testing.T) {
	a := ClusterChurn(rand.New(rand.NewPCG(5, 6)))
	if a.Name != "ClusterChurn" {
		t.Fatalf("unexpected suite name: %s", a.Name)
	}
	a.InitRoot()

	var cleanup *Actor
	a.WalkDepthFirst(func(actor *Actor) bool {
		if actor.Name == "BondYield-Cleanup" {
			cleanup = actor
		}
		return true
	})
	if cleanup == nil {
		t.Fatal("expected ClusterChurn suite to include Mimir cleanup")
	}

	for parent := range cleanup.Parents() {
		if parent.Name == "BondYield-ChurnOut" {
			return
		}
	}
	t.Fatal("expected Bond Yield Mimir cleanup to run after churn-out verification")
}
