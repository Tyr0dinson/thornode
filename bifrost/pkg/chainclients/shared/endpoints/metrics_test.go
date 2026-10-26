package endpoints_test

import (
	"bytes"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/common"
)

type MetricsSuite struct{}

var _ = Suite(&MetricsSuite{})

func (s *MetricsSuite) TestNewMetricsRegistersOnce(c *C) {
	reg := prometheus.NewRegistry()
	m, err := endpoints.NewMetrics(reg)
	c.Assert(err, IsNil)
	c.Assert(m, NotNil)
	_, err = endpoints.NewMetrics(reg)
	c.Assert(err, NotNil)
}

func (s *MetricsSuite) TestDefaultMetricsIsSingleton(c *C) {
	a := endpoints.DefaultMetrics()
	b := endpoints.DefaultMetrics()
	c.Assert(a, Equals, b)
}

func (s *MetricsSuite) TestManagerPublishesStateActiveAndFailover(c *C) {
	reg := prometheus.NewRegistry()
	m, err := endpoints.NewMetrics(reg)
	c.Assert(err, IsNil)
	mgr, err := endpoints.NewManager(common.ETHChain, eps("local", "backup"), fc(1, 1), zerolog.Nop(), endpoints.WithMetrics(m))
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("0x1")
	}

	c.Assert(testutil.ToFloat64(m.ActiveGauge().WithLabelValues("ETH", "local")), Equals, 1.0)
	c.Assert(testutil.ToFloat64(m.AllDownGauge().WithLabelValues("ETH")), Equals, 0.0)

	mgr.RecordError("local", endpoints.ErrorKindTransport)
	c.Assert(testutil.ToFloat64(m.StateGauge().WithLabelValues("ETH", "local")), Equals, float64(endpoints.StateUnhealthy))
	c.Assert(testutil.ToFloat64(m.ActiveGauge().WithLabelValues("ETH", "backup")), Equals, 1.0)
	c.Assert(testutil.ToFloat64(m.FailoversCounter().WithLabelValues("ETH", "local", "backup", "failover")), Equals, 1.0)
	c.Assert(testutil.ToFloat64(m.ErrorsCounter().WithLabelValues("ETH", "local", "transport")), Equals, 1.0)

	mgr.RecordError("backup", endpoints.ErrorKindHTTP)
	c.Assert(testutil.ToFloat64(m.AllDownGauge().WithLabelValues("ETH")), Equals, 1.0)
	c.Assert(m.HeightGauge(), NotNil)
}

// A signer-cache verification that stays ambiguous (automatic re-sign blocked) is
// counted against each endpoint that kept it so, one whose lookup failed and one
// whose network was never verified, and a warning names it at most once per five
// minutes per endpoint. An endpoint that answered is not counted.
func (s *MetricsSuite) TestAmbiguousVerificationNamesTheBlockingEndpoint(c *C) {
	reg := prometheus.NewRegistry()
	m, err := endpoints.NewMetrics(reg)
	c.Assert(err, IsNil)
	now := time.Unix(1_000_000, 0)
	var logs bytes.Buffer
	mgr, err := endpoints.NewManager(common.ETHChain, eps("a", "unverified", "dead"), fc(1, 1), zerolog.New(&logs), endpoints.WithMetrics(m), endpoints.WithClock(func() time.Time { return now }))
	c.Assert(err, IsNil)
	mgr.Entry("a").SetIdentity("0x1")
	mgr.Entry("dead").SetIdentity("0x1")
	blocked := func(name string) float64 {
		return testutil.ToFloat64(m.VerificationBlockedCounter().WithLabelValues("ETH", name))
	}
	warnings := func() int { return strings.Count(logs.String(), "blocks automatic re-sign") }

	mgr.NoteAmbiguousVerification(0, "dead")
	c.Assert(blocked("dead"), Equals, 1.0)
	c.Assert(blocked("unverified"), Equals, 1.0)
	c.Assert(blocked("a"), Equals, 0.0)
	c.Assert(warnings(), Equals, 2)

	mgr.NoteAmbiguousVerification(0, "dead")
	c.Assert(blocked("dead"), Equals, 2.0)
	c.Assert(warnings(), Equals, 2) // throttled

	now = now.Add(6 * time.Minute)
	mgr.NoteAmbiguousVerification(0, "dead")
	c.Assert(warnings(), Equals, 4)
}
