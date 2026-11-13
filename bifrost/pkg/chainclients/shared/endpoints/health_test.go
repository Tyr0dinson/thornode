package endpoints_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

// With exactly two endpoints no quorum tip exists when they disagree, so THORChain's
// last observed height (agreed by 2/3 of nodes, never from one of our endpoints)
// breaks the tie: an endpoint more than max_block_lag below it is lagging. It can
// only strike an endpoint behind what the network has already observed, never one
// at the tip, and a missing, failing, slow or lower reference strikes nothing. A
// single endpoint keeps the scanner baseline only.
func (s *HealthSuite) TestThorchainHeightBreaksTheTwoEndpointTie(c *C) {
	run := func(ref func() (int64, error), heights ...int64) *endpoints.Manager {
		probes := []*fakeProbe{{name: "a", height: heights[0], identity: "g"}}
		if len(heights) > 1 {
			probes = append(probes, &fakeProbe{name: "b", height: heights[1], identity: "g"})
		}
		m, ps := newHealthMgr(c, healthCfg(), nil, probes...)
		c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
		m.SetReferenceHeight(ref)
		for i := 0; i < 3; i++ {
			m.Cycle(ctxBg())
		}
		return m
	}
	at := func(h int64) func() (int64, error) { return func() (int64, error) { return h, nil } }

	// stalled primary at 100, backup at 200, the network has observed 150
	m := run(at(150), 100, 200)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "b")

	// no reference, a failing one, or one below both endpoints: nothing struck
	for _, ref := range []func() (int64, error){nil, func() (int64, error) { return 0, errors.New("thornode down") }, at(50)} {
		m = run(ref, 100, 200)
		c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
		c.Assert(m.Active().Name, Equals, "a")
	}

	// a reference no usable endpoint has reached (forced on THORChain, or every
	// endpoint behind) is not trusted: nothing struck, the chain does not go all-down
	m = run(at(500), 100, 200)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)

	// a reference that does not answer within the probe timeout is skipped
	release := make(chan struct{})
	defer close(release)
	m = run(func() (int64, error) { <-release; return 150, nil }, 100, 200)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)

	// a single endpoint is not judged against it
	m = run(at(150), 100)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
}

// A THORChain height read that hangs is not started again while it is in flight: a
// stalled thornode costs one waiting goroutine, not one per health cycle.
func (s *HealthSuite) TestHangingReferenceReadIsNotRestartedEachCycle(c *C) {
	a := &fakeProbe{name: "a", height: 100, identity: "g"}
	b := &fakeProbe{name: "b", height: 100, identity: "g"}
	cfg := healthCfg()
	cfg.HealthCheckTimeout = 20 * time.Millisecond
	m, ps := newHealthMgr(c, cfg, nil, a, b)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	var calls atomic.Int32
	release := make(chan struct{})
	m.SetReferenceHeight(func() (int64, error) {
		calls.Add(1)
		<-release
		return 100, nil
	})
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(calls.Load(), Equals, int32(1))
	close(release)
}

// slowHashProbe is a fakeProbe whose height and hash reads both take a while, as a
// remote endpoint's do.
type slowHashProbe struct {
	*fakeProbe
	heightDelay, hashDelay time.Duration
}

func (p slowHashProbe) Height(ctx context.Context) (int64, error) {
	select {
	case <-time.After(p.heightDelay):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return p.fakeProbe.Height(ctx)
}

func (p slowHashProbe) HashAt(ctx context.Context, h int64) (string, error) {
	select {
	case <-time.After(p.hashDelay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return p.fakeProbe.HashAt(ctx, h)
}

// A cycle runs under one health_check_interval deadline. THORChain's height is read
// while the endpoints are probed, so a slow thornode and slow probes together still
// leave the integrity check its time, and a minority hash is still struck.
func (s *HealthSuite) TestSlowReferenceAndProbesLeaveTheIntegrityCheckItsTime(c *C) {
	cfg := healthCfg()
	cfg.HealthCheckInterval = 700 * time.Millisecond
	cfg.HealthCheckTimeout = 400 * time.Millisecond
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	m, _ := newHealthMgr(c, cfg, nil, a, b, d)
	var slow []endpoints.Probe
	for _, p := range []*fakeProbe{a, b, d} {
		slow = append(slow, slowHashProbe{fakeProbe: p, heightDelay: 300 * time.Millisecond, hashDelay: 50 * time.Millisecond})
	}
	c.Assert(m.Bootstrap(ctxBg(), slow), IsNil)
	release := make(chan struct{})
	defer close(release)
	m.SetReferenceHeight(func() (int64, error) { // a thornode that answers only after the timeout
		<-release
		return 0, nil
	})
	ctx, cancel := context.WithTimeout(ctxBg(), cfg.HealthCheckInterval)
	defer cancel()
	m.Cycle(ctx)
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
}

// An endpoint quarantined for a minority hash is on another fork. Its height must not
// set the tip the others are judged against: two quarantined endpoints that agree
// with each other ahead of the healthy ones strike nobody. Nor does it count as having
// reached THORChain's height, which would strike every healthy endpoint below it.
func (s *HealthSuite) TestQuarantinedEndpointsDoNotSetTheLagTip(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	hashes := func(h string) map[int64]string { return map[int64]string{95: h, 195: h} }
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: hashes("H")}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: hashes("H")}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: hashes("H")}
	x := &fakeProbe{name: "x", height: 200, identity: "g", hashes: hashes("X")}
	y := &fakeProbe{name: "y", height: 200, identity: "g", hashes: hashes("X")}
	m, ps := newHealthMgr(c, cfg, nil, a, b, d, x, y)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	m.Entry("y").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "d"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf("%s", name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf("%s", name))
	}
	// the integrity check ran at the healthy endpoints' tip: x and y stay quarantined
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("y").IntegrityEvicted(), Equals, true)

	// THORChain's height 150 is reached only by a quarantined endpoint: not trusted
	two, ps2 := newHealthMgr(c, cfg, nil, &fakeProbe{name: "a", height: 100, identity: "g"}, &fakeProbe{name: "b", height: 100, identity: "g"}, &fakeProbe{name: "x", height: 200, identity: "g"})
	c.Assert(two.Bootstrap(ctxBg(), ps2), IsNil)
	two.Entry("x").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	two.SetReferenceHeight(func() (int64, error) { return 150, nil })
	two.Cycle(ctxBg())
	c.Assert(two.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(two.Entry("b").State(), Equals, endpoints.StateHealthy)
}

// THORChain's height is read while the endpoints are probed. One that answered in time
// is used even when a probe ran to its own timeout and the reference's timer fired
// meanwhile: here it is the only lag signal (the two answering endpoints form no tip),
// and it strikes the endpoint below it every time.
func (s *HealthSuite) TestReferenceAnsweredDuringASlowProbeIsUsed(c *C) {
	cfg := healthCfg()
	cfg.HealthCheckTimeout = 50 * time.Millisecond
	cfg.ErrorThreshold = 1
	for i := 0; i < 12; i++ {
		a := &fakeProbe{name: "a", height: 200, identity: "g"}
		b := &fakeProbe{name: "b", height: 100, identity: "g"}
		hung := &slowProbe{fakeProbe: fakeProbe{name: "c", height: 200, identity: "g"}, delay: time.Hour}
		m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "c"), cfg, zerolog.Nop())
		c.Assert(err, IsNil)
		c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, hung}), IsNil)
		m.SetReferenceHeight(func() (int64, error) { return 150, nil })
		m.Cycle(ctxBg())
		c.Assert(m.Entry("b").State(), Equals, endpoints.StateUnhealthy, Commentf("run %d", i))
		c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy, Commentf("run %d", i))
	}
}

// Two endpoints on another fork that report heights ahead of the healthy majority
// must not pick the height the hashes are compared at (which would leave the
// majority out of the vote) nor set the lag tip the majority is judged against: the
// hashes are compared at the majority's level, the pair is quarantined, and nobody in
// the majority is struck. This holds whether the pair is far ahead or within the
// integrity depth.
func (s *HealthSuite) TestMinorityPairAheadIsComparedAtTheMajorityHeight(c *C) {
	for _, ahead := range []int64{200, 104} {
		cfg := healthCfg()
		cfg.IntegrityCheckDepth = 20
		cfg.ErrorThreshold = 1
		honest := func(name string) *fakeProbe {
			return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H", ahead - 20: "H"}}
		}
		forked := func(name string) *fakeProbe {
			return &fakeProbe{name: name, height: ahead, identity: "g", hashes: map[int64]string{80: "X", ahead - 20: "X"}}
		}
		m, ps := newHealthMgr(c, cfg, nil, honest("a"), honest("b"), honest("d"), forked("x"), forked("y"))
		c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
		m.Cycle(ctxBg())
		for _, name := range []string{"a", "b", "d"} {
			c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf("ahead %d: %s", ahead, name))
			c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf("ahead %d: %s", ahead, name))
		}
		for _, name := range []string{"x", "y"} {
			c.Assert(m.Entry(name).IntegrityEvicted(), Equals, true, Commentf("ahead %d: %s", ahead, name))
		}
	}
}

// voteEvictedProbe is a fakeProbe that runs onHash while its hash is read, as
// concurrent request failures can while the integrity vote is under way.
type voteEvictedProbe struct {
	*fakeProbe
	onHash func()
}

func (p voteEvictedProbe) HashAt(ctx context.Context, h int64) (string, error) {
	if p.onHash != nil {
		p.onHash()
	}
	return p.fakeProbe.HashAt(ctx, h)
}

// An endpoint found with a minority hash is quarantined even when inline request
// errors evicted it while the vote ran: recovering on probe passes alone would
// discard the hash disagreement and let it serve traffic again.
func (s *HealthSuite) TestMinorityHashFoundDuringAnInlineEvictionStillQuarantines(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &fakeProbe{name: "d", height: 100, identity: "g", hashes: map[int64]string{95: "X"}}
	var m *endpoints.Manager
	evicting := voteEvictedProbe{fakeProbe: d, onHash: func() { m.RecordError("d", endpoints.ErrorKindHTTP) }}
	m, _ = newHealthMgr(c, cfg, nil, a, b, d)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, evicting}), IsNil)
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateHealthy)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Not(Equals), endpoints.StateHealthy)
	c.Assert(m.Entry("d").IntegrityEvicted(), Equals, true)
}

// Endpoints far behind that are probed while they recover must not pull the height
// the hashes are compared at down to theirs: a recent fork on a healthy endpoint is
// then still found, also when they outnumber the healthy ones.
func (s *HealthSuite) TestRecoveringEndpointsDoNotLowerTheComparedHeight(c *C) {
	for _, behindN := range []int{3, 4} {
		cfg := healthCfg()
		cfg.IntegrityCheckDepth = 20
		cfg.ErrorThreshold = 1
		a := &fakeProbe{name: "a", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}
		b := &fakeProbe{name: "b", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}
		x := &fakeProbe{name: "x", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "X"}} // forked after 480
		probes := []*fakeProbe{a, b, x}
		var behind []string
		for i := 1; i <= behindN; i++ {
			name := fmt.Sprintf("r%d", i)
			behind = append(behind, name)
			probes = append(probes, &fakeProbe{name: name, height: 500, identity: "g", hashes: map[int64]string{480: "H"}})
		}
		m, ps := newHealthMgr(c, cfg, nil, probes...)
		c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
		for _, name := range behind {
			m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
		}
		m.Cycle(ctxBg())
		c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true, Commentf("%d behind", behindN))
		c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
		c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	}
}

// A split vote that no healthy endpoint contradicted (their hashes could not be
// read), or in which an endpoint's hash is shared by no tip, does not let endpoints
// far behind recover against their own tip.
func (s *HealthSuite) TestUncontradictedSplitStillHoldsBackRecovery(c *C) {
	for _, alone := range []bool{false, true} {
		cfg := healthCfg()
		cfg.IntegrityCheckDepth = 20
		cfg.ErrorThreshold = 1
		// not alone: the healthy pair cannot read 80 and the returning ones agree;
		// alone: the pair answers X and each returning endpoint has a hash of its own
		pairAt80, returningAt80 := "", []string{"H", "H", "H"}
		if alone {
			pairAt80, returningAt80 = "X", []string{"Z", "Y", "H"}
		}
		probes := []*fakeProbe{
			{name: "a", height: 1000, identity: "g", hashes: map[int64]string{80: pairAt80, 980: "H"}},
			{name: "b", height: 1000, identity: "g", hashes: map[int64]string{80: pairAt80, 980: "H"}},
		}
		returning := []string{"r1", "r2", "r3"}
		for i, name := range returning {
			probes = append(probes, &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: returningAt80[i]}})
		}
		m, ps := newHealthMgr(c, cfg, nil, probes...)
		c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
		for _, name := range returning {
			m.Entry(name).MarkUnhealthy(endpoints.ErrorKindHTTP)
		}
		for i := 0; i < cfg.MinRecoveryChecks+1; i++ {
			m.Cycle(ctxBg())
		}
		for _, name := range returning {
			c.Assert(m.Entry(name).State(), Not(Equals), endpoints.StateHealthy, Commentf("alone=%v %s", alone, name))
		}
	}
}

// Correct endpoints back from an outage that outnumber a healthy pair on another fork
// running ahead get a hash vote at a height they have, and the pair's tip, which
// they voted against, does not hold back their recovery: once back they outvote the
// pair, which is quarantined. Endpoints that are merely behind (the same hash as the
// healthy pair there) stay out of service.
func (s *HealthSuite) TestReturningMajorityIsNotHeldBackByAForkedPairAhead(c *C) {
	for _, forkedPair := range []bool{true, false} {
		cfg := healthCfg()
		cfg.IntegrityCheckDepth = 20
		cfg.ErrorThreshold = 1
		pairHash := "H"
		if forkedPair {
			pairHash = "X"
		}
		pair := func(name string) *fakeProbe {
			return &fakeProbe{name: name, height: 300, identity: "g", hashes: map[int64]string{80: pairHash, 280: pairHash}}
		}
		returning := func(name string) *fakeProbe {
			return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
		}
		m, ps := newHealthMgr(c, cfg, nil, pair("x"), pair("y"), returning("a"), returning("b"), returning("d"))
		c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
		for _, name := range []string{"a", "b", "d"} {
			m.Entry(name).MarkUnhealthy(endpoints.ErrorKindHTTP)
		}
		for i := 0; i < cfg.MinRecoveryChecks+1; i++ {
			m.Cycle(ctxBg())
		}
		for _, name := range []string{"a", "b", "d"} {
			if forkedPair {
				c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf("%s", name))
			} else {
				c.Assert(m.Entry(name).State(), Not(Equals), endpoints.StateHealthy, Commentf("%s", name))
			}
		}
		for _, name := range []string{"x", "y"} {
			c.Assert(m.Entry(name).IntegrityEvicted(), Equals, forkedPair, Commentf("%s", name))
		}
	}
}

// A hash vote with no majority shows no fork agreement: two endpoints on another fork
// that report heights ahead must not strike the other two for lag (nor the reverse).
// THORChain's height still can.
func (s *HealthSuite) TestSplitHashVoteLetsNoPairEvictTheOther(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{80: "X", 180: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a"), honest("b"), forked("x"), forked("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "x", "y"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf(name))
	}

	// THORChain's height (here the forked pair's) still strikes the pair behind it
	m.SetReferenceHeight(func() (int64, error) { return 200, nil })
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateUnhealthy)
}

// Only endpoints that showed the majority hash form the lag tip: two endpoints ahead
// whose hash at the compared height cannot be read have not shown they are on the
// majority's fork, so they strike nobody.
func (s *HealthSuite) TestEndpointsThatShowNoHashDoNotSetTheLagTip(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	silent := func(name string) *fakeProbe { // answers heights, no hashes
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a"), honest("b"), honest("d"), silent("x"), silent("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "d"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf(name))
	}
}

// A majority that has stalled is still struck by endpoints ahead that showed the
// same hash at the compared height: they are on its fork, only further along.
func (s *HealthSuite) TestStalledMajorityIsStruckByEndpointsAheadOnItsFork(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	stalled := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	ahead := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{80: "H", 180: "H"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, stalled("a"), stalled("b"), stalled("d"), ahead("x"), ahead("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "d"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateUnhealthy, Commentf(name))
	}
	for _, name := range []string{"x", "y"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
	}
}

// THORChain's height counts as reached when any endpoint not shown to be on another
// fork has reached it, including one whose hash could not be read this cycle (it
// does not form the lag tip): endpoints stalled behind the height the network has
// observed are still struck.
func (s *HealthSuite) TestThorchainHeightReachedByAnEndpointWithoutAHashStillStrikes(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	stalled := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	ahead := &fakeProbe{name: "x", height: 130, identity: "g", hashes: map[int64]string{}} // no hash this cycle
	m, ps := newHealthMgr(c, cfg, nil, stalled("a"), stalled("b"), ahead)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.SetReferenceHeight(func() (int64, error) { return 130, nil })
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateHealthy)
}

// A split hash vote suspends only the lag strikes of healthy endpoints: an endpoint
// evicted for lag that is still far behind the others must not pass its recovery
// checks while the vote is split (here THORChain's height is unavailable).
func (s *HealthSuite) TestSplitHashVoteDoesNotLetALaggingEndpointRecover(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: hash}}
	}
	behind := &fakeProbe{name: "r", height: 50, identity: "g", hashes: map[int64]string{}}
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("b", "H"), at("x", "X"), at("y", "X"), behind)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("r").MarkUnhealthy(endpoints.ErrorKindLag)
	for i := 0; i < 2*cfg.MinRecoveryChecks; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("r").State(), Equals, endpoints.StateUnhealthy)
	for _, name := range []string{"a", "b", "x", "y"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
	}
}

// A vote that two or more healthy endpoints were asked for but fewer than two
// answered shows no fork agreement either: a pair ahead that never shows a hash must
// not strike the honest endpoints whose hash reads failed this cycle.
func (s *HealthSuite) TestUnansweredHashVoteLetsNoPairEvictTheOthers(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	answering := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	failing := func(name string) *fakeProbe { // its hash read fails this cycle
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{}}
	}
	silent := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{}}
	}
	m, ps := newHealthMgr(c, cfg, nil, answering, failing("b"), failing("d"), silent("x"), silent("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "d"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf(name))
	}
}

// Endpoints out of service but not quarantined (evicted for request errors or lag)
// that have reached the compared height answer the hash vote too: a healthy majority
// must also hold a strict majority of all those answers. With two endpoints on another
// fork, an honest one that dropped out on request errors still counts, so the pair is
// not a majority and the honest one still voting is not struck.
func (s *HealthSuite) TestOutOfServiceEndpointsCanBlockAHealthyMajority(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("d", "H"), at("x", "X"), at("y", "X"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindHTTP) // dropped out on request errors
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "x", "y"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).IntegrityEvicted(), Equals, false, Commentf(name))
	}
}

// An endpoint out of service but not quarantined that answers another hash than a
// majority that stands is on another fork too: it is quarantined, so it cannot come
// back on probe passes alone.
func (s *HealthSuite) TestOutOfServiceEndpointWithAMinorityHashIsQuarantined(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("b", "H"), at("x", "X"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
}

// An endpoint out of service is probed only when its recovery check is due, but it
// votes in every hash vote, from the height it last reported: otherwise, between its
// recovery checks, two endpoints on another fork would be a majority again and strike
// the honest one still voting until it is quarantined.
func (s *HealthSuite) TestOutOfServiceEndpointVotesBetweenItsRecoveryChecks(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 2
	cfg.RecoveryCheckInterval = time.Hour // probed again only an hour later
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("d", "H"), at("x", "X"), at("y", "X"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindHTTP)
	for i := 0; i < 4; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("a").IntegrityEvicted(), Equals, false)
	c.Assert(m.Entry("a").Snapshot().ProbeStrikes, Equals, 0)
}

// An endpoint out of service that shows a minority hash in a cycle where its recovery
// check is not due (it voted from its last height) is quarantined in that cycle too.
func (s *HealthSuite) TestOutOfServiceMinorityHashBetweenRecoveryChecksIsQuarantined(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	cfg.RecoveryCheckInterval = time.Hour
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	x := at("x", "H")
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("b", "H"), x)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg()) // x's recovery check: it agrees
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, false)
	x.mu.Lock()
	x.hashes[95] = "X"
	x.mu.Unlock()
	m.Cycle(ctxBg()) // not due: it votes from its last height, with another hash
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
}

// A healthy endpoint skipped this cycle for a rate-limit cooldown votes too, from the
// height it last reported: two endpoints on another fork are not a majority because an
// honest one is cooling down.
func (s *HealthSuite) TestCoolingDownEndpointCanBlockAHealthyMajority(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("d", "H"), at("x", "X"), at("y", "X"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.RecordRateLimited("d")
	c.Assert(m.Entry("d").InCooldown(), Equals, true)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("a").IntegrityEvicted(), Equals, false)
}

// Between its recovery checks only an endpoint whose last check passed (recovering)
// votes from its last height: one whose checks fail may not answer at all, and asking
// it every cycle would hold the cycle for up to the probe timeout.
func (s *HealthSuite) TestUnreachableOutOfServiceEndpointIsNotAskedBetweenChecks(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.RecoveryCheckInterval = time.Hour
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	x := at("x", "H")
	var asked atomic.Int32
	counting := voteEvictedProbe{fakeProbe: x, onHash: func() { asked.Add(1) }}
	a, b := at("a", "H"), at("b", "H")
	m, _ := newHealthMgr(c, cfg, nil, a, b, x)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, counting}), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindTransport)
	x.set(100, down)
	m.Cycle(ctxBg()) // its recovery check fails: still unhealthy
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateUnhealthy)
	asked.Store(0)
	m.Cycle(ctxBg()) // not due
	c.Assert(asked.Load(), Equals, int32(0))
}

// When the healthy endpoints are spread over more than the integrity depth (here two on
// another fork report heights far ahead of an honest healthy one), the hashes are
// compared at the lower median of every voter, out-of-service ones included: the pair
// cannot leave the honest endpoints out of the vote and strike the healthy one for lag.
// Nothing here depends on THORChain's height.
func (s *HealthSuite) TestEndpointsBelowAPairAheadStillVote(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{80: "X", 180: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("h1"), honest("h2"), honest("h3"), forked("x"), forked("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("h2").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Entry("h3").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("h1").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("h1").Snapshot().ProbeStrikes, Equals, 0)
	for _, name := range []string{"h1", "x", "y"} {
		c.Assert(m.Entry(name).IntegrityEvicted(), Equals, false, Commentf(name))
	}
}

// Out-of-service endpoints far behind do not lower the compared height while every
// healthy endpoint has reached it, whatever THORChain's height (which can trail the tip
// by many blocks on a quiet chain): a fork among the healthy endpoints is still found.
func (s *HealthSuite) TestLaggersDoNotBlockAMajorityWhateverThorchainsHeight(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	a := &fakeProbe{name: "a", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}
	b := &fakeProbe{name: "b", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}
	x := &fakeProbe{name: "x", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "X"}}
	behind := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 500, identity: "g", hashes: map[int64]string{480: "H"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, a, b, x, behind("r1"), behind("r2"), behind("r3"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"r1", "r2", "r3"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.SetReferenceHeight(func() (int64, error) { return 500, nil }) // trailing the tip
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
}

// A healthy endpoint lagging behind the majority by more than the depth spreads the
// healthy heights, so the hashes are compared at every voter's lower median; when they
// agree there, the lagging endpoint is still struck for lag against the majority's tip.
func (s *HealthSuite) TestLaggingHealthyEndpointIsStillStruckWhenTheHeightsSpread(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	at := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, at("a", 1000), at("b", 1000), at("d", 970), at("r1", 970), at("r2", 970))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("r1").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Entry("r2").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("d").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("r1").State(), Equals, endpoints.StateUnhealthy)
}

// An endpoint already cooling down still votes, but a rate-limited answer to its hash
// read does not extend its cooldown: it is asked every cycle only for the vote.
func (s *HealthSuite) TestRateLimitedHashReadDoesNotExtendACooldown(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
	d := &rateLimitedHashProbe{fakeProbe{name: "d", height: 100, identity: "g"}}
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "d"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, d}), IsNil)
	var mu sync.Mutex
	now := time.Now()
	m.Entry("d").SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	advance := func(by time.Duration) { mu.Lock(); now = now.Add(by); mu.Unlock() }
	m.RecordRateLimited("d") // cooling down for RateLimitBackoff (30s)
	advance(20 * time.Second)
	m.Cycle(ctxBg()) // d votes from its last height; its hash read is rate limited again
	c.Assert(m.Entry("d").InCooldown(), Equals, true)
	advance(15 * time.Second) // past the end of the original cooldown
	c.Assert(m.Entry("d").InCooldown(), Equals, false)
}

// A recovering endpoint whose height probe is rate limited in the cycle its hash vote
// shows a minority hash is quarantined in that cycle, not one later.
func (s *HealthSuite) TestRateLimitedRecoveringEndpointWithAMinorityHashIsQuarantined(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 5
	cfg.ErrorThreshold = 1
	at := func(name, hash string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{95: hash}}
	}
	x := at("x", "H")
	m, ps := newHealthMgr(c, cfg, nil, at("a", "H"), at("b", "H"), x)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg()) // x's first recovery check passes: recovering
	c.Assert(m.Entry("x").State(), Equals, endpoints.StateRecovering)
	x.mu.Lock()
	x.hashes[95] = "X"
	x.err = fmt.Errorf("http 429: %w", endpoints.ErrRateLimited)
	x.mu.Unlock()
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
}

// Huginn's case with real recovery intervals: the honest endpoints out of service fail
// their recovery checks (behind the pair's tip) but answer them, so between checks
// they still vote from the height they last reported: the pair never becomes a
// majority and never strikes the honest healthy endpoint.
func (s *HealthSuite) TestPairAheadIsBlockedBetweenRecoveryChecks(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 2
	cfg.RecoveryCheckInterval = time.Hour
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "H"}}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: map[int64]string{80: "X", 180: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("h1"), honest("h2"), honest("h3"), forked("x"), forked("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("h2").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Entry("h3").MarkUnhealthy(endpoints.ErrorKindHTTP)
	for i := 0; i < 5; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("h1").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("h1").Snapshot().ProbeStrikes, Equals, 0)
	c.Assert(m.Entry("h1").IntegrityEvicted(), Equals, false)
}

// When the healthy heights spread, the compared height never goes below the lowest
// healthy endpoint's level: laggers far behind (out of service) cannot pull it down to a
// height where a fork among the healthy endpoints still agrees, so the fork is found in
// the same cycle.
func (s *HealthSuite) TestSpreadHeightsAreComparedNoLowerThanTheLowestHealthy(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{480: "H", 950: "H", 980: "H"}}
	}
	x := &fakeProbe{name: "x", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 950: "X", 980: "X"}} // forked after 480
	behind := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 500, identity: "g", hashes: map[int64]string{480: "H"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a", 1000), honest("b", 1000), x, honest("e", 970),
		behind("r1"), behind("r2"), behind("r3"), behind("r4"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"r1", "r2", "r3", "r4"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
}

// When the healthy heights spread, the compared height never rises above the healthy
// endpoints' own level: out-of-service endpoints far ahead (here on another fork)
// cannot move the check to a height only they have reached and then strike every
// healthy endpoint for lag.
func (s *HealthSuite) TestSpreadHeightsAreNeverComparedAboveTheHealthyEndpoints(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{880: "H", 980: "H"}}
	}
	ahead := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 1100, identity: "g", hashes: map[int64]string{880: "X", 980: "X", 1080: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a", 1000), honest("b", 1000), honest("e", 900),
		ahead("r1"), ahead("r2"), ahead("r3"), ahead("r4"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"r1", "r2", "r3", "r4"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindHTTP)
	}
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "e"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf(name))
	}
}

// The lower vote (healthy heights spread) only withholds a decision: where the vote at
// the healthy median's height is a tie, agreement lower down (here below a recent fork
// at 975) does not make a majority, so the pair ahead strikes nobody.
func (s *HealthSuite) TestALowerVoteNeverMakesAMajority(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 1
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{940: "H", 950: "H", 980: "H"}}
	}
	forked := func(name string) *fakeProbe { // forked at 975
		return &fakeProbe{name: name, height: 1030, identity: "g", hashes: map[int64]string{940: "H", 950: "H", 980: "X", 1010: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a", 1000), honest("b", 1000), forked("x"), forked("y"), honest("e", 970),
		honest("o1", 960), honest("o2", 960), honest("o3", 960))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"o1", "o2", "o3"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Cycle(ctxBg())
	for _, name := range []string{"a", "b", "e"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
		c.Assert(m.Entry(name).Snapshot().ProbeStrikes, Equals, 0, Commentf(name))
	}
}

// The lower vote never clears a quarantine: q, forked after 950, matches there but not
// at the healthy median's height, so it stays quarantined.
func (s *HealthSuite) TestALowerVoteNeverClearsAQuarantine(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 5
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	q := &fakeProbe{name: "q", height: 1000, identity: "g", hashes: map[int64]string{950: "H", 980: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, honest("a", 1000), honest("b", 1000), honest("e", 970), q, honest("o1", 960))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("o1").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Entry("q").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	for i := 0; i < 3; i++ {
		m.Cycle(ctxBg())
	}
	c.Assert(m.Entry("q").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("q").State(), Equals, endpoints.StateUnhealthy)
}

// The lower vote does not delay finding a fork among the healthy endpoints: x, forked
// after 950, is quarantined in the first cycle, as at the healthy median's height alone.
func (s *HealthSuite) TestALowerVoteDoesNotDelayFindingAFork(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 1
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	x := &fakeProbe{name: "x", height: 1000, identity: "g", hashes: map[int64]string{950: "H", 980: "X"}}
	m, ps := newHealthMgr(c, cfg, nil, honest("a", 1000), honest("b", 1000), x, honest("e", 970), honest("o1", 960), honest("o2", 960))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("o1").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Entry("o2").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
}

// A lower vote that gets too few answers (providers without that older block) withholds
// nothing: the majority at the healthy median's height stands and the fork is found.
func (s *HealthSuite) TestALowerVoteWithoutAnswersWithholdsNothing(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 1
	recent := func(name, hash string) *fakeProbe { // keeps only recent blocks
		return &fakeProbe{name: name, height: 1000, identity: "g", hashes: map[int64]string{980: hash}}
	}
	lagger := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 960, identity: "g", hashes: map[int64]string{}}
	}
	m, ps := newHealthMgr(c, cfg, nil, recent("a", "H"), recent("b", "H"), recent("x", "X"),
		&fakeProbe{name: "e", height: 970, identity: "g", hashes: map[int64]string{}},
		lagger("o1"), lagger("o2"), lagger("o3"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"o1", "o2", "o3"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
}

// A lower vote without two healthy answers withholds nothing even when a quarantined
// endpoint is re-checked there: q, which matched the majority above, is not "another
// hash" because no healthy endpoint could give the older block to compare it with.
func (s *HealthSuite) TestALowerVoteWithOnlyARecheckWithholdsNothing(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 1
	recent := func(name, hash string) *fakeProbe { // keeps only recent blocks
		return &fakeProbe{name: name, height: 1000, identity: "g", hashes: map[int64]string{980: hash}}
	}
	lagger := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 960, identity: "g", hashes: map[int64]string{}}
	}
	q := &fakeProbe{name: "q", height: 1000, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	m, ps := newHealthMgr(c, cfg, nil, recent("a", "H"), recent("b", "H"), recent("x", "X"),
		&fakeProbe{name: "e", height: 970, identity: "g", hashes: map[int64]string{}},
		lagger("o1"), lagger("o2"), lagger("o3"), q)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	for _, name := range []string{"o1", "o2", "o3"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Entry("q").MarkUnhealthy(endpoints.ErrorKindIntegrity)
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("q").State(), Equals, endpoints.StateRecovering)
}

type countingRateLimitedHashProbe struct {
	fakeProbe
	calls atomic.Int32
}

func (p *countingRateLimitedHashProbe) HashAt(context.Context, int64) (string, error) {
	p.calls.Add(1)
	return "", fmt.Errorf("http 429: %w", endpoints.ErrRateLimited)
}

// An endpoint whose hash read was rate limited is not asked again by the lower vote in
// the same cycle.
func (s *HealthSuite) TestALowerVoteDoesNotAskARateLimitedEndpointAgain(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 5
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	limited := &countingRateLimitedHashProbe{fakeProbe: fakeProbe{name: "l", height: 1000, identity: "g"}}
	m, err := endpoints.NewManager(common.BTCChain, eps("l", "a", "b", "e", "o1", "o2"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{
		limited, honest("a", 1000), honest("b", 1000), honest("e", 970), honest("o1", 960), honest("o2", 960),
	}), IsNil)
	m.Entry("o1").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Entry("o2").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg())
	c.Assert(limited.calls.Load(), Equals, int32(1))
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The lower vote names no endpoint as holding another hash: it decides nothing about
// them (o1, out of service, differs only below), so nothing is logged at error level.
func (s *HealthSuite) TestALowerVoteLogsNoEndpointAsDisagreeing(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 5
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	o1 := &fakeProbe{name: "o1", height: 960, identity: "g", hashes: map[int64]string{950: "X"}}
	logs := &syncBuffer{}
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "e", "o1", "o2"), cfg, zerolog.New(logs))
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{honest("a", 1000), honest("b", 1000), honest("e", 970), o1, honest("o2", 960)}), IsNil)
	m.Entry("o1").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Entry("o2").MarkUnhealthy(endpoints.ErrorKindLag)
	m.Cycle(ctxBg())
	c.Assert(strings.Contains(logs.String(), "disagrees with majority"), Equals, false, Commentf(logs.String()))
	c.Assert(m.Entry("o1").IntegrityEvicted(), Equals, false)
}

func hashRange(lo, hi int64, hash string) map[int64]string {
	out := make(map[int64]string, hi-lo+1)
	for h := lo; h <= hi; h++ {
		out[h] = hash
	}
	return out
}

// On a chain that moves more than the depth between recovery checks, the height an
// out-of-service endpoint last reported falls below the lower vote's: it is asked
// anyway (it has moved on with the chain), so the pair ahead is still blocked.
func (s *HealthSuite) TestOutOfServiceEndpointsBlockAPairAheadAfterTheChainMovesOn(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 2
	cfg.RecoveryCheckInterval = time.Hour
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: hashRange(1, 400, "H")}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 200, identity: "g", hashes: hashRange(1, 500, "X")}
	}
	h1, h2, h3, x, y := honest("h1"), honest("h2"), honest("h3"), forked("x"), forked("y")
	m, ps := newHealthMgr(c, cfg, nil, h1, h2, h3, x, y)
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("h2").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Entry("h3").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Cycle(ctxBg()) // h2 and h3 answer their check at 100
	for i := int64(1); i <= 4; i++ {
		for _, p := range []*fakeProbe{h1, h2, h3} {
			p.set(100+50*i, nil)
		}
		x.set(200+50*i, nil)
		y.set(200+50*i, nil)
		m.Cycle(ctxBg())
		c.Assert(m.Entry("h1").State(), Equals, endpoints.StateHealthy, Commentf("cycle %d", i))
		c.Assert(m.Entry("h1").Snapshot().ProbeStrikes, Equals, 0, Commentf("cycle %d", i))
	}
}

type countingHashProbe struct {
	fakeProbe
	calls atomic.Int32
}

func (p *countingHashProbe) HashAt(ctx context.Context, h int64) (string, error) {
	p.calls.Add(1)
	return p.fakeProbe.HashAt(ctx, h)
}

// The lower vote does not ask an endpoint whose check failed or was rate limited
// earlier in the same cycle: r1's check is rate limited (it still votes between checks
// from its last answer) and r2's fails while it recovers (the vote at the healthy
// median's height still asks it from its last height, as before).
func (s *HealthSuite) TestALowerVoteDoesNotAskAnEndpointWhoseCheckFailedThisCycle(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 5
	honest := func(name string, height int64) *fakeProbe {
		return &fakeProbe{name: name, height: height, identity: "g", hashes: map[int64]string{950: "H", 980: "H"}}
	}
	r1 := &countingHashProbe{fakeProbe: *honest("r1", 900)}
	r2 := &countingHashProbe{fakeProbe: *honest("r2", 1000)}
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "e", "o1", "o2", "r1", "r2"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{
		honest("a", 1000), honest("b", 1000), honest("e", 970), honest("o1", 960), honest("o2", 960), r1, r2,
	}), IsNil)
	for _, name := range []string{"o1", "o2", "r1", "r2"} {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Cycle(ctxBg())
	c.Assert(m.Entry("r1").State(), Equals, endpoints.StateUnhealthy)
	c.Assert(m.Entry("r1").CheckAnswered(), Equals, true)
	c.Assert(m.Entry("r2").State(), Equals, endpoints.StateRecovering)
	r1.set(0, fmt.Errorf("http 429: %w", endpoints.ErrRateLimited))
	r2.set(0, down)
	r1.calls.Store(0)
	r2.calls.Store(0)
	m.Cycle(ctxBg())
	c.Assert(r1.calls.Load(), Equals, int32(0))
	c.Assert(r2.calls.Load(), Equals, int32(1))
}

type deadlineAtProbe struct {
	*fakeProbe
	at int64
}

func (p deadlineAtProbe) HashAt(ctx context.Context, h int64) (string, error) {
	if h == p.at {
		return "", context.DeadlineExceeded
	}
	return p.fakeProbe.HashAt(ctx, h)
}

// A lower vote that runs out of the cycle's time (every read there fails) withholds
// nothing: the minority hash the vote above found still quarantines x.
func (s *HealthSuite) TestALowerVoteThatRunsOutOfTimeWithholdsNothing(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	names := []string{"a", "b", "x", "r1", "r2", "r3", "r4"}
	probes := []endpoints.Probe{
		deadlineAtProbe{&fakeProbe{name: "a", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}, 480},
		deadlineAtProbe{&fakeProbe{name: "b", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "H"}}, 480},
		deadlineAtProbe{&fakeProbe{name: "x", height: 1000, identity: "g", hashes: map[int64]string{480: "H", 980: "X"}}, 480},
	}
	for _, name := range names[3:] {
		probes = append(probes, deadlineAtProbe{&fakeProbe{name: name, height: 500, identity: "g", hashes: map[int64]string{480: "H"}}, 480})
	}
	m, err := endpoints.NewManager(common.BTCChain, eps(names...), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), probes), IsNil)
	for _, name := range names[3:] {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindLag)
	}
	m.Cycle(ctxBg())
	c.Assert(m.Entry("x").IntegrityEvicted(), Equals, true)
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
}

// Endpoints that come back behind with a hash a healthy endpoint contradicts recover
// against their own tip only when they outvote the others: an even split (a forked
// pair returning against an honest pair) leaves them held back by the healthy tip.
func (s *HealthSuite) TestReturningEndpointsThatDoNotOutvoteTheOthersAreHeldBack(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.ErrorThreshold = 1
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 300, identity: "g", hashes: map[int64]string{80: "H", 280: "H"}}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 100, identity: "g", hashes: map[int64]string{80: "X"}}
	}
	m, ps := newHealthMgr(c, cfg, nil, honest("a"), honest("b"), forked("x"), forked("y"))
	c.Assert(m.Bootstrap(ctxBg(), ps), IsNil)
	m.Entry("x").MarkUnhealthy(endpoints.ErrorKindHTTP)
	m.Entry("y").MarkUnhealthy(endpoints.ErrorKindHTTP)
	for i := 0; i < cfg.MinRecoveryChecks+1; i++ {
		m.Cycle(ctxBg())
	}
	for _, name := range []string{"x", "y"} {
		c.Assert(m.Entry(name).State(), Not(Equals), endpoints.StateHealthy, Commentf(name))
	}
	for _, name := range []string{"a", "b"} {
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
	}
}

// Returning endpoints outvote the others only by a strict majority of every voter, not
// only of those that answered: three forked endpoints coming back against three honest
// ones, one of which cannot give its hash, are held back.
func (s *HealthSuite) TestReturningHalfDoesNotOutvoteAnHonestEndpointThatCannotAnswer(c *C) {
	cfg := healthCfg()
	cfg.IntegrityCheckDepth = 20
	cfg.MaxBlockLag = 10
	cfg.ErrorThreshold = 2
	cfg.MinRecoveryChecks = 3
	honest := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 1000, identity: "g", hashes: map[int64]string{940: "H", 980: "H"}}
	}
	forked := func(name string) *fakeProbe {
		return &fakeProbe{name: name, height: 960, identity: "g", hashes: map[int64]string{940: "K"}}
	}
	names := []string{"h1", "h2", "h3", "k1", "k2", "k3"}
	probes := []endpoints.Probe{
		honest("h1"), honest("h2"), &errHashProbe{fakeProbe{name: "h3", height: 1000, identity: "g"}},
		forked("k1"), forked("k2"), forked("k3"),
	}
	m, err := endpoints.NewManager(common.BTCChain, eps(names...), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(ctxBg(), probes), IsNil)
	for _, name := range names[3:] {
		m.Entry(name).MarkUnhealthy(endpoints.ErrorKindHTTP)
	}
	for i := 0; i < 8; i++ {
		m.Cycle(ctxBg())
	}
	for _, name := range names[3:] {
		c.Assert(m.Entry(name).State(), Not(Equals), endpoints.StateHealthy, Commentf(name))
	}
	for _, name := range names[:2] {
		c.Assert(m.Entry(name).IntegrityEvicted(), Equals, false, Commentf(name))
		c.Assert(m.Entry(name).State(), Equals, endpoints.StateHealthy, Commentf(name))
	}
}

// An endpoint out of service with a minority hash when the vote reads it is quarantined
// even when the healthy endpoints fail inline while the vote runs and fallback traffic
// brings it back to healthy before the results are applied: both when its recovery
// check runs this cycle and when it votes between checks.
func (s *HealthSuite) TestMinorityHashQuarantinesAnEndpointTrafficRecoveredDuringTheVote(c *C) {
	for _, betweenChecks := range []bool{false, true} {
		cfg := healthCfg()
		cfg.IntegrityCheckDepth = 5
		cfg.ErrorThreshold = 5
		cfg.MinRecoveryChecks = 3
		if betweenChecks {
			cfg.RecoveryCheckInterval = time.Hour
		}
		a := &fakeProbe{name: "a", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
		b := &fakeProbe{name: "b", height: 100, identity: "g", hashes: map[int64]string{95: "H"}}
		xh := "X"
		if betweenChecks {
			xh = "H" // recovering first, so its next check is not due
		}
		xp := &fakeProbe{name: "x", height: 100, identity: "g", hashes: map[int64]string{95: xh}}
		var m *endpoints.Manager
		armed := false
		hook := voteEvictedProbe{fakeProbe: xp, onHash: func() {
			if !armed {
				return
			}
			armed = false
			for i := 0; i < cfg.ErrorThreshold; i++ {
				m.RecordError("a", endpoints.ErrorKindTransport)
				m.RecordError("b", endpoints.ErrorKindTransport)
			}
			for i := 0; i < cfg.MinRecoveryChecks; i++ {
				m.RecordSuccess("x")
			}
		}}
		m, _ = newHealthMgr(c, cfg, nil, a, b, xp)
		c.Assert(m.Bootstrap(ctxBg(), []endpoints.Probe{a, b, hook}), IsNil)
		x := m.Entry("x")
		x.MarkUnhealthy(endpoints.ErrorKindLag)
		if betweenChecks {
			m.Cycle(ctxBg())
			c.Assert(x.State(), Equals, endpoints.StateRecovering)
			xp.mu.Lock()
			xp.hashes[95] = "X"
			xp.mu.Unlock()
		}
		armed = true
		m.Cycle(ctxBg())
		c.Assert(x.IntegrityEvicted(), Equals, true, Commentf("betweenChecks=%v", betweenChecks))
		c.Assert(x.State(), Equals, endpoints.StateUnhealthy, Commentf("betweenChecks=%v", betweenChecks))
		c.Assert(m.Active().Name, Not(Equals), "x")
		c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a", "b"})
	}
}
