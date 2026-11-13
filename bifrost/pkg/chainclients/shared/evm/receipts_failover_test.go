package evm_test

import (
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ReceiptsFailoverSuite is run by the package's existing TestingT call.
type ReceiptsFailoverSuite struct{}

var _ = Suite(&ReceiptsFailoverSuite{})

func receiptsMgr(c *C, names ...string) *endpoints.Manager {
	var eps []config.ChainEndpointConfig
	for i, n := range names {
		eps = append(eps, config.ChainEndpointConfig{Name: n, RPCHost: "http://" + n + ":8545", Priority: i + 1})
	}
	m, err := endpoints.NewManager(common.BSCChain, eps, config.ChainFailoverConfig{
		ErrorThreshold: 5, ErrorWindow: time.Minute, MinRecoveryChecks: 1, RateLimitBackoff: 30 * time.Second,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("g")
	}
	return m
}

// The second failure in a row of the same height from the same active endpoint
// sidelines it; the first does not.
func (s *ReceiptsFailoverSuite) TestSecondFailureSidelinesTheActiveEndpoint(c *C) {
	m := receiptsMgr(c, "a", "b")
	var f evm.ReceiptsFailover
	src := evm.ReceiptsSource(m)
	c.Assert(src, Equals, "a")
	f.Failed(m, src, 100)
	c.Assert(m.Active().Name, Equals, "a")
	f.Failed(m, evm.ReceiptsSource(m), 100)
	c.Assert(m.Active().Name, Equals, "b")
	c.Assert(m.Entry("a").InCooldown(), Equals, true)
}

// Failures at different heights, or separated by a served read, sideline nothing.
func (s *ReceiptsFailoverSuite) TestFailuresThatAreNotInARowSidelineNothing(c *C) {
	m := receiptsMgr(c, "a", "b")
	var f evm.ReceiptsFailover
	f.Failed(m, "a", 100)
	f.Failed(m, "a", 101)
	c.Assert(m.Active().Name, Equals, "a")
	f.Served()
	f.Failed(m, "a", 101)
	c.Assert(m.Active().Name, Equals, "a")
	c.Assert(m.Entry("a").InCooldown(), Equals, false)
}

// When the active role moved during a failed read (a rate limit handed it off), the
// failure may be the new endpoint's first answer: it is not counted against either.
func (s *ReceiptsFailoverSuite) TestAHandoffBetweenFailuresSidelinesNeither(c *C) {
	m := receiptsMgr(c, "a", "b", "z")
	var f evm.ReceiptsFailover
	f.Failed(m, "a", 100)
	m.RecordRateLimited("a") // the next read was handed to b
	c.Assert(m.Active().Name, Equals, "b")
	f.Failed(m, "a", 100) // read started on a, active is now b
	c.Assert(m.Active().Name, Equals, "b")
	f.Failed(m, "b", 100) // b's first failure
	c.Assert(m.Active().Name, Equals, "b")
	c.Assert(m.Entry("b").InCooldown(), Equals, false)
}

// With one endpoint, or no manager, there is no source and nothing is ever sidelined.
func (s *ReceiptsFailoverSuite) TestNothingToMoveToSidelinesNothing(c *C) {
	m := receiptsMgr(c, "a")
	var f evm.ReceiptsFailover
	c.Assert(evm.ReceiptsSource(m), Equals, "")
	c.Assert(evm.ReceiptsSource(nil), Equals, "")
	for i := 0; i < 3; i++ {
		f.Failed(m, evm.ReceiptsSource(m), 100)
		f.Failed(nil, "a", 100)
	}
	c.Assert(m.Entry("a").InCooldown(), Equals, false)
}

// With no other eligible endpoint to take over (the other one lagging, disabled or
// cooling down), there is no source: sidelining would only pause the active one.
func (s *ReceiptsFailoverSuite) TestNoEligibleEndpointToMoveToSidelinesNothing(c *C) {
	m := receiptsMgr(c, "a", "b")
	m.Entry("b").MarkUnhealthy(endpoints.ErrorKindLag)
	c.Assert(evm.ReceiptsSource(m), Equals, "")
	var f evm.ReceiptsFailover
	f.Failed(m, evm.ReceiptsSource(m), 100)
	f.Failed(m, evm.ReceiptsSource(m), 100)
	c.Assert(m.Entry("a").InCooldown(), Equals, false)
}
