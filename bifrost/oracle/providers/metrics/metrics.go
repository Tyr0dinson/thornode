package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
)

type Metrics struct {
	providerRate   *prometheus.GaugeVec
	providerVolume *prometheus.GaugeVec
	providerUpdate *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	metrics := Metrics{
		providerRate: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "oracle",
				Subsystem: "provider",
				Name:      "rate",
				Help:      "rate for trading pair reported by provider",
			}, []string{"provider", "base", "quote"},
		),
		providerVolume: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "oracle",
				Subsystem: "provider",
				Name:      "volume",
				Help:      "volume for trading pair reported by provider",
			}, []string{"provider", "base", "quote"},
		),
		providerUpdate: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "oracle",
				Subsystem: "provider",
				Name:      "updates",
				Help:      "number of price updates for trading pair",
			}, []string{"provider", "base", "quote"},
		),
	}

	return &metrics
}

func (m *Metrics) Register() {
	prometheus.MustRegister(m.providerRate)
	prometheus.MustRegister(m.providerVolume)
	prometheus.MustRegister(m.providerUpdate)
}

func (m *Metrics) UpdateProviderTicker(provider string, ticker types.Ticker) {
	rate, _ := ticker.Price.Float64()
	volume, _ := ticker.Volume.Float64()

	base, quote := ticker.Pair.Base, ticker.Pair.Quote

	m.providerRate.WithLabelValues(provider, base, quote).Set(rate)
	m.providerVolume.WithLabelValues(provider, base, quote).Set(volume)
	m.providerUpdate.WithLabelValues(provider, base, quote).Add(1)
}
