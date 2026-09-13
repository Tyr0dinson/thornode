package endpoints

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the Prometheus vectors shared by every chain's Manager.
//
//	bifrost_endpoint_state{chain,endpoint}                 0 healthy, 1 unhealthy, 2 recovering, 3 disabled
//	bifrost_endpoint_active{chain,endpoint}                1 for the active endpoint
//	bifrost_endpoint_height{chain,endpoint}                latest probed height
//	bifrost_endpoint_probe_latency_seconds{chain,endpoint} latest probe round trip
//	bifrost_endpoint_errors_total{chain,endpoint,kind}     inline + probe errors by kind
//	bifrost_endpoint_failovers_total{chain,from,to,event}  event = failover | failback
//	bifrost_endpoint_recoveries_total{chain,endpoint}      unhealthy/recovering -> healthy
//	bifrost_endpoint_all_down{chain}                       1 when no endpoint is healthy (alert on this)
type Metrics struct {
	state        *prometheus.GaugeVec
	active       *prometheus.GaugeVec
	height       *prometheus.GaugeVec
	probeLatency *prometheus.GaugeVec
	errors       *prometheus.CounterVec
	failovers    *prometheus.CounterVec
	recoveries   *prometheus.CounterVec
	allDown      *prometheus.GaugeVec
}

var (
	defaultMetrics     *Metrics
	defaultMetricsOnce sync.Once
)

// DefaultMetrics returns the process-wide metrics registered on
// prometheus.DefaultRegisterer, registering them on first use. It returns nil if
// registration failed (metrics are then disabled rather than crashing bifrost).
func DefaultMetrics() *Metrics {
	defaultMetricsOnce.Do(func() {
		m, err := NewMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			m = nil
		}
		defaultMetrics = m
	})
	return defaultMetrics
}

// NewMetrics creates and registers the vectors on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	ns, sub := "bifrost", "endpoint"
	labels := []string{"chain", "endpoint"}
	m := &Metrics{
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: sub, Name: "state",
			Help: "Endpoint state: 0 healthy, 1 unhealthy, 2 recovering, 3 disabled.",
		}, labels),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: sub, Name: "active",
			Help: "1 for the endpoint currently serving traffic.",
		}, labels),
		height: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: sub, Name: "height",
			Help: "Latest block height reported by the endpoint's health probe.",
		}, labels),
		probeLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: sub, Name: "probe_latency_seconds",
			Help: "Round trip of the latest successful health probe.",
		}, labels),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: sub, Name: "errors_total",
			Help: "Errors recorded against the endpoint, by kind.",
		}, []string{"chain", "endpoint", "kind"}),
		failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: sub, Name: "failovers_total",
			Help: "Active endpoint changes (event = failover or failback).",
		}, []string{"chain", "from", "to", "event"}),
		recoveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: sub, Name: "recoveries_total",
			Help: "Transitions back to healthy.",
		}, labels),
		allDown: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: sub, Name: "all_down",
			Help: "1 when no endpoint of the chain is healthy. Alert on this.",
		}, []string{"chain"}),
	}
	for _, col := range []prometheus.Collector{m.state, m.active, m.height, m.probeLatency, m.errors, m.failovers, m.recoveries, m.allDown} {
		if err := reg.Register(col); err != nil {
			return nil, fmt.Errorf("register bifrost_endpoint metrics: %w", err)
		}
	}
	return m, nil
}

// StateGauge exposes the state vector (tests and dashboards).
func (m *Metrics) StateGauge() *prometheus.GaugeVec { return m.state }

// ActiveGauge exposes the active vector.
func (m *Metrics) ActiveGauge() *prometheus.GaugeVec { return m.active }

// HeightGauge exposes the height vector.
func (m *Metrics) HeightGauge() *prometheus.GaugeVec { return m.height }

// AllDownGauge exposes the all_down vector.
func (m *Metrics) AllDownGauge() *prometheus.GaugeVec { return m.allDown }

// ErrorsCounter exposes the errors vector.
func (m *Metrics) ErrorsCounter() *prometheus.CounterVec { return m.errors }

// FailoversCounter exposes the failovers vector.
func (m *Metrics) FailoversCounter() *prometheus.CounterVec { return m.failovers }
