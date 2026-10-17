package providers

import (
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
)

// newBenchHtxProvider builds an HtxProvider without a websocket connection;
// HandleWsMessage only needs pairs, tickers and metrics for ticker messages.
func newBenchHtxProvider() *HtxProvider {
	return &HtxProvider{websocketProvider: &websocketProvider{baseProvider: &baseProvider{
		name:    "htx",
		ctx:     context.Background(),
		logger:  zerolog.Nop(),
		tickers: map[string]types.Ticker{},
		pairs:   map[string]types.CurrencyPair{"btcusdt": newPair("BTC/USDT")},
		metrics: &metrics.Metrics{},
	}}}
}

func gzipBytes(tb testing.TB, data []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// htxTickerMessage is a market.<symbol>.ticker push as HTX sends it.
const htxTickerMessage = `{"ch":"market.btcusdt.ticker","ts":1760600000000,"tick":{"open":108000.01,"high":110000.0,"low":107500.5,"close":109425.0,"amount":1234.5678,"vol":135000000.12,"count":98765,"bid":109424.99,"bidSize":0.5,"ask":109425.01,"askSize":0.7,"lastPrice":109425.0,"lastSize":0.01}}`

func BenchmarkHtxHandleWsMessage(b *testing.B) {
	p := newBenchHtxProvider()
	msg := gzipBytes(b, []byte(htxTickerMessage))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.HandleWsMessage(msg); err != nil {
			b.Fatal(err)
		}
	}
}
