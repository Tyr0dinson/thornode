package oracle

import (
	"github.com/prometheus/client_golang/prometheus"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
)

type Metric struct {
	providerRate   *prometheus.GaugeVec
	providerVolume *prometheus.GaugeVec
}

func NewMetric() *Metric {
	metrics := Metric{
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
	}

	return &metrics
}

func (m *Metric) Register() {
	prometheus.MustRegister(m.providerRate)
	prometheus.MustRegister(m.providerVolume)
}

func (m *Metric) UpdateProviderTicker(provider string, ticker types.Ticker) {
	rate, _ := ticker.Price.Float64()
	volume, _ := ticker.Volume.Float64()

	base, quote := ticker.Pair.Base, ticker.Pair.Quote

	m.providerRate.WithLabelValues(provider, base, quote).Set(rate)
	m.providerVolume.WithLabelValues(provider, base, quote).Set(volume)
}
