package dag

import (
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestExecute(t *testing.T) {
	// NewUser reads the Bifrost chain ID from the loaded config.
	t.Cleanup(config.Init) // t.Setenv restores CHAIN_ID before this cleanup runs.
	t.Setenv("CHAIN_ID", "thorchain-mocknet-v1")
	config.Init()

	rng := rand.New(rand.NewPCG(1, 2))

	// bump count once on each operation
	count := 0
	op := func(config *OpConfig) OpResult {
		count++
		return OpResult{Continue: true, Finish: true, Error: nil}
	}

	// create nodes
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	child3 := NewActor("child3", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)
	grandchild3 := NewActor("grandchild3", rng)

	// add operations
	descendants := []*Actor{child1, child2, child3, grandchild1, grandchild2, grandchild3}
	for _, node := range descendants {
		node.Ops = []Op{op}
	}

	// build dag
	root.Children[child1] = true
	root.Children[child2] = true
	root.Children[child3] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true
	child3.Children[grandchild3] = true

	// execute
	cfg := &OpConfig{
		Users: []*User{
			NewUser(strings.Repeat("dog ", 23)+"fossil", nil),
		},
	}
	Execute(cfg, root, 1, rng)

	// should have executed op 6 times
	if count != 6 {
		t.Fatalf("expected 6 operations, got %d", count)
	}
}

func TestExecuteStopsBackgroundActorsAtShutdown(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))

	childRan := make(chan struct{})
	executeDone := make(chan struct{})

	background := NewActor("background", rng)
	background.Interval = time.Hour
	child := NewActor("child", rng)
	root := NewActor("root", rng)

	background.Ops = []Op{
		func(config *OpConfig) OpResult {
			background.Background()
			return OpResult{Continue: true}
		},
		func(config *OpConfig) OpResult {
			return OpResult{}
		},
	}
	child.Ops = []Op{
		func(config *OpConfig) OpResult {
			close(childRan)
			return OpResult{Finish: true}
		},
	}

	root.Children[background] = true
	background.Children[child] = true

	go func() {
		Execute(nil, root, 2, rng)
		close(executeDone)
	}()

	select {
	case <-childRan:
	case <-time.After(5 * time.Second):
		t.Fatal("child actor never ran")
	}

	select {
	case <-executeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("execute waited for background actor to finish")
	}

	if !background.Finished() {
		t.Fatal("background actor was left running after shutdown")
	}
}

func TestExecuteBackgroundActorDoesNotConsumeParallelism(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))

	childRan := make(chan struct{})
	executeDone := make(chan struct{})

	background := NewActor("background", rng)
	background.Interval = time.Hour
	child := NewActor("child", rng)
	root := NewActor("root", rng)

	background.Ops = []Op{
		func(config *OpConfig) OpResult {
			background.Background()
			return OpResult{Continue: true}
		},
		func(config *OpConfig) OpResult {
			return OpResult{}
		},
	}
	child.Ops = []Op{
		func(config *OpConfig) OpResult {
			close(childRan)
			return OpResult{Finish: true}
		},
	}

	root.Children[background] = true
	background.Children[child] = true

	go func() {
		Execute(nil, root, 1, rng)
		close(executeDone)
	}()

	select {
	case <-childRan:
	case <-time.After(5 * time.Second):
		t.Fatal("child actor never ran after parent backgrounded")
	}

	select {
	case <-executeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("execute waited for background actor to release a foreground slot")
	}

	if !background.Finished() {
		t.Fatal("background actor was left running after shutdown")
	}
}

func TestExecuteBackgroundSurvivesForegroundTeardown(t *testing.T) {
	t.Setenv("DAG_TICK_INTERVAL", "1m")
	synctest.Test(t, func(t *testing.T) {
		rng := rand.New(rand.NewPCG(7, 8))
		root := NewActor("root", rng)
		background := NewActor("background", rng)
		background.Timeout = time.Hour
		background.Interval = time.Minute
		background.BackgroundUntilCanceled = true
		background.Ops = []Op{
			func(*OpConfig) OpResult { background.Background(); return OpResult{Continue: true} },
			func(*OpConfig) OpResult { return OpResult{} },
		}
		teardown := NewActor("teardown", rng)
		teardown.Timeout = 3 * time.Hour
		teardown.Interval = time.Minute
		var started time.Time
		teardown.Ops = []Op{func(*OpConfig) OpResult {
			if started.IsZero() {
				started = time.Now()
			}
			return OpResult{Finish: time.Since(started) > 2*time.Hour}
		}}
		root.Children[background] = true
		background.Children[teardown] = true
		if err := execute(nil, root, 1, rng); err != nil {
			t.Fatal(err)
		}
		if !background.Finished() || !teardown.Finished() {
			t.Fatal("simulation returned before actors stopped")
		}
	})
}

func TestExecutePreservesActorFailures(t *testing.T) {
	t.Setenv("DAG_TICK_INTERVAL", "10ms")
	for _, background := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(9, 10))
			root := NewActor("root", rng)
			a := NewActor("failure", rng)
			a.BackgroundUntilCanceled = true
			want := errors.New("terminal actor error")
			a.Ops = []Op{func(*OpConfig) OpResult {
				if background {
					a.Background()
					// Return an error after the scheduler has started shutdown.
					time.Sleep(time.Second)
				}
				return OpResult{Finish: true, Error: want}
			}}
			root.Children[a] = true
			if err := execute(nil, root, 1, rng); !errors.Is(err, want) {
				t.Fatalf("lost terminal error (background=%t): %v", background, err)
			}
		})
	}
}

func TestExecuteForegroundTimeoutStopsBackground(t *testing.T) {
	t.Setenv("DAG_TICK_INTERVAL", "10ms")
	synctest.Test(t, func(t *testing.T) {
		rng := rand.New(rand.NewPCG(11, 12))
		root := NewActor("root", rng)
		background := NewActor("background", rng)
		background.Interval = time.Hour
		background.BackgroundUntilCanceled = true
		background.Ops = []Op{func(*OpConfig) OpResult { background.Background(); return OpResult{} }}
		foreground := NewActor("foreground", rng)
		foreground.Timeout = time.Second
		foreground.Ops = []Op{func(*OpConfig) OpResult { return OpResult{} }}
		root.Children[background] = true
		background.Children[foreground] = true
		if err := execute(nil, root, 1, rng); !errors.Is(err, ErrTimeout) {
			t.Fatalf("lost foreground deadline: %v", err)
		}
		if !background.Finished() {
			t.Fatal("background actor leaked after foreground failure")
		}
	})
}

func TestExecuteBoundsShutdown(t *testing.T) {
	t.Setenv("DAG_TICK_INTERVAL", "10ms")
	for _, fail := range []bool{false, true} {
		name := "foreground completion"
		if fail {
			name = "foreground failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rng := rand.New(rand.NewPCG(13, 14))
				root := NewActor("root", rng)
				stuck := NewActor("stuck operation", rng)
				foreground := NewActor("foreground", rng)
				started := make(chan struct{})
				release := make(chan struct{})
				stuck.Ops = []Op{func(*OpConfig) OpResult {
					if !fail {
						stuck.Background()
					}
					close(started)
					<-release
					return OpResult{Finish: true}
				}}
				var want error
				if fail {
					want = errors.New("foreground failed while another op was stuck")
				}
				foreground.Ops = []Op{func(*OpConfig) OpResult {
					<-started
					return OpResult{Finish: true, Error: want}
				}}
				root.Children[stuck] = true
				root.Children[foreground] = true
				type executionResult struct {
					err     error
					elapsed time.Duration
				}
				done := make(chan executionResult, 1)
				start := time.Now()
				go func() {
					err := execute(nil, root, 2, rng)
					done <- executionResult{err: err, elapsed: time.Since(start)}
				}()
				// In-flight ops cannot observe cancellation. Shutdown must use
				// one 30s grace period even if an op never returns on its own.
				time.Sleep(31 * time.Second)
				var result executionResult
				returned := false
				select {
				case result = <-done:
					returned = true
				default:
					t.Error("scheduler still waiting beyond shutdown grace")
				}
				close(release)
				if !returned {
					result = <-done
				}
				synctest.Wait()
				if !errors.Is(result.err, want) {
					t.Fatalf("lost shutdown outcome: want %v, got %v", want, result.err)
				}
				if result.elapsed < 30*time.Second || result.elapsed >= 31*time.Second {
					t.Fatalf("expected one bounded grace period, took %s", result.elapsed)
				}
			})
		})
	}
}
