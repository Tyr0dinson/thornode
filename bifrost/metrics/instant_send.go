package metrics

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"gitlab.com/thorchain/thornode/v3/common"
)

// InstantSendObserved identifies the metric for one chain.
func InstantSendObserved(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_observed")
}

// InstantSendDeferred identifies the metric for one chain.
func InstantSendDeferred(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_deferred")
}

// InstantSendExposure identifies the metric for one chain.
func InstantSendExposure(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_exposure")
}

// InstantSendAvailable identifies the metric for one chain.
func InstantSendAvailable(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_available")
}

// InstantSendPending identifies the metric for one chain.
func InstantSendPending(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_pending")
}

// InstantSendPaused identifies the metric for one chain.
func InstantSendPaused(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_paused")
}

func InstantSendMinFeeRate(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_instantsend_min_fee_rate")
}

func ObservationSkipped(chain common.Chain) MetricName {
	return MetricName(strings.ToLower(chain.String()) + "_observation_skipped")
}

// addInstantSendMetrics gives each client its own collectors.
func addInstantSendMetrics(chain common.Chain, counters map[MetricName]prometheus.Counter, counterVecs map[MetricName]*prometheus.CounterVec, gauges map[MetricName]prometheus.Gauge) {
	namespace := strings.ToLower(chain.String())
	counterVecs[ObservationSkipped(chain)] = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "observation", Name: "skipped_total",
		Help: "Transactions skipped during extraction, by reason; rescans are counted again.",
	}, []string{"reason"})
	gauges[InstantSendMinFeeRate(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "min_fee_rate",
		Help: "Current InstantSend admission fee floor in base units per virtual byte.",
	})
	counters[InstantSendObserved(chain)] = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "instantsend",
		Name:      "observed_total",
		Help:      "InstantSend observations emitted by the local mempool scanner; network credit is not implied.",
	})
	counterVecs[InstantSendDeferred(chain)] = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "deferred_total",
		Help: "InstantSend admission attempts deferred, by reason; repeated attempts are counted.",
	}, []string{"reason"})
	gauges[InstantSendExposure(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: amountMetric(chain, "exposure"),
		Help: "Network InstantSend exposure awaiting ChainLock settlement, in base units.",
	})
	gauges[InstantSendAvailable(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: amountMetric(chain, "available"),
		Help: "Available network InstantSend allowance at the last successful query, in base units.",
	})
	gauges[InstantSendPending(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "pending_transactions",
		Help: "Network customer InstantSend deposits awaiting ChainLock settlement.",
	})
	gauges[InstantSendPaused(chain)] = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "instantsend", Name: "paused",
		Help: "One while the last admission check was paused, zero when ready.",
	})
	gauges[InstantSendPaused(chain)].Set(1)
}

// amountMetric names the unit used by each chain.
func amountMetric(chain common.Chain, name string) string {
	if chain.Equals(common.DASHChain) {
		return name + "_duffs"
	}
	return name + "_base_units"
}
