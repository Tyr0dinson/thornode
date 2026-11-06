package metrics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestInstantSendMetricsExportAndIsolation(t *testing.T) {
	// Collectors must exist even before NewMetrics and without configured chains.
	m := &Metrics{}
	registry := prometheus.NewPedanticRegistry()
	var observedValues [2]float64
	for i, tc := range []struct {
		chain  common.Chain
		amount float64
		gauges [4]float64
	}{
		{common.DASHChain, 2, [4]float64{101, 102, 3, 1}},
		{common.FIROChain, 5, [4]float64{201, 202, 4, 0}},
	} {
		observed := m.GetCounter(InstantSendObserved(tc.chain))
		require.NotNil(t, observed)
		observedValues[i] = testutil.ToFloat64(observed) + tc.amount
		observed.Add(tc.amount)
		require.NoError(t, registry.Register(observed))

		deferred := m.GetCounterVec(InstantSendDeferred(tc.chain))
		require.NotNil(t, deferred)
		deferred.WithLabelValues("metrics_test").Add(tc.amount)
		t.Cleanup(func() { deferred.DeleteLabelValues("metrics_test") })
		require.NoError(t, registry.Register(deferred))

		for j, name := range []MetricName{
			InstantSendReserved(tc.chain), InstantSendAvailable(tc.chain),
			InstantSendPending(tc.chain), InstantSendWarmingUp(tc.chain),
		} {
			gauge := m.GetGauge(name)
			require.NotNil(t, gauge)
			before := testutil.ToFloat64(gauge)
			t.Cleanup(func() { gauge.Set(before) })
			gauge.Set(tc.gauges[j])
			require.NoError(t, registry.Register(gauge))
		}
	}

	// Verify the complete exported contract, including units, help text, types,
	// and labels. Different values ensure neither chain overwrites the other.
	expected := fmt.Sprintf(`
# HELP dash_instantsend_observed_total number of Dash InstantSend observations accepted from mempool
# TYPE dash_instantsend_observed_total counter
dash_instantsend_observed_total %g
# HELP dash_instantsend_deferred_total InstantSend admission attempts deferred, by reason; repeated attempts are counted.
# TYPE dash_instantsend_deferred_total counter
dash_instantsend_deferred_total{reason="metrics_test"} 2
# HELP dash_instantsend_reserved_duffs Customer InstantSend amount awaiting ChainLock settlement, in duffs.
# TYPE dash_instantsend_reserved_duffs gauge
dash_instantsend_reserved_duffs 101
# HELP dash_instantsend_available_duffs Available customer InstantSend allowance at the last mempool scan, in duffs.
# TYPE dash_instantsend_available_duffs gauge
dash_instantsend_available_duffs 102
# HELP dash_instantsend_pending_transactions Customer InstantSend reservations awaiting ChainLock settlement.
# TYPE dash_instantsend_pending_transactions gauge
dash_instantsend_pending_transactions 3
# HELP dash_instantsend_warming_up One while the startup InstantSend snapshot has not completed, zero once ready.
# TYPE dash_instantsend_warming_up gauge
dash_instantsend_warming_up 1
# HELP firo_instantsend_observed_total number of FIRO InstantSend observations accepted from mempool
# TYPE firo_instantsend_observed_total counter
firo_instantsend_observed_total %g
# HELP firo_instantsend_deferred_total InstantSend admission attempts deferred, by reason; repeated attempts are counted.
# TYPE firo_instantsend_deferred_total counter
firo_instantsend_deferred_total{reason="metrics_test"} 5
# HELP firo_instantsend_reserved_atoms Customer InstantSend amount awaiting ChainLock settlement, in atoms.
# TYPE firo_instantsend_reserved_atoms gauge
firo_instantsend_reserved_atoms 201
# HELP firo_instantsend_available_atoms Available customer InstantSend allowance at the last mempool scan, in atoms.
# TYPE firo_instantsend_available_atoms gauge
firo_instantsend_available_atoms 202
# HELP firo_instantsend_pending_transactions Customer InstantSend reservations awaiting ChainLock settlement.
# TYPE firo_instantsend_pending_transactions gauge
firo_instantsend_pending_transactions 4
# HELP firo_instantsend_warming_up One while the startup InstantSend snapshot has not completed, zero once ready.
# TYPE firo_instantsend_warming_up gauge
firo_instantsend_warming_up 0
`, observedValues[0], observedValues[1])
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(expected)))
}
