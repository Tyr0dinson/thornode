package dag

import (
	rand "math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// newUserCov builds a real User without a live chain: NewUser only needs a
// chain id in the bifrost config, which is restored once the test finishes.
func newUserCov(t *testing.T) *User {
	t.Helper()
	t.Cleanup(config.Init) // runs after t.Setenv restores the environment
	t.Setenv("CHAIN_ID", "thorchain")
	config.Init()
	return NewUser("dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil", nil)
}

func TestExecuteWaitsForAvailableUserCov(t *testing.T) {
	t.Setenv("DAG_TICK_INTERVAL", "10ms")
	rng := rand.New(rand.NewPCG(7, 8))
	user := newUserCov(t)
	if !user.Acquire() {
		t.Fatal("acquire")
	}

	var ran atomic.Bool
	root := NewActor("root", rng)
	child := NewActor("child", rng)
	child.Ops = []Op{func(*OpConfig) OpResult {
		ran.Store(true)
		return OpResult{Finish: true}
	}}
	root.Children[child] = true

	done := make(chan struct{})
	go func() {
		Execute(&OpConfig{Users: []*User{user}}, root, 1, rng)
		close(done)
	}()

	// all users locked: the ready actor must not be scheduled
	time.Sleep(60 * time.Millisecond)
	if ran.Load() {
		t.Fatal("actor ran while no user was available")
	}
	select {
	case <-done:
		t.Fatal("execute returned while work remained")
	default:
	}

	user.Release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not finish after user release")
	}
	if !ran.Load() || !child.Finished() {
		t.Fatal("child must have run once a user became available")
	}
}

func TestExecuteInvalidTickIntervalFallsBackCov(t *testing.T) {
	for _, v := range []string{"100", "5ms"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("DAG_TICK_INTERVAL", v)
			rng := rand.New(rand.NewPCG(9, 10))
			root := NewActor("root", rng)
			child := NewActor("child", rng)
			child.Ops = []Op{func(*OpConfig) OpResult { return OpResult{Finish: true} }}
			root.Children[child] = true

			start := time.Now()
			Execute(nil, root, 1, rng)
			if !child.Finished() {
				t.Fatal("child must finish")
			}
			// default 100ms tick: at least two ticks (schedule + observe finish)
			if el := time.Since(start); el < 150*time.Millisecond {
				t.Fatalf("expected default 100ms tick to be used, finished in %s", el)
			}
		})
	}
}
