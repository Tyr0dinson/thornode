package endpoints_test

import (
	"context"
	"errors"
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
	c.Assert(m.Entry("backup").IdentityVerified(), Equals, true)
	c.Assert(m.ReferenceIdentity(), Equals, "g1")
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

func (s *HealthSuite) TestBootstrapMajorityWhenPriorityOneDown(c *C) {
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

// Mirrors the unrolled table in section 1.3 of the plan.
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
	slow := &slowProbe{fakeProbe: fakeProbe{name: "slow", height: 1, identity: "g"}, delay: 3 * time.Second}
	m, err := endpoints.NewManager(common.BTCChain, eps("slow"), cfg, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(m.Bootstrap(context.Background(), []endpoints.Probe{slow}), IsNil) // bootstrap itself waits up to timeout... use a short one
	m.Start()
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	m.Stop()
	c.Assert(time.Since(start) < time.Second, Equals, true)
}
