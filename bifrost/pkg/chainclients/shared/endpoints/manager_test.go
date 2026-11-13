package endpoints_test

import (
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type ManagerSuite struct{}

var _ = Suite(&ManagerSuite{})

func eps(names ...string) []config.ChainEndpointConfig {
	out := make([]config.ChainEndpointConfig, len(names))
	for i, n := range names {
		out[i] = config.ChainEndpointConfig{Name: n, RPCHost: "http://" + n, Priority: i + 1}
	}
	return out
}

func newMgr(c *C, fcfg config.ChainFailoverConfig, names ...string) *endpoints.Manager {
	m, err := endpoints.NewManager(common.BTCChain, eps(names...), fcfg, zerolog.Nop())
	c.Assert(err, IsNil)
	// tests that do not bootstrap treat every entry as verified
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	return m
}

func names(entries []*endpoints.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func (s *ManagerSuite) TestNewManagerRejectsBadInput(c *C) {
	_, err := endpoints.NewManager(common.BTCChain, nil, fc(1, 1), zerolog.Nop())
	c.Assert(err, ErrorMatches, ".*at least one endpoint.*")
	_, err = endpoints.NewManager(common.BTCChain, eps("a", "a"), fc(1, 1), zerolog.Nop())
	c.Assert(err, ErrorMatches, ".*duplicate.*")
}

func (s *ManagerSuite) TestEntriesSortedByPriorityStable(c *C) {
	in := []config.ChainEndpointConfig{
		{Name: "c", RPCHost: "http://c", Priority: 2},
		{Name: "a", RPCHost: "http://a", Priority: 1},
		{Name: "b", RPCHost: "http://b", Priority: 2},
	}
	m, err := endpoints.NewManager(common.BTCChain, in, fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(names(m.Entries()), DeepEquals, []string{"a", "c", "b"})
	c.Assert(m.Names(), DeepEquals, []string{"a", "c", "b"})
	c.Assert(m.Active().Name, Equals, "a")
	ordered := endpoints.OrderedEndpoints(m, in)
	c.Assert(ordered[0].Name, Equals, "a")
	c.Assert(ordered[2].Name, Equals, "b")
	c.Assert(endpoints.EndpointByName(in, "zzz").Name, Equals, "")
}

func (s *ManagerSuite) TestActiveIsStickyUntilUnhealthy(c *C) {
	m := newMgr(c, fc(1, 1), "local", "backup")
	c.Assert(m.Active().Name, Equals, "local")
	c.Assert(m.RecordError("local", endpoints.ErrorKindTransport), Equals, true) // threshold 1 -> failover
	c.Assert(m.Active().Name, Equals, "backup")
	m.Entry("local").RecordHealthCheck(true, fc(1, 1))
	c.Assert(m.Entry("local").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "backup")
	c.Assert(m.MaybeFailback(), Equals, false)
}

func (s *ManagerSuite) TestPreferHigherPriorityFailsBack(c *C) {
	cfg := fc(1, 1)
	cfg.PreferHigherPriority = true
	m := newMgr(c, cfg, "local", "backup")
	m.RecordError("local", endpoints.ErrorKindTransport)
	c.Assert(m.Active().Name, Equals, "backup")
	m.Entry("local").RecordHealthCheck(true, cfg)
	c.Assert(m.MaybeFailback(), Equals, true)
	c.Assert(m.Active().Name, Equals, "local")
}

func (s *ManagerSuite) TestReadCandidatesOrderAndCooldown(c *C) {
	m := newMgr(c, fc(5, 1), "a", "b", "c")
	m.RecordError("a", endpoints.ErrorKindTransport) // below threshold, still healthy
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a", "b", "c"})
	for i := 0; i < 5; i++ {
		m.RecordError("a", endpoints.ErrorKindTransport)
	}
	c.Assert(m.Active().Name, Equals, "b")
	m.Entry("a").RecordHealthCheck(true, fc(5, 1))
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"b", "a", "c"})
	m.RecordRateLimited("c")
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"b", "a"})
	c.Assert(m.WriteTarget().Name, Equals, "b")
}

func (s *ManagerSuite) TestReadCandidatesWhenAllDownTriesEverythingButDisabled(c *C) {
	m := newMgr(c, fc(1, 1), "a", "b", "c")
	m.RecordError("a", endpoints.ErrorKindTransport)
	m.RecordError("b", endpoints.ErrorKindTransport)
	m.RecordError("c", endpoints.ErrorKindTransport)
	c.Assert(m.AllDown(), Equals, true)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a", "b", "c"})
	// each RecordError on the active entry reselected, so active ended on c
	c.Assert(m.Active().Name, Equals, "c")
	// a disabled active is replaced by the best non-disabled entry even with nothing healthy
	m.Disable("c", "identity")
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"a", "b"})
	c.Assert(m.Active().Name, Equals, "a")
	m.Cycle(ctxBg()) // no probes bound: must not panic
	c.Assert(m.Active().Name, Equals, "a")
}

func (s *ManagerSuite) TestRateLimitedActiveHandsOffToEligibleEndpoint(c *C) {
	m := newMgr(c, fc(5, 1), "a", "b")
	m.RecordRateLimited("a")
	c.Assert(m.Active().Name, Equals, "b")
	c.Assert(m.Entry("a").State(), Equals, endpoints.StateHealthy)
	m.RecordRateLimited("b") // nothing eligible: stays sticky
	c.Assert(m.Active().Name, Equals, "b")
}

func (s *ManagerSuite) TestInlineSuccessWhileAllDownCountsAsRecoveryCheck(c *C) {
	m := newMgr(c, fc(1, 2), "a", "b")
	m.RecordError("a", endpoints.ErrorKindTransport)
	m.RecordError("b", endpoints.ErrorKindTransport)
	c.Assert(m.AllDown(), Equals, true)
	m.RecordSuccess("b")
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateRecovering)
	m.RecordSuccess("b")
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "b")
}

func (s *ManagerSuite) TestInlineSuccessNeverPromotesUnverifiedEndpoint(c *C) {
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b"), fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	m.RecordError("a", endpoints.ErrorKindTransport)
	m.RecordError("b", endpoints.ErrorKindTransport)
	m.RecordSuccess("b")
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateUnhealthy)
	m.Entry("b").SetIdentity("g")
	m.RecordSuccess("b")
	c.Assert(m.Entry("b").State(), Equals, endpoints.StateHealthy)
}

func (s *ManagerSuite) TestStatusMarksActive(c *C) {
	m := newMgr(c, fc(1, 1), "a", "b")
	st := m.Status()
	c.Assert(st, HasLen, 2)
	c.Assert(st[0].Name, Equals, "a")
	c.Assert(st[0].Active, Equals, true)
	c.Assert(st[1].Active, Equals, false)
	c.Assert(st[0].State, Equals, "healthy")
}

func (s *ManagerSuite) TestStopIsIdempotentAndNilSafe(c *C) {
	m := newMgr(c, fc(1, 1), "a")
	m.Stop()
	m.Stop()
	var nilMgr *endpoints.Manager
	nilMgr.Stop()
	nilMgr.Start()
	nilMgr.SetScannerHeight(nil)
}

func (s *ManagerSuite) TestWriteTargetIsNilWhenEveryEndpointDisabled(c *C) {
	m := newMgr(c, fc(1, 1), "a", "b")
	m.Disable("b", "identity")
	m.Disable("a", "identity")
	c.Assert(m.WriteTarget(), IsNil)
	c.Assert(m.ReadCandidates(), HasLen, 0)
}

func (s *ManagerSuite) TestFailbackSkipsCoolingDownEndpoint(c *C) {
	cfg := fc(1, 1)
	cfg.PreferHigherPriority = true
	m := newMgr(c, cfg, "a", "b")
	m.RecordRateLimited("a") // hands off to b while a cools down
	c.Assert(m.Active().Name, Equals, "b")
	c.Assert(m.MaybeFailback(), Equals, false) // a is healthy but not eligible
	c.Assert(m.Active().Name, Equals, "b")
}

func (s *ManagerSuite) TestVerificationCandidatesSkipDisabledAndLongDead(c *C) {
	now := time.Unix(1_700_000_000, 0)
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "d"), fc(1, 1), zerolog.Nop(),
		endpoints.WithClock(func() time.Time { return now }))
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	verified := func(maxDown time.Duration) []string {
		got, unverified := m.VerificationCandidates(maxDown)
		c.Assert(unverified, Equals, 0) // every endpoint here has a verified identity
		return names(got)
	}
	c.Assert(verified(time.Hour), DeepEquals, []string{"a", "b", "d"})

	m.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	now = now.Add(2 * time.Hour) // b has been down longer than the cache expiry
	m.Disable("d", "identity")
	c.Assert(verified(time.Hour), DeepEquals, []string{"a"})
	// a briefly-down endpoint still counts, and maxDown <= 0 keeps everything
	c.Assert(verified(3*time.Hour), DeepEquals, []string{"a", "b"})
	c.Assert(verified(0), DeepEquals, []string{"a", "b"})
	c.Assert(m.Entry("a").NonHealthyFor(), Equals, time.Duration(0))
}

// An endpoint whose identity has not been verified since bifrost started (one that
// was unreachable at startup) cannot be asked, but may have taken a broadcast before
// a restart: it is counted, so verification cannot conclude the broadcast is absent.
// A disabled one (a proven wrong network) is not, and maxDown skips it like any
// long-dead endpoint.
func (s *ManagerSuite) TestVerificationCandidatesCountUnverified(c *C) {
	now := time.Unix(1_700_000_000, 0)
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "d"), fc(1, 1), zerolog.Nop(),
		endpoints.WithClock(func() time.Time { return now }))
	c.Assert(err, IsNil)
	m.Entry("a").SetIdentity("g") // b and d never reported one
	m.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	m.Entry("d").MarkUnhealthy(endpoints.ErrorKindTransport)
	got, unverified := m.VerificationCandidates(0)
	c.Assert(names(got), DeepEquals, []string{"a"})
	c.Assert(unverified, Equals, 2)

	m.Disable("d", "identity")
	got, unverified = m.VerificationCandidates(0)
	c.Assert(names(got), DeepEquals, []string{"a"})
	c.Assert(unverified, Equals, 1)

	now = now.Add(2 * time.Hour) // b has been down longer than maxDown
	got, unverified = m.VerificationCandidates(time.Hour)
	c.Assert(names(got), DeepEquals, []string{"a"})
	c.Assert(unverified, Equals, 0)
}

// An endpoint evicted for a minority block hash is quarantined: it is left out of the
// all-down fallback list, inline successes never count as recovery checks for it, and
// the active role moves off it while a non-quarantined endpoint exists. Only the
// health loop's hash check (health_test) may clear the quarantine.
func (s *ManagerSuite) TestIntegrityEvictedEndpointIsQuarantinedUntilHashMatches(c *C) {
	m := newMgr(c, fc(1, 2), "a", "b", "d")
	a, b, d := m.Entry("a"), m.Entry("b"), m.Entry("d")
	// a (active) is evicted and the health loop would have reselected; then b and d
	// go down one after the other
	c.Assert(m.RecordError("a", endpoints.ErrorKindTransport), Equals, true)
	a.MarkUnhealthy(endpoints.ErrorKindIntegrity)
	c.Assert(a.IntegrityEvicted(), Equals, true)
	c.Assert(m.Active().Name, Equals, "b")
	c.Assert(m.RecordError("b", endpoints.ErrorKindTransport), Equals, true)
	c.Assert(m.RecordError("d", endpoints.ErrorKindTransport), Equals, true)
	c.Assert(m.AllDown(), Equals, true)

	// fallback traffic goes to the merely-unreachable endpoints, never to the fork
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"b", "d"})
	c.Assert(m.Active().Name, Not(Equals), "a")

	// inline successes recover b but leave a quarantined
	for i := 0; i < 3; i++ {
		m.RecordSuccess("a")
		m.RecordSuccess("b")
	}
	c.Assert(b.State(), Equals, endpoints.StateHealthy)
	c.Assert(a.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(a.IntegrityEvicted(), Equals, true)
	c.Assert(d.State(), Equals, endpoints.StateUnhealthy)

	// with every other endpoint disabled the quarantined one is still not used
	m.Disable("b", "test")
	m.Disable("d", "test")
	c.Assert(m.ReadCandidates(), HasLen, 0)
}

// Inline errors that complete an eviction after a minority-hash strike keep the
// integrity evidence: the endpoint is quarantined, not just unhealthy.
func (s *ManagerSuite) TestInlineEvictionAfterAnIntegrityStrikeQuarantines(c *C) {
	m := newMgr(c, fc(2, 1), "a", "b")
	cfg := m.Config()
	a := m.Entry("a")
	c.Assert(a.RecordProbeStrike(cfg, endpoints.ErrorKindIntegrity), Equals, false) // 1 of 2: still healthy
	c.Assert(a.IntegrityEvicted(), Equals, false)
	m.RecordError("a", endpoints.ErrorKindTransport)
	c.Assert(m.RecordError("a", endpoints.ErrorKindTransport), Equals, true)
	c.Assert(a.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(a.IntegrityEvicted(), Equals, true)
	c.Assert(names(m.ReadCandidates()), DeepEquals, []string{"b"})

	// a passing probe between the strike and the eviction clears the evidence
	m2 := newMgr(c, fc(2, 1), "a", "b")
	a2 := m2.Entry("a")
	a2.RecordProbeStrike(m2.Config(), endpoints.ErrorKindIntegrity)
	a2.RecordProbePass()
	m2.RecordError("a", endpoints.ErrorKindTransport)
	m2.RecordError("a", endpoints.ErrorKindTransport)
	c.Assert(a2.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(a2.IntegrityEvicted(), Equals, false)
}

// With nothing healthy and the active endpoint disabled, the all-down fallback moves
// to a usable endpoint: one whose identity was never verified would refuse every
// write, and nothing would ever move the active role off it.
func (s *ManagerSuite) TestAllDownFallbackSkipsAnUnverifiedEndpoint(c *C) {
	m, err := endpoints.NewManager(common.BTCChain, eps("a", "b", "c"), fc(1, 1), zerolog.Nop())
	c.Assert(err, IsNil)
	m.Entry("a").SetIdentity("g")
	m.Entry("c").SetIdentity("g") // b never verified
	m.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	m.Entry("c").MarkUnhealthy(endpoints.ErrorKindTransport)
	m.Disable("a", "identity")
	c.Assert(m.Active().Name, Equals, "c")
	c.Assert(m.WriteTarget(), NotNil)
	c.Assert(m.WriteTarget().Name, Equals, "c")
}

// The router passed RecordSuccess's quarantine guard and the health loop then
// quarantined the entry: the inline promotion re-checks the quarantine under the
// entry's lock and refuses it. A matched hash still clears the quarantine, and an
// entry that is not quarantined still recovers from traffic.
func (s *ManagerSuite) TestInlineRecoveryRefusesAQuarantineAppliedAfterTheGuard(c *C) {
	cfg := healthCfg()
	cfg.ErrorThreshold = 1
	cfg.MinRecoveryChecks = 1
	m := newMgr(c, cfg, "a", "b", "x")
	x := m.Entry("x")
	for _, n := range []string{"a", "b", "x"} {
		m.RecordError(n, endpoints.ErrorKindTransport)
	}
	c.Assert(x.IntegrityEvicted(), Equals, false)
	c.Assert(m.AllDown(), Equals, true)
	x.MarkUnhealthy(endpoints.ErrorKindIntegrity)
	x.RecordHealthCheck(false, cfg)
	from, to := x.RecordInlineRecovery(cfg)
	c.Assert(from, Equals, endpoints.StateUnhealthy)
	c.Assert(to, Equals, endpoints.StateUnhealthy)
	c.Assert(x.IntegrityEvicted(), Equals, true)
	m.RecordSuccess("x")
	c.Assert(x.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(x.IntegrityEvicted(), Equals, true)

	_, to = x.RecordHealthCheck(true, cfg)
	c.Assert(to, Equals, endpoints.StateHealthy)
	c.Assert(x.IntegrityEvicted(), Equals, false)

	_, to = m.Entry("a").RecordInlineRecovery(cfg)
	c.Assert(to, Equals, endpoints.StateHealthy)
}
