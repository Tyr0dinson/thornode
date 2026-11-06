package metrics

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"gitlab.com/thorchain/thornode/v3/common"
)

func instantSendName(chain common.Chain, name string) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_" + name)
}

func InstantSendObserved(chain common.Chain) MetricName {
	return instantSendName(chain, "observed")
}

func InstantSendDeferred(chain common.Chain) MetricName {
	return instantSendName(chain, "deferred")
}

func InstantSendReserved(chain common.Chain) MetricName {
	return instantSendName(chain, "reserved")
}

func InstantSendAvailable(chain common.Chain) MetricName {
	return instantSendName(chain, "available")
}

func InstantSendPending(chain common.Chain) MetricName {
	return instantSendName(chain, "pending")
}

func InstantSendWarmingUp(chain common.Chain) MetricName {
	return instantSendName(chain, "warming_up")
}

func init() {
	// Keep these collectors available before NewMetrics, independently of the
	// configured scanner chains, as they were with static map entries.
	addInstantSendMetrics(common.DASHChain, "Dash", "duffs")
	addInstantSendMetrics(common.FIROChain, "FIRO", "atoms")
}

func addInstantSendMetrics(chain common.Chain, displayName, unit string) {
	namespace := strings.ToLower(chain.String())
	counters[InstantSendObserved(chain)] = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "observed_total",
		Help: "number of " + displayName + " InstantSend observations accepted from mempool",
	})
	counterVecs[InstantSendDeferred(chain)] = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "deferred_total",
		Help: "InstantSend admission attempts deferred, by reason; repeated attempts are counted.",
	}, []string{"reason"})
	gauges[InstantSendReserved(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "reserved_" + unit,
		Help: "Customer InstantSend amount awaiting ChainLock settlement, in " + unit + ".",
	})
	gauges[InstantSendAvailable(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "available_" + unit,
		Help: "Available customer InstantSend allowance at the last mempool scan, in " + unit + ".",
	})
	gauges[InstantSendPending(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "pending_transactions",
		Help: "Customer InstantSend reservations awaiting ChainLock settlement.",
	})
	gauges[InstantSendWarmingUp(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "warming_up",
		Help: "One while the startup InstantSend snapshot has not completed, zero once ready.",
	})
}
