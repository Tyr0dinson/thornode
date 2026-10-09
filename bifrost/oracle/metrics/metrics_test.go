package metrics

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
)

func tickerCov(price, volume float64) types.Ticker {
	return types.Ticker{
		Time:   time.Unix(0, 0),
		Pair:   types.CurrencyPair{Base: "BTC", Quote: "USDT"},
		Price:  big.NewFloat(price),
		Volume: big.NewFloat(volume),
	}
}

func TestMetricsDetailedCov(t *testing.T) {
	m := NewMetrics(true)

	m.UpdateProviderTicker("binance", tickerCov(100, 5))
	m.UpdateProviderTicker("binance", tickerCov(101, 6))
	if v := testutil.ToFloat64(m.providerRate.WithLabelValues("binance", "BTC", "USDT")); v != 101 {
		t.Fatalf("rate %v", v)
	}
	if v := testutil.ToFloat64(m.providerVolume.WithLabelValues("binance", "BTC", "USDT")); v != 6 {
		t.Fatalf("volume %v", v)
	}
	if v := testutil.ToFloat64(m.providerUpdate.WithLabelValues("binance", "BTC", "USDT")); v != 2 {
		t.Fatalf("updates %v", v)
	}

	m.UpdateProviderPrice("binance", "BTC", big.NewFloat(99.5))
	if v := testutil.ToFloat64(m.providerPrice.WithLabelValues("binance", "BTC")); v != 99.5 {
		t.Fatalf("price %v", v)
	}

	m.UpdateConverterCappedVolume("binance", "BTC", big.NewFloat(7))
	m.UpdateConverterTotalVolume("binance", "BTC", big.NewFloat(8))
	if v := testutil.ToFloat64(m.converterVolume.WithLabelValues("binance", "BTC", "capped")); v != 7 {
		t.Fatalf("capped %v", v)
	}
	if v := testutil.ToFloat64(m.converterVolume.WithLabelValues("binance", "BTC", "total")); v != 8 {
		t.Fatalf("total %v", v)
	}

	m.UpdateProviderBounds("BTC", big.NewFloat(110), big.NewFloat(90))
	if v := testutil.ToFloat64(m.providerBound.WithLabelValues("BTC", "upper")); v != 110 {
		t.Fatalf("upper %v", v)
	}
	if v := testutil.ToFloat64(m.providerBound.WithLabelValues("BTC", "lower")); v != 90 {
		t.Fatalf("lower %v", v)
	}
}

func TestMetricsNotDetailedCov(t *testing.T) {
	m := NewMetrics(false)

	m.UpdateProviderTicker("binance", tickerCov(100, 5))
	m.UpdateProviderPrice("binance", "BTC", big.NewFloat(99.5))
	m.UpdateConverterCappedVolume("binance", "BTC", big.NewFloat(7))
	m.UpdateConverterTotalVolume("binance", "BTC", big.NewFloat(8))
	m.UpdateProviderBounds("BTC", big.NewFloat(110), big.NewFloat(90))

	// nothing but the "final" price should be tracked when not detailed
	for name, c := range map[string]prometheus.Collector{
		"rate":      m.providerRate,
		"volume":    m.providerVolume,
		"update":    m.providerUpdate,
		"price":     m.providerPrice,
		"bound":     m.providerBound,
		"converter": m.converterVolume,
	} {
		if n := testutil.CollectAndCount(c); n != 0 {
			t.Fatalf("%s: expected no series, got %d", name, n)
		}
	}

	m.UpdateProviderPrice("final", "BTC", big.NewFloat(42))
	if v := testutil.ToFloat64(m.providerPrice.WithLabelValues("final", "BTC")); v != 42 {
		t.Fatalf("final price %v", v)
	}
	if n := testutil.CollectAndCount(m.providerPrice); n != 1 {
		t.Fatalf("expected 1 price series, got %d", n)
	}
}

func TestMetricsRegisterCov(t *testing.T) {
	m := NewMetrics(true)
	m.Register()
	// a second registration of identical collectors must be rejected by the
	// default registry (errors are swallowed by Register)
	m.Register()
	err := prometheus.Register(m.providerRate)
	var are prometheus.AlreadyRegisteredError
	if !errors.As(err, &are) {
		t.Fatalf("expected AlreadyRegisteredError, got %v", err)
	}
	for _, c := range []prometheus.Collector{
		m.providerRate, m.providerVolume, m.providerUpdate,
		m.providerPrice, m.providerBound, m.converterVolume,
	} {
		if !prometheus.Unregister(c) {
			t.Fatalf("collector was not registered")
		}
	}
}
