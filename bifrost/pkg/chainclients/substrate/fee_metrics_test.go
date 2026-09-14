package substrate

import (
	"sync"
	"testing"
	"testing/synctest"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// Register the shared collectors once, including when tests run with -count.
var newFeeTestMetrics = sync.OnceValues(func() (*metrics.Metrics, error) {
	return metrics.NewMetrics(config.BifrostMetricsConfiguration{
		Chains: []common.Chain{common.TAOChain, common.DOTChain},
	})
})

func TestNetworkFeeMetrics(t *testing.T) {
	m, err := newFeeTestMetrics()
	if err != nil {
		t.Fatal(err)
	}
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		t.Run(chain.String(), func(t *testing.T) {
			for _, tt := range []struct {
				name      string
				current   uint64
				bootstrap bool
				blocked   bool
			}{
				{name: "changed estimate"},
				{name: "unchanged estimate", current: 101_000},
				{name: "default estimate", bootstrap: true},
				{name: "queue timeout", current: 42, blocked: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						sc, queue := newTestScannerWithFeeQueue(1000)
						sc.cfg.ChainID = chain
						sc.params = ChainParams[chain]
						sc.metrics = m
						sc.currentFee.Store(tt.current)
						wantFee := uint64(101_000)
						if tt.bootstrap {
							wantFee = sc.params.DefaultFee
						} else {
							for range FeeCacheSize {
								sc.updateFeeCache(100_500)
							}
						}
						if tt.blocked {
							queue = make(chan common.NetworkFee)
							sc.globalNetworkFeeQueue = queue
						}
						gauge := m.GetGauge(metrics.GasPrice(chain))
						counter := m.GetCounter(metrics.GasPriceChange(chain))
						if gauge == nil || counter == nil {
							t.Fatal("missing fee collectors")
						}
						gauge.Set(0)
						before := testutil.ToFloat64(counter)
						sc.updateNetworkFee(FeeUpdatePeriod)
						if got := testutil.ToFloat64(gauge); got != float64(wantFee) {
							t.Fatalf("fee gauge = %v, want %d", got, wantFee)
						}
						wantChanges := 0
						wantCurrent := tt.current
						if !tt.blocked && tt.current != wantFee {
							wantChanges = 1
							wantCurrent = wantFee
						}
						if got := testutil.ToFloat64(counter) - before; got != float64(wantChanges) {
							t.Errorf("fee changes = %v, want %d", got, wantChanges)
						}
						if got := sc.currentFee.Load(); got != wantCurrent {
							t.Errorf("current fee = %d, want %d", got, wantCurrent)
						}
						if len(queue) != wantChanges {
							t.Fatalf("queued updates = %d, want %d", len(queue), wantChanges)
						}
						if wantChanges == 1 {
							fee := <-queue
							if fee.Chain != chain || fee.TransactionRate != wantFee || fee.TransactionSize != 1 {
								t.Errorf("unexpected queued fee: %+v", fee)
							}
						}
					})
				})
			}
		})
	}
}

func TestNetworkFeeWithoutMetrics(t *testing.T) {
	m, err := newFeeTestMetrics()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		metrics *metrics.Metrics
		chain   common.Chain
	}{
		{"nil metrics", nil, common.TAOChain},
		{"missing collectors", m, common.Chain("FEE_TEST")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sc, queue := newTestScannerWithFeeQueue(1)
			sc.cfg.ChainID = tt.chain
			sc.metrics = tt.metrics
			if tt.metrics != nil && (tt.metrics.GetGauge(metrics.GasPrice(tt.chain)) != nil ||
				tt.metrics.GetCounter(metrics.GasPriceChange(tt.chain)) != nil) {
				t.Fatal("expected missing collectors")
			}
			sc.updateNetworkFee(FeeUpdatePeriod)
			if len(queue) != 1 {
				t.Fatalf("queued updates = %d, want 1", len(queue))
			}
			if got := (<-queue).TransactionRate; got != sc.params.DefaultFee {
				t.Errorf("queued fee = %d, want %d", got, sc.params.DefaultFee)
			}
			if got := sc.currentFee.Load(); got != sc.params.DefaultFee {
				t.Errorf("current fee = %d, want %d", got, sc.params.DefaultFee)
			}
		})
	}
}
