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
	c.Assert(names(m.VerificationCandidates(time.Hour)), DeepEquals, []string{"a", "b", "d"})

	m.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	now = now.Add(2 * time.Hour) // b has been down longer than the cache expiry
	m.Disable("d", "identity")
	c.Assert(names(m.VerificationCandidates(time.Hour)), DeepEquals, []string{"a"})
	// a briefly-down endpoint still counts, and maxDown <= 0 keeps everything
	c.Assert(names(m.VerificationCandidates(3*time.Hour)), DeepEquals, []string{"a", "b"})
	c.Assert(names(m.VerificationCandidates(0)), DeepEquals, []string{"a", "b"})
	c.Assert(m.Entry("a").NonHealthyFor(), Equals, time.Duration(0))
}
