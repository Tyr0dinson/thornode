package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestInstantSendMetricsChainIsolation(t *testing.T) {
	counters := make(map[MetricName]prometheus.Counter)
	vectors := make(map[MetricName]*prometheus.CounterVec)
	gauges := make(map[MetricName]prometheus.Gauge)
	bash := common.Chain("BASH")
	for _, chain := range []common.Chain{common.DASHChain, bash} {
		addInstantSendMetrics(chain, counters, vectors, gauges)
	}
	counters[InstantSendObserved(common.DASHChain)].Inc()
	gauges[InstantSendExposure(common.DASHChain)].Set(10)
	gauges[InstantSendExposure(bash)].Set(20)
	vectors[InstantSendDeferred(common.DASHChain)].WithLabelValues("limit").Inc()
	require.Equal(t, float64(0), testutil.ToFloat64(counters[InstantSendObserved(bash)]))
	require.Equal(t, float64(0), testutil.ToFloat64(vectors[InstantSendDeferred(bash)].WithLabelValues("limit")))
	require.Equal(t, float64(10), testutil.ToFloat64(gauges[InstantSendExposure(common.DASHChain)]))
	require.Equal(t, float64(20), testutil.ToFloat64(gauges[InstantSendExposure(bash)]))
	registry := prometheus.NewRegistry()
	registry.MustRegister(gauges[InstantSendExposure(common.DASHChain)], gauges[InstantSendExposure(bash)])
	metrics, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, metrics, 2)
	require.Equal(t, "bash_instantsend_exposure_base_units", metrics[0].GetName())
	require.Equal(t, "dash_instantsend_exposure_duffs", metrics[1].GetName())
}
