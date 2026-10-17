package providers

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func htxTicker(symbol string, price float64) []byte {
	return []byte(fmt.Sprintf(`{"ch":"market.%s.ticker","ts":1760600000000,"tick":{"lastPrice":%v,"amount":%v}}`, symbol, price, price/1000))
}

// htxTestMessages covers valid pushes and every gzip failure mode: bad header,
// truncation, corrupt deflate data, checksum mismatch, the 10 KB read limit,
// multistream members and optional header fields.
func htxTestMessages(t *testing.T) [][]byte {
	valid := gzipBytes(t, htxTicker("btcusdt", 101))

	corrupt := append([]byte(nil), gzipBytes(t, htxTicker("btcusdt", 102))...)
	corrupt[len(corrupt)/2] ^= 0xff

	badChecksum := append([]byte(nil), gzipBytes(t, htxTicker("btcusdt", 103))...)
	badChecksum[len(badChecksum)-6] ^= 0x01

	var named bytes.Buffer
	w := gzip.NewWriter(&named)
	w.Name, w.Comment, w.Extra = "ticker.json", "htx", []byte{1, 2, 3}
	_, _ = w.Write(htxTicker("btcusdt", 104))
	_ = w.Close()

	oversized := gzipBytes(t, append(htxTicker("btcusdt", 105)[:20], []byte(strings.Repeat(" ", 20000))...))

	msgs := [][]byte{
		valid,
		[]byte("not gzip at all"),
		gzipBytes(t, htxTicker("btcusdt", 106)),
		valid[:len(valid)/2],
		gzipBytes(t, htxTicker("btcusdt", 107)),
		corrupt,
		badChecksum,
		named.Bytes(),
		gzipBytes(t, htxTicker("ethusdt", 108)), // pair not configured
		gzipBytes(t, []byte(`{"ch":"market.btcusdt","tick":{"lastPrice":109}}`)),
		oversized,
		append(gzipBytes(t, htxTicker("btcusdt", 110)), gzipBytes(t, htxTicker("btcusdt", 111))...),
		gzipBytes(t, nil),
		{0x1f, 0x8b},
		{0x1f},
		gzipBytes(t, htxTicker("btcusdt", 112)),
	}
	rng := rand.New(rand.NewSource(5)) //nolint:gosec // fixed seed: a reproducible differential test
	for i := 0; i < 300; i++ {
		m := gzipBytes(t, htxTicker("btcusdt", float64(200+i)))
		for n := rng.Intn(3); n > 0; n-- {
			m[rng.Intn(len(m))] ^= byte(1 << rng.Intn(8))
		}
		if rng.Intn(4) == 0 {
			m = m[:rng.Intn(len(m))]
		}
		msgs = append(msgs, m)
		if rng.Intn(3) == 0 {
			msgs = append(msgs, gzipBytes(t, htxTicker("btcusdt", float64(1000+i))))
		}
	}
	return msgs
}

func btcTickerText(p *HtxProvider) string {
	tk, ok := p.tickers["BTC/USDT"]
	if !ok {
		return "none"
	}
	return tk.Price.Text('g', 20) + "/" + tk.Volume.Text('g', 20)
}

// TestHtxReusedGzipReaderMatchesFresh checks that one provider reusing its gzip
// reader across messages behaves exactly like a fresh provider (whose first
// message runs on a brand-new reader, i.e. the original gzip.NewReader path).
func TestHtxReusedGzipReaderMatchesFresh(t *testing.T) {
	for _, firstBad := range []bool{false, true} {
		shared := newBenchHtxProvider()
		msgs := htxTestMessages(t)
		if firstBad {
			msgs = append([][]byte{[]byte("not gzip"), {0x1f, 0x8b, 8}}, msgs...)
		}
		for i, msg := range msgs {
			fresh := newBenchHtxProvider()
			wantErr := fresh.HandleWsMessage(msg)
			before := btcTickerText(shared)
			gotErr := shared.HandleWsMessage(msg)
			if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("msg %d: err %v, want %v", i, gotErr, wantErr)
			}
			want := btcTickerText(fresh)
			got := btcTickerText(shared)
			if want == "none" {
				if got != before {
					t.Fatalf("msg %d: shared ticker changed to %s, fresh set none", i, got)
				}
			} else if got != want {
				t.Fatalf("msg %d: shared ticker %s, want %s", i, got, want)
			}
		}
	}
}
