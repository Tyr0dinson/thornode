package endpoints_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type HealthSuite struct{}

var _ = Suite(&HealthSuite{})

// fakeProbe is a scriptable Probe + HashProbe.
type fakeProbe struct {
	mu       sync.Mutex
	name     string
	height   int64
	err      error
	identity string
	hashes   map[int64]string
}

func (f *fakeProbe) set(height int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.height, f.err = height, err
}

func (f *fakeProbe) Name() string { return f.name }

func (f *fakeProbe) Height(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.height, f.err
}

func (f *fakeProbe) Identity(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.identity, nil
}

func (f *fakeProbe) HashAt(_ context.Context, h int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hashes[h], nil
}

func healthCfg() config.ChainFailoverConfig {
	return config.ChainFailoverConfig{
		HealthCheckInterval:   time.Hour, // tests call Cycle() directly
		HealthCheckTimeout:    time.Second,
		MaxBlockLag:           3,
		ErrorThreshold:        2,
		ErrorWindow:           time.Minute,
		RecoveryCheckInterval: 0, // every cycle
		MinRecoveryChecks:     2,
		RateLimitBackoff:      30 * time.Second,
	}
}

func newHealthMgr(c *C, cfg config.ChainFailoverConfig, opts []endpoints.Option, probes ...*fakeProbe) (*endpoints.Manager, []endpoints.Probe) {
	var ns []string
	ps := make([]endpoints.Probe, len(probes))
	for i, p := range probes {
		ns = append(ns, p.name)
		ps[i] = p
	}
	m, err := endpoints.NewManager(common.BTCChain, eps(ns...), cfg, zerolog.Nop(), opts...)
	c.Assert(err, IsNil)
	return m, ps
}

var down = errors.New("down")

func (s *HealthSuite) TestBootstrapSelectsHighestPriorityReachable(c *C) {
	local := &fakeProbe{name: "local", err: down, identity: "g1"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "g1"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Active().Name, Equals, "backup")
	c.Assert(m.Entry("local").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("local").IdentityVerified(), Equals, false)
	// the backup's identity is recorded so it can serve, but one endpoint is not a
	// majority, so it does not become the reference the others are judged against
	c.Assert(m.Entry("backup").IdentityVerified(), Equals, true)
	c.Assert(m.ReferenceIdentity(), Equals, "")
	// once priority-1 answers it sets the reference, and the agreeing backup stays
	local.set(100, nil)
	m.Cycle(ctxBg())
	c.Assert(m.ReferenceIdentity(), Equals, "g1")
	c.Assert(m.Entry("backup").State(), Not(Equals), endpoints.StateDisabled)
}

// A lone reachable backup must never latch the reference identity: if it did, the
// operator's own priority-1 endpoint would be permanently disabled when it came
// back on the correct network.
func (s *HealthSuite) TestLoneVerifiedEndpointDoesNotLatchReference(c *C) {
	local := &fakeProbe{name: "local", err: down, identity: "mainnet"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.ReferenceIdentity(), Equals, "")
	c.Assert(m.Entry("backup").State(), Equals, endpoints.StateHealthy) // still serves
	c.Assert(m.Active().Name, Equals, "backup")

	// priority-1 recovers on the correct network: it sets the reference and the
	// wrong-network backup is the one disabled, not the other way round
	local.set(100, nil)
	m.Cycle(ctxBg())
	c.Assert(m.ReferenceIdentity(), Equals, "mainnet")
	c.Assert(m.Entry("backup").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Entry("local").State(), Not(Equals), endpoints.StateDisabled)
	c.Assert(m.Active().Name, Equals, "local")
}

// Two agreeing endpoints are a majority even without priority-1, and a third that
// disagrees is disabled.
func (s *HealthSuite) TestMajorityOfTwoEstablishesReference(c *C) {
	local := &fakeProbe{name: "local", err: down}
	b := &fakeProbe{name: "b", height: 100, identity: "mainnet"}
	d := &fakeProbe{name: "d", height: 100, identity: "mainnet"}
	x := &fakeProbe{name: "x", height: 100, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, b, d, x)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.ReferenceIdentity(), Equals, "mainnet")
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Active().Name, Equals, "b")
}

func (s *HealthSuite) TestBootstrapPriorityOneSetsReferenceAndDisablesOthers(c *C) {
	local := &fakeProbe{name: "local", height: 100, identity: "mainnet"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.ReferenceIdentity(), Equals, "mainnet")
	c.Assert(m.Entry("backup").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Active().Name, Equals, "local")
}

func (s *HealthSuite) TestBootstrapExpectedIdentityPinsPriorityOne(c *C) {
	local := &fakeProbe{name: "local", height: 100, identity: "testnet"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "mainnet"}
	m, ps := newHealthMgr(c, healthCfg(), []endpoints.Option{endpoints.WithExpectedIdentity("mainnet")}, local, backup)
	err := m.Bootstrap(ctxBg(), ps)
	c.Assert(err, ErrorMatches, `(?s).*priority-1 endpoint local reports identity "testnet" but expected_identity is "mainnet".*`)

	// without a pin, priority-1 sets the reference and the third party is disabled
	local2 := &fakeProbe{name: "local", height: 100, identity: "testnet"}
	backup2 := &fakeProbe{name: "backup", height: 100, identity: "mainnet"}
	m2, ps2 := newHealthMgr(c, healthCfg(), nil, local2, backup2)
	c.Assert(m2.Bootstrap(ctxBg(), ps2), IsNil)
	c.Assert(m2.Entry("backup").State(), Equals, endpoints.StateDisabled)
}

func (s *HealthSuite) TestBootstrapMajorityWhenPriorityOneDownAndMismatchDisabled(c *C) {
	local := &fakeProbe{name: "local", err: down, identity: "mainnet"}
	b := &fakeProbe{name: "b", height: 100, identity: "mainnet"}
	d := &fakeProbe{name: "d", height: 100, identity: "mainnet"}
	x := &fakeProbe{name: "x", height: 100, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, b, d, x)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.ReferenceIdentity(), Equals, "mainnet")
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Active().Name, Equals, "b")
	// local comes up on the wrong network later: disabled, never active
	local.set(100, nil)
	local.identity = "testnet"
	m.Cycle(ctxBg())
	c.Assert(m.Entry("local").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Active().Name, Equals, "b")
}

func (s *HealthSuite) TestBootstrapNothingReachableIsNotFatal(c *C) {
	local := &fakeProbe{name: "local", err: down}
	m, ps := newHealthMgr(c, healthCfg(), nil, local)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.AllDown(), Equals, true)
	c.Assert(m.Active().Name, Equals, "local")
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"local"})
}

func (s *HealthSuite) TestBootstrapUnverifiedIsUnhealthyOnlyWithSeveralEndpoints(c *C) {
	only := &fakeProbe{name: "only", height: 1} // no identity available
	m, ps := newHealthMgr(c, healthCfg(), nil, only)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateHealthy)

	a := &fakeProbe{name: "a", height: 1, identity: "g"}
	b := &fakeProbe{name: "b", height: 1} // no identity
	m2, ps2 := newHealthMgr(c, healthCfg(), nil, a, b)
	c.Assert(m2.Bootstrap(ctxBg(), ps2), IsNil)
	c.Assert(m2.Entry("b").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(names(m2.ReadCandidates()), DeepEquals, []string{"a"})
	// b starts reporting an identity: verified, then recovers
	b.identity = "g"
	m2.Cycle(ctxBg())
	m2.Cycle(ctxBg())
	c.Assert(m2.Entry("b").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestBootstrapRejectsProbeNameMismatch(c *C) {
	m, _ := newHealthMgr(c, healthCfg(), nil, &fakeProbe{name: "local"})
	err := m.Bootstrap(ctxBg(), []endpoints.Probe{&fakeProbe{name: "other"}})
	c.Assert(err, ErrorMatches, ".*probe.*other.*does not match.*local.*")
}

// One health cycle per step: a lagging backup without quorum is left alone, the
// primary fails over on probe strikes, recovers, and is failed back to.
func (s *HealthSuite) TestHealthLoopUnrolledFailoverAndFailback(c *C) {
	cfg := healthCfg()
	cfg.PreferHigherPriority = true
	local := &fakeProbe{name: "local", height: 100, identity: "g"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "g"}
	m, ps := newHealthMgr(c, cfg, nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.SetScannerHeight(func() int64 { return 0 })

	step := func(n int, lh int64, lerr error, bh int64, wantLocal, wantBackup endpoints.State, wantActive string) {
		local.set(lh, lerr)
		backup.set(bh, nil)
		m.Cycle(ctxBg())
		c.Check(m.Entry("local").State(), Equals, wantLocal, Commentf("cycle %d local", n))
		c.Check(m.Entry("backup").State(), Equals, wantBackup, Commentf("cycle %d backup", n))
		c.Check(m.Active().Name, Equals, wantActive, Commentf("cycle %d active", n))
	}
	step(1, 100, nil, 100, endpoints.StateHealthy, endpoints.StateHealthy, "local")
	step(2, 100, nil, 110, endpoints.StateHealthy, endpoints.StateHealthy, "local") // no quorum, no eviction
	step(3, 100, nil, 111, endpoints.StateHealthy, endpoints.StateHealthy, "local")
	step(4, 0, down, 112, endpoints.StateHealthy, endpoints.StateHealthy, "local") // probe strike 1/2
	step(5, 0, down, 113, endpoints.StateUnhealthy, endpoints.StateHealthy, "backup")
	step(6, 114, nil, 114, endpoints.StateRecovering, endpoints.StateHealthy, "backup")
	step(7, 115, nil, 115, endpoints.StateHealthy, endpoints.StateHealthy, "local") // failback
	step(8, 116, nil, 116, endpoints.StateHealthy, endpoints.StateHealthy, "local")
}

func (s *HealthSuite) TestNoFailbackWithoutPreferHigherPriority(c *C) {
	local := &fakeProbe{name: "local", height: 100, identity: "g"}
	backup := &fakeProbe{name: "backup", height: 100, identity: "g"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	local.set(0, down)
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.Active().Name, Equals, "backup")
	local.set(100, nil)
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.Entry("local").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "backup")
}

func (s *HealthSuite) TestLagEvictionNeedsQuorumAndConsecutiveStrikes(c *C) {
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 100, identity: "g"}
	d := &fakeProbe{name: "d", height: 100, identity: "g"}
	m, ps := newHealthMgr(c, healthCfg(), nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	a.set(100, nil)
	b.set(110, nil)
	d.set(110, nil)
	m.Cycle(ctxBg()) // strike 1
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	m.RecordSuccess("a") // inline traffic must not erase the strike
	m.Cycle(ctxBg())     // strike 2 -> unhealthy
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Active().Name, Equals, "b")
	a.set(111, nil)
	b.set(111, nil)
	d.set(111, nil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateRecovering)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestScannerBaselineOnlyForSingleEndpoint(c *C) {
	only := &fakeProbe{name: "only", height: 100, identity: "g"}
	m, ps := newHealthMgr(c, healthCfg(), nil, only)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	scanner := int64(0)
	m.SetScannerHeight(func() int64 { return scanner })
	m.Cycle(ctxBg())
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateHealthy)
	scanner = 120
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.Entry("only").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"only"})

	// with two endpoints the scanner height (read from the active one) is ignored
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 600, identity: "g"}
	m2, ps2 := newHealthMgr(c, healthCfg(), nil, a, b)
	c.Assert(m2.Bootstrap(ctxBg(), ps2), IsNil)
	m2.SetScannerHeight(func() int64 { return 600 })
	for i := 0; i < 5; i++ {
		m2.Cycle(ctxBg())
	}
	c.Assert(m2.Entry("a").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestLateIdentityMismatchDisables(c *C) {
	local := &fakeProbe{name: "local", height: 100, identity: "mainnet"}
	backup := &fakeProbe{name: "backup", err: down, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, backup)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	backup.set(100, nil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("backup").State(), Equals, endpoints.StateDisabled)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"local"})
}

func (s *HealthSuite) TestIntegrityMinorityHashIsAStrike(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestIntegrityTieIsSkipped(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
}

type slowProbe struct {
	fakeProbe
	delay time.Duration
}

func (p *slowProbe) Height(ctx context.Context) (int64, error) {
	select {
	case <-time.After(p.delay):
		return p.fakeProbe.Height(ctx)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *HealthSuite) TestProbeTimeoutCountsAsFailure(c *C) {
	cfg := healthCfg()
	cfg.HealthCheckTimeout = 20 * time.Millisecond
	cfg.ErrorThreshold = 1
	slow := &slowProbe{fakeProbe: fakeProbe{name: "slow", height: 1, identity: "g"}, delay: 200 * time.Millisecond}
	m, err := endpoints.NewManager(common.BTCChain, eps("slow"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{slow}), IsNil)
	c.Assert(m.Entry("slow").State(), Equals, endpoints.StateUnhealthy)
}

func (s *HealthSuite) TestStartStopRunsCyclesAndExits(c *C) {
	cfg := healthCfg()
	cfg.HealthCheckInterval = 10 * time.Millisecond
	cfg.HealthCheckTimeout = 5 * time.Millisecond
	p := &fakeProbe{name: "a", height: 1, identity: "g"}
	m, ps := newHealthMgr(c, cfg, nil, p)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Start()
	m.Start() // idempotent
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		c.Fatal("Stop did not return")
	}
	c.Assert(m.Entry("a").LastHeight(), Equals, int64(1))
}

func (s *HealthSuite) TestStopCancelsInFlightCycle(c *C) {
	cfg := healthCfg()
	cfg.HealthCheckInterval = 10 * time.Millisecond
	cfg.HealthCheckTimeout = 5 * time.Second
	slow := &slowProbe{fakeProbe: fakeProbe{name: "slow", height: 1, identity: "g"}}
	m, err := endpoints.NewManager(common.BTCChain, eps("slow"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	// bootstrap answers at once; only the cycles the loop runs are slow, so the test
	// measures Stop and not the probe
	c.Assert(m.Bootstrap(context.Background(), []endpoints.Probe{slow}), IsNil)
	slow.delay = 3 * time.Second
	m.Start()
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	m.Stop()
	c.Assert(time.Since(start) < time.Second, Equals, true)
}

func (s *HealthSuite) TestBootstrapWithoutReferenceStillServes(c *C) {
	local := &fakeProbe{name: "local", err: down}
	b := &fakeProbe{name: "b", height: 100, identity: "mainnet"}
	x := &fakeProbe{name: "x", height: 100, identity: "testnet"}
	m, ps := newHealthMgr(c, healthCfg(), nil, local, b, x)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	// two endpoints disagree and priority-1 is silent: no reference, nobody disabled,
	// and both keep serving rather than taking the chain dark
	c.Assert(m.ReferenceIdentity(), Equals, "")
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.AllDown(), Equals, false)
	m.Cycle(ctxBg())
	c.Assert(m.ReferenceIdentity(), Equals, "")
	// priority-1 comes up: it sets the reference and x is disabled
	local.set(100, nil)
	local.identity = "mainnet"
	m.Cycle(ctxBg())
	c.Assert(m.ReferenceIdentity(), Equals, "mainnet")
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("local").State(), Equals, endpoints.StateHealthy)
}

// An endpoint that never reports an identity is out of rotation when verification
// is required, even when nothing else is healthy: observing an unknown network is
// worse than going dark.
func (s *HealthSuite) TestUnverifiedEndpointIsNotUsedEvenWhenAllDown(c *C) {
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 100} // never reports an identity
	m, ps := newHealthMgr(c, healthCfg(), nil, a, b)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a"})
	c.Assert(m.WriteTarget().Name, Equals, "a")
	// a goes down too: the all-down fallback keeps retrying a, which is verified,
	// but never reaches for b, whose network is unknown
	a.set(0, down)
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.AllDown(), Equals, true)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a"})
	verify, unverified := m.VerificationCandidates(0)
	c.Assert(names(verify), DeepEquals, []string{"a"})
	c.Assert(unverified, Equals, 1) // b cannot be asked, so absence is not established
	c.Assert(m.WriteTarget().Name, Equals, "a")
	// and if a is disabled outright, nothing is usable at all
	m.Disable("a", "identity")
	c.Assert(m.ReadCandidates(), HasLen, 0)
	c.Assert(m.WriteTarget(), IsNil)
	verify, _ = m.VerificationCandidates(0)
	c.Assert(verify, HasLen, 0)
}

func (s *HealthSuite) TestIntegrityEvictedEndpointMustMatchMajorityToRecover(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	cfg.MinRecoveryChecks = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	// still disagreeing: liveness and height are fine but the hash is not
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	// now agreeing: recovers
	d.mu.Lock()
	d.hashes[95] = "H"
	d.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestIntegrityRecheckAgainstSingleHealthyReference(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	cfg.MinRecoveryChecks = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindIntegrity) // evicted earlier
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy) // X != H
	d.mu.Lock()
	d.hashes[95] = "H"
	d.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
}

func (s *HealthSuite) TestRateLimitedProbeIsCooldownNotStrike(c *C) {
	cfg := healthCfg()
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 100, identity: "g"}
	m, ps := newHealthMgr(c, cfg, nil, a, b)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	a.mu.Lock()
	a.err = fmt.Errorf("http 429: %w", endpoints.ErrRateLimited)
	a.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("a").InCooldown(), Equals, true)
	c.Assert(m.Active().Name, Equals, "b") // handed off while cooling down
	// while cooling down the endpoint is not probed at all
	a.set(0, errors.New("down"))
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("a").Snapshot().ProbeStrikes, Equals, 0)
}

func (s *HealthSuite) TestBootstrapRateLimitedEndpointStaysHealthyInCooldown(c *C) {
	a := &fakeProbe{name: "a", err: fmt.Errorf("429: %w", endpoints.ErrRateLimited), identity: "g"}
	m, ps := newHealthMgr(c, healthCfg(), nil, a)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("a").InCooldown(), Equals, true)
}

// errHashProbe is a fakeProbe whose HashAt fails.
type errHashProbe struct{ fakeProbe }

func (p *errHashProbe) HashAt(context.Context, int64) (string, error) {
	return "", errors.New("hash unavailable")
}

// An evicted endpoint recovers only on a hash that positively matched the healthy
// reference. A failed hash fetch, an empty hash, or a tie among the healthy endpoints
// is not a match, however many height probes pass.
func (s *HealthSuite) TestIntegrityEvictedEndpointNeedsAnExplicitHashMatchToRecover(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	cfg.MinRecoveryChecks = 1

	// 1. hash fetch fails on the evicted endpoint: stays evicted
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &errHashProbe{fakeProbe{name: "d", height: 100, identity: "g"}}
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "d"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, d}), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)

	// 2. empty hash (endpoint answers but has no hash): stays evicted
	d2 := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{}}
	var ps []endpoints.Probe
	m, ps = newHealthMgr(c, cfg, nil, a, b, d2)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)

	// 3. the healthy endpoints tie: nothing is cleared, even though d agrees with one
	bx := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	d3 := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	m, ps = newHealthMgr(c, cfg, nil, a, bx, d3)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)

	// 4. a matching hash clears the quarantine in one cycle
	d4 := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	m, ps = newHealthMgr(c, cfg, nil, a, b, d4)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, false)
}

// A minority-hash strike that does not evict leaves the endpoint healthy and NOT
// quarantined: only an eviction for a minority hash sets the quarantine flag.
func (s *HealthSuite) TestIntegrityStrikeBelowThresholdDoesNotQuarantine(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 3
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, false)
	c.Assert(m.Entry("d").Snapshot().IntegrityEvicted, Equals, false)
	// a transient disagreement that heals never quarantines
	d.mu.Lock()
	d.hashes[95] = "H"
	d.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, false)
	// three disagreements evict and quarantine, visible in the status snapshot
	d.mu.Lock()
	d.hashes[95] = "X"
	d.mu.Unlock()
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("d").Snapshot().IntegrityEvicted, Equals, true)
}

// An endpoint that is both behind and on another fork is evicted for the fork, not
// merely for lag, so it stays quarantined until it serves the majority hash.
func (s *HealthSuite) TestLaggingEndpointWithMinorityHashIsQuarantined(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	cfg.MaxBlockLag = 3
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 96, identity: "g", hashes: map[int64]string{95: "X"}} // behind by 4 and on a fork
	m, ps := newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("d").LastErrorKind(), Equals, endpoints.ErrorKindIntegrity)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a", "b"})
}

// Without a strict majority among the healthy endpoints (H, H, X, Y) nothing is
// evicted and nothing is cleared: an inconclusive disagreement must not pick a fork.
func (s *HealthSuite) TestIntegrityPluralityWithoutMajorityIsSkipped(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	x := &fakeProbe{name: "x", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	y := &fakeProbe{name: "y", height: 100, identity: "g", hashes: map[int64]string{95: "Y"}}
	z := &fakeProbe{name: "z", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b, x, y, z)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("z").MarkUnhealthy(endpoints.ErrorKindIntegrity) // quarantined earlier, now agrees with the plurality
	m.Cycle(ctxBg())
	for _, n := range []string{"a", "b", "x", "y"} {
		c.Assert(m.Entry(n).State(), Equals, endpoints.StateHealthy, Commentf(n))
	}
	// H holds 2 of the 4 healthy answers: a plurality, not a majority, so z is not cleared
	c.Assert(m.Entry("z").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("z").IntegrityEvicted(), Equals, true)
	// once H is a strict majority (3 of the 4 healthy answers) z recovers
	y.mu.Lock()
	y.hashes[95] = "H"
	y.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("z").State(), Not(Equals), endpoints.StateUnhealthy)
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateUnhealthy)
}

// A probe that proves the endpoint serves another network (ErrIdentityMismatch) gets
// the endpoint disabled, at bootstrap or later, instead of a liveness strike; with
// expected_identity set it is fatal for the priority-1 endpoint.
func (s *HealthSuite) TestIdentityMismatchDisablesTheEndpoint(c *C) {
	cfg := healthCfg()
	mismatch := fmt.Errorf("%w: grpc host is on another network", endpoints.ErrIdentityMismatch)

	// at bootstrap, a backup on another network is disabled
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 100, identity: "g", err: mismatch}
	m, ps := newHealthMgr(c, cfg, nil, a, b)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)

	// later: an endpoint verified from one host alone proves the other host wrong
	a2 := &fakeProbe{name: "a", height: 100, identity: "g"}
	b2 := &fakeProbe{name: "b", height: 100, identity: "g"}
	m, ps = newHealthMgr(c, cfg, nil, a2, b2)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	b2.set(100, mismatch)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.Entry("b").Usable(), Equals, false)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a"})

	// priority 1 with a pinned identity: fatal
	a3 := &fakeProbe{name: "a", height: 100, identity: "g", err: mismatch}
	b3 := &fakeProbe{name: "b", height: 100, identity: "g"}
	m, ps = newHealthMgr(c, cfg, []endpoints.Option{endpoints.WithExpectedIdentity("g")}, a3, b3)
	c.Assert(m.Bootstrap(ctxBg(), ps), ErrorMatches, ".*priority-1 endpoint a: .*another network.*")

	// a single legacy endpoint is disabled rather than left serving an unknown network
	a4 := &fakeProbe{name: "a", height: 100, identity: "g", err: mismatch}
	m, ps = newHealthMgr(c, cfg, nil, a4)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateDisabled)
	c.Assert(m.ReadCandidates(), HasLen, 0)
}

// A forked node that is evicted by a lag or probe strike after minority-hash strikes
// is still quarantined: the integrity strike anywhere in the run counts.
func (s *HealthSuite) TestIntegrityStrikeEarlierInTheRunStillQuarantines(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 2
	cfg.MaxBlockLag = 3
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg()) // strike 1: minority hash, still healthy
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
	d.set(0, down) // strike 2: probe failure completes the eviction
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("d").Snapshot().IntegrityEvicted, Equals, true)
	// a passing probe between the two strikes resets the run: no quarantine
	d2 := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, ps = newHealthMgr(c, cfg, nil, a, b, d2)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	d2.mu.Lock()
	d2.hashes[95] = "H"
	d2.mu.Unlock()
	m.Cycle(ctxBg()) // pass
	d2.set(0, down)
	m.Cycle(ctxBg())
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, false)
}

type rateLimitedHashProbe struct{ fakeProbe }

func (p *rateLimitedHashProbe) HashAt(context.Context, int64) (string, error) {
	return "", fmt.Errorf("http 429: %w", endpoints.ErrRateLimited)
}

// A hash probe that is rate limited puts the endpoint in cooldown, as a rate-limited
// height probe does: it is not a strike, and the endpoint is left alone until the
// cooldown ends instead of being asked again every cycle.
func (s *HealthSuite) TestRateLimitedHashProbeIsACooldown(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &rateLimitedHashProbe{fakeProbe{name: "d", height: 100, identity: "g"}}
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "d"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, d}), IsNil)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").InCooldown(), Equals, true)
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
}

// Only an endpoint that may serve votes on the consensus tip: two endpoints whose
// network was never verified, close to each other and far ahead, must not make the
// verified endpoint look behind and evict it.
func (s *HealthSuite) TestUnverifiedEndpointsDoNotVoteOnTheTip(c *C) {
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	x := &fakeProbe{name: "x", height: 200}
	y := &fakeProbe{name: "y", height: 201}
	m, ps := newHealthMgr(c, healthCfg(), nil, a, x, y)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	c.Assert(m.Entry("x").Usable(), Equals, false)
	c.Assert(m.Entry("y").Usable(), Equals, false)
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "a")
}
