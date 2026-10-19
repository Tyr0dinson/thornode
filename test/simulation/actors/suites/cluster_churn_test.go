package suites

import (
	rand "math/rand/v2"
	"slices"
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

func TestClusterChurnCompletesCleanupBeforeNextStage(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	root := NewActor("root", rng)
	root.Append(ClusterChurn(rng))
	nextStage := NewActor("next-stage", rng)
	root.Append(nextStage)
	root.InitRoot()

	var stages []string
	root.WalkDepthFirst(func(actor *Actor) bool {
		stages = append(stages, actor.Name)
		return true
	})
	churnOut := slices.Index(stages, "BondYield-ChurnOut")
	cleanup := slices.Index(stages, "BondYield-Cleanup")
	if churnOut < 0 || cleanup <= churnOut {
		t.Fatalf("expected payout verification followed by cleanup: %v", stages)
	}
	if slices.Contains(stages, "BondYield-ChurnIn") {
		t.Fatalf("unexpected extra validator restoration churn: %v", stages)
	}
	if len(nextStage.Parents()) != 1 {
		t.Fatalf("expected one cleanup dependency, got %d", len(nextStage.Parents()))
	}
	for parent := range nextStage.Parents() {
		if parent.Name != "Mimir(all) ChurnInterval=60" {
			t.Fatalf("next stage can run before the final Mimir is restored: parent=%s", parent.Name)
		}
	}
}

func TestClusterChurnChecksSPLMigrationAroundChurnOut(t *testing.T) {
	a := ClusterChurn(rand.New(rand.NewPCG(5, 6)))
	a.InitRoot()

	actors := map[string]*Actor{}
	a.WalkDepthFirst(func(actor *Actor) bool {
		actors[actor.Name] = actor
		return true
	})
	// runsAfter reports whether later is a descendant of earlier.
	runsAfter := func(later, earlier string) bool {
		e, ok := actors[earlier]
		if !ok {
			t.Fatalf("ClusterChurn has no %s actor", earlier)
		}
		found := false
		e.WalkDepthFirst(func(actor *Actor) bool {
			if actor != e && actor.Name == later {
				found = true
			}
			return !found
		})
		return found
	}
	if !runsAfter("BondYield-ChurnOut", "SPLPreChurnOut") {
		t.Fatal("expected the SPL snapshot to run before the churn-out")
	}
	if !runsAfter("SPLPostChurnOut", "BondYield-ChurnOut") {
		t.Fatal("expected the SPL migration check to run after the churn-out")
	}
	// It runs beside the cleanup and must finish before the cleanup's final Mimir
	// restore, which the next stage waits on.
	actors["BondYield-Cleanup"].WalkDepthFirst(func(actor *Actor) bool {
		if len(actor.Children) == 0 && !runsAfter(actor.Name, "SPLPostChurnOut") {
			t.Fatalf("expected %s to wait for the SPL migration check", actor.Name)
		}
		return true
	})
}
