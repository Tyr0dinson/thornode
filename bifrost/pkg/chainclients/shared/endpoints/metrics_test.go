package endpoints_test

import (
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
