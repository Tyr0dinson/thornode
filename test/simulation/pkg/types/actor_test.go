package types

import (
	"errors"
	rand "math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	. "gopkg.in/check.v1"
)

func Test(t *testing.T) { TestingT(t) }

type ActorSuite struct{}

var _ = Suite(&ActorSuite{})

func testRNG() *rand.Rand {
	return rand.New(rand.NewChaCha8([32]byte{}))
}

func (s *ActorSuite) TestInit(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true

	root.InitRoot()

	// init the atomics
	c.Assert(child1.state, NotNil)
	c.Assert(grandchild2.state, NotNil)

	// ensure parents are set
	c.Assert(child1.parents, DeepEquals, map[*Actor]bool{root: true})
	c.Assert(grandchild2.parents, DeepEquals, map[*Actor]bool{child2: true})
}

func (s *ActorSuite) TestWalkDepthFirst(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true

	root.InitRoot()

	visited := map[string]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		visited[a.Name] = true
		return a.Execute(nil) == nil
	})

	expected := map[string]bool{
		"root":        true,
		"child1":      true,
		"grandchild1": true,
		"child2":      true,
		"grandchild2": true,
	}
	c.Assert(visited, DeepEquals, expected)
}

func (s *ActorSuite) TestWalkDepthFirstFail(c *C) {
	rng := testRNG()
	opFail := func(config *OpConfig) OpResult {
		return OpResult{Continue: false, Finish: true, Error: errors.New("foo")}
	}

	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	child2.Ops = []Op{opFail}
	child3 := NewActor("child3", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)
	grandchild3 := NewActor("grandchild3", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	root.Children[child3] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true
	child3.Children[grandchild3] = true

	root.InitRoot()

	visited := map[string]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		visited[a.Name] = true
		return a.Execute(nil) == nil
	})

	expected := map[string]bool{
		"root":        true,
		"child1":      true,
		"child2":      true,
		"child3":      true,
		"grandchild1": true,
		"grandchild3": true,
	}
	c.Assert(visited, DeepEquals, expected)
}

func (s *ActorSuite) TestWalkDepthFirstVisitsDAGNodeOnce(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	parent1 := NewActor("parent1", rng)
	parent2 := NewActor("parent2", rng)
	shared := NewActor("shared", rng)
	child := NewActor("child", rng)

	root.Children[parent1] = true
	root.Children[parent2] = true
	parent1.Children[shared] = true
	parent2.Children[shared] = true
	shared.Children[child] = true

	root.InitRoot()

	visits := map[string]int{}
	cont := root.WalkDepthFirst(func(a *Actor) bool {
		visits[a.Name]++
		return true
	})

	expected := map[string]int{
		"root":    1,
		"parent1": 1,
		"parent2": 1,
		"shared":  1,
		"child":   1,
	}
	c.Assert(cont, Equals, true)
	c.Assert(visits, DeepEquals, expected)
	c.Assert(shared.parents, DeepEquals, map[*Actor]bool{parent1: true, parent2: true})
	c.Assert(child.parents, DeepEquals, map[*Actor]bool{shared: true})
}

// TestExecuteBlockingOpRespectsTimeout reproduces the deadlock pattern observed
// in production: an Op that blocks past Actor.Timeout (e.g. a chain-state poll
// that wedges on a slow mocknet RPC) must not be able to bypass the actor's
// timeout. Before this fix, Execute called op(c) inline and only checked
// time.Since(start) after the call returned — a stuck Op would spin until the
// outer job-level timeout (1h10m) killed the run, hiding 38% of test-simulation
// failures over a recent 7-day window.
func (s *ActorSuite) TestExecuteBlockingOpRespectsTimeout(c *C) {
	rng := testRNG()
	a := NewActor("blocker", rng)
	a.InitRoot()
	a.Timeout = 100 * time.Millisecond
	a.Interval = 10 * time.Millisecond

	opStarted := make(chan struct{})
	blockingOp := func(_ *OpConfig) OpResult {
		close(opStarted)
		// Simulate a wedged HTTP call: block for much longer than Actor.Timeout.
		time.Sleep(5 * time.Second)
		return OpResult{Continue: true}
	}
	a.Ops = append(a.Ops, blockingOp)

	start := time.Now()
	err := a.Execute(&OpConfig{})
	elapsed := time.Since(start)

	// Op must have started (proving we got past the goroutine launch)
	select {
	case <-opStarted:
	default:
		c.Fatalf("op never started")
	}

	c.Assert(err, Equals, ErrTimeout)
	// Should return shortly after Timeout, well before the Op's 5s sleep.
	c.Assert(elapsed < time.Second, Equals, true,
		Commentf("Execute took %v, expected < 1s (Actor.Timeout=100ms)", elapsed))
}

// TestExecuteFastOpCompletes verifies the happy path is unchanged: an Op that
// returns Continue immediately completes Execute without timing out.
func (s *ActorSuite) TestExecuteFastOpCompletes(c *C) {
	rng := testRNG()
	a := NewActor("fast", rng)
	a.InitRoot()
	a.Timeout = time.Second
	a.Interval = 10 * time.Millisecond
	a.Ops = append(a.Ops, func(_ *OpConfig) OpResult {
		return OpResult{Continue: true}
	})

	c.Assert(a.Execute(&OpConfig{}), IsNil)
}

// TestExecuteRetriesUntilContinue verifies the retry loop still works: an Op
// that returns OpResult{} a few times before signalling Continue should
// complete normally within the actor's timeout.
func (s *ActorSuite) TestExecuteRetriesUntilContinue(c *C) {
	rng := testRNG()
	a := NewActor("retrier", rng)
	a.InitRoot()
	a.Timeout = time.Second
	a.Interval = 10 * time.Millisecond

	var calls int32
	a.Ops = append(a.Ops, func(_ *OpConfig) OpResult {
		if atomic.AddInt32(&calls, 1) < 3 {
			return OpResult{}
		}
		return OpResult{Continue: true}
	})

	c.Assert(a.Execute(&OpConfig{}), IsNil)
	c.Assert(atomic.LoadInt32(&calls), Equals, int32(3))
}

// TestExecuteOpReturnedErrorPropagates ensures op errors with Finish=true still
// surface (the goroutine wrapper must not swallow the result).
func (s *ActorSuite) TestExecuteOpReturnedErrorPropagates(c *C) {
	rng := testRNG()
	a := NewActor("err", rng)
	a.InitRoot()
	a.Timeout = time.Second
	a.Interval = 10 * time.Millisecond
	wantErr := errors.New("boom")
	a.Ops = append(a.Ops, func(_ *OpConfig) OpResult {
		return OpResult{Finish: true, Error: wantErr}
	})

	c.Assert(a.Execute(&OpConfig{}), Equals, wantErr)
}
