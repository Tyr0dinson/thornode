package endpoints_test

import (
	"testing"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/config"
)

// The only TestingT call in this package. Every suite in every _test.go file of
// package endpoints_test is run by it.
func TestPackage(t *testing.T) { TestingT(t) }

type EntrySuite struct{}

var _ = Suite(&EntrySuite{})

func fc(threshold, recovery int) config.ChainFailoverConfig {
	return config.ChainFailoverConfig{
		ErrorThreshold:    threshold,
		ErrorWindow:       time.Minute,
		MinRecoveryChecks: recovery,
		RateLimitBackoff:  30 * time.Second,
	}
}

// Mirrors the unrolled table in section 1.1 of the plan, step by step.
func (s *EntrySuite) TestEntryUnrolledLifecycle(c *C) {
	now := time.Unix(1_700_000_000, 0)
	e := endpoints.NewEntry("local", 1)
	e.SetClock(func() time.Time { return now })
	cfg := fc(3, 2)

	e.RecordError(cfg, endpoints.ErrorKindTransport) // 1
	e.RecordError(cfg, endpoints.ErrorKindTransport) // 2
	c.Assert(e.State(), Equals, endpoints.StateHealthy)
	c.Assert(e.Snapshot().InlineErrors, Equals, 2)
	e.RecordSuccess() // 3
	c.Assert(e.Snapshot().InlineErrors, Equals, 0)
	c.Assert(e.RecordProbeStrike(cfg, endpoints.ErrorKindLag), Equals, false) // 4
	e.RecordSuccess()                                                         // 5: inline traffic never clears probe strikes
	c.Assert(e.Snapshot().ProbeStrikes, Equals, 1)
	c.Assert(e.RecordProbeStrike(cfg, endpoints.ErrorKindLag), Equals, false) // 6
	c.Assert(e.State(), Equals, endpoints.StateHealthy)
	c.Assert(e.RecordProbeStrike(cfg, endpoints.ErrorKindIntegrity), Equals, true) // 7
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(e.RecordError(cfg, endpoints.ErrorKindTransport), Equals, false) // 8
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	from, to := e.RecordHealthCheck(false, cfg) // 9
	c.Assert(from, Equals, endpoints.StateUnhealthy)
	c.Assert(to, Equals, endpoints.StateUnhealthy)
	_, to = e.RecordHealthCheck(true, cfg) // 10
	c.Assert(to, Equals, endpoints.StateRecovering)
	c.Assert(e.RecordError(cfg, endpoints.ErrorKindTransport), Equals, true) // 11
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	_, to = e.RecordHealthCheck(true, cfg) // 12
	c.Assert(to, Equals, endpoints.StateRecovering)
	_, to = e.RecordHealthCheck(true, cfg) // 13
	c.Assert(to, Equals, endpoints.StateHealthy)
	e.RecordRateLimited(cfg.RateLimitBackoff) // 14
	c.Assert(e.State(), Equals, endpoints.StateHealthy)
	c.Assert(e.InCooldown(), Equals, true)
	c.Assert(e.Eligible(), Equals, false)
	now = now.Add(31 * time.Second)
	c.Assert(e.InCooldown(), Equals, false)
	c.Assert(e.Eligible(), Equals, true)
	e.Disable("identity mismatch") // 15
	c.Assert(e.State(), Equals, endpoints.StateDisabled)
	_, to = e.RecordHealthCheck(true, cfg) // 16
	c.Assert(to, Equals, endpoints.StateDisabled)
	c.Assert(e.Eligible(), Equals, false)
	c.Assert(e.Snapshot().DisabledReason, Equals, "identity mismatch")
}

func (s *EntrySuite) TestInlineErrorsRestartAfterGap(c *C) {
	now := time.Unix(1_700_000_000, 0)
	cfg := fc(3, 1)

	e := endpoints.NewEntry("a", 1)
	e.SetClock(func() time.Time { return now })
	e.RecordError(cfg, endpoints.ErrorKindTransport) // t=0
	now = now.Add(30 * time.Second)
	e.RecordError(cfg, endpoints.ErrorKindTransport) // t=30
	now = now.Add(29 * time.Second)
	e.RecordError(cfg, endpoints.ErrorKindTransport) // t=59: three consecutive, gaps <= window
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)

	now = time.Unix(1_700_000_000, 0)
	e2 := endpoints.NewEntry("b", 1)
	e2.SetClock(func() time.Time { return now })
	e2.RecordError(cfg, endpoints.ErrorKindTransport) // t=0
	now = now.Add(61 * time.Second)
	e2.RecordError(cfg, endpoints.ErrorKindTransport) // t=61: gap > window restarts the run
	c.Assert(e2.Snapshot().InlineErrors, Equals, 1)
	c.Assert(e2.State(), Equals, endpoints.StateHealthy)
}

func (s *EntrySuite) TestIdentityGatesRecoveryWhenRequired(c *C) {
	cfg := fc(1, 1)
	e := endpoints.NewEntry("a", 1)
	e.RequireIdentity(true)
	e.MarkUnhealthy(endpoints.ErrorKindProbe)
	_, to := e.RecordHealthCheck(true, cfg)
	c.Assert(to, Equals, endpoints.StateUnhealthy) // passing check does not count while unverified
	e.SetIdentity("g")
	_, to = e.RecordHealthCheck(true, cfg)
	c.Assert(to, Equals, endpoints.StateHealthy)
	c.Assert(e.IdentityVerified(), Equals, true)
	c.Assert(e.Identity(), Equals, "g")
}

func (s *EntrySuite) TestMarkUnhealthyAndSnapshot(c *C) {
	e := endpoints.NewEntry("local", 1)
	e.RecordProbe(123, 40*time.Millisecond)
	snap := e.Snapshot()
	c.Assert(snap.Height, Equals, int64(123))
	c.Assert(snap.ProbeLatencyMs, Equals, int64(40))
	c.Assert(snap.State, Equals, "healthy")
	e.MarkUnhealthy(endpoints.ErrorKindProbe)
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(e.Snapshot().LastErrorKind, Equals, "probe")
	c.Assert(e.RecoveryDue(0), Equals, true)
	c.Assert(e.RecoveryDue(time.Hour), Equals, true)
	c.Assert(e.RecoveryDue(time.Hour), Equals, false)
}

// The health loop read the entry as healthy and holds a minority hash for it, but
// inline errors evicted it before the strike arrived: the strike still quarantines it.
func (s *EntrySuite) TestIntegrityStrikeAfterAnInlineEvictionQuarantines(c *C) {
	cfg := fc(2, 2)
	e := endpoints.NewEntry("x", 1)
	e.RecordError(cfg, endpoints.ErrorKindHTTP)
	e.RecordError(cfg, endpoints.ErrorKindHTTP)
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(e.IntegrityEvicted(), Equals, false)

	c.Assert(e.RecordProbeStrike(cfg, endpoints.ErrorKindIntegrity), Equals, false)
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(e.IntegrityEvicted(), Equals, true)
}

// The same when the evicted entry already passed a recovery check: it goes back to
// unhealthy and is quarantined.
func (s *EntrySuite) TestIntegrityStrikeOnARecoveringEntryQuarantines(c *C) {
	cfg := fc(2, 2)
	e := endpoints.NewEntry("x", 1)
	e.RecordError(cfg, endpoints.ErrorKindHTTP)
	e.RecordError(cfg, endpoints.ErrorKindHTTP)
	e.RecordHealthCheck(true, cfg)
	c.Assert(e.State(), Equals, endpoints.StateRecovering)

	c.Assert(e.RecordProbeStrike(cfg, endpoints.ErrorKindIntegrity), Equals, true)
	c.Assert(e.State(), Equals, endpoints.StateUnhealthy)
	c.Assert(e.IntegrityEvicted(), Equals, true)
}

// Only a minority hash quarantines: a lag or probe strike that arrives after an inline
// eviction does not.
func (s *EntrySuite) TestOtherStrikesAfterAnInlineEvictionDoNotQuarantine(c *C) {
	cfg := fc(2, 2)
	for _, kind := range []endpoints.ErrorKind{endpoints.ErrorKindLag, endpoints.ErrorKindProbe} {
		e := endpoints.NewEntry("x", 1)
		e.RecordError(cfg, endpoints.ErrorKindHTTP)
		e.RecordError(cfg, endpoints.ErrorKindHTTP)
		e.RecordProbeStrike(cfg, kind)
		c.Assert(e.IntegrityEvicted(), Equals, false, Commentf("%s", kind))
		e.RecordHealthCheck(true, cfg)
		e.RecordProbeStrike(cfg, kind)
		c.Assert(e.IntegrityEvicted(), Equals, false, Commentf("recovering, %s", kind))
	}
}
