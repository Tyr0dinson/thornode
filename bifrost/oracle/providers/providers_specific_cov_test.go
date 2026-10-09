package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// routeServerCov serves fixed bodies per request path (the body can be swapped
// at runtime) and counts requests.
type routeServerCov struct {
	srv    *httptest.Server
	bodies map[string]*atomic.Value
	reqs   atomic.Int64
}

func newRouteServerCov(t *testing.T, routes map[string]string) *routeServerCov {
	t.Helper()
	rs := &routeServerCov{bodies: map[string]*atomic.Value{}}
	for path, body := range routes {
		v := &atomic.Value{}
		v.Store(body)
		rs.bodies[path] = v
	}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := rs.bodies[r.URL.Path]
		var body string
		if ok {
			body, _ = v.Load().(string)
		}
		rs.reqs.Add(1)
		if !ok || body == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *routeServerCov) set(path, body string) { rs.bodies[path].Store(body) }

func providerCfgCov(api string, pairs ...string) config.BifrostOracleProviderConfiguration {
	return config.BifrostOracleProviderConfiguration{
		PollingInterval: time.Second,
		ApiEndpoints:    []string{api},
		WsEndpoints:     []string{"ws://127.0.0.1:1/ws"},
		Pairs:           pairs,
	}
}

func TestBitfinexEdgeCasesCov(t *testing.T) {
	rs := newRouteServerCov(t, map[string]string{
		// a non-string symbol and a funding (non "t") symbol are skipped
		"/v2/tickers": `[[5],["fUSD",1],["tBTCUSDT",1,1,1,1,1,1,100.5,10,1,1],["tETHUSDT",1,1,1,1,1,1,2,3,1,1]]`,
	})
	p, err := NewBitfinexProvider(context.Background(), zerolog.Nop(), providerCfgCov(rs.srv.URL, "BTC/USDT", "ETH/USDT", "DOGE/USDT"), metrics.NewMetrics(false))
	require.NoError(t, err)
	require.Len(t, p.pairs, 2)

	// an empty row in the REST tickers response is skipped, not indexed
	rs.set("/v2/tickers", `[[],["tBTCUSDT",1,1,1,1,1,1,100.5,10,1,1]]`)
	var symbols []string
	require.NotPanics(t, func() { symbols, err = p.GetAvailablePairs() })
	require.NoError(t, err)
	require.Equal(t, []string{"tBTCUSDT"}, symbols)

	// polling skips malformed rows: wrong length, non-string symbol, unknown
	// pair, non-numeric price and volume
	rs.set("/v2/tickers", `[[1,2],[5,1,1,1,1,1,1,1,1,1,1],["tDOGEUSDT",1,1,1,1,1,1,1,1,1,1],["tBTCUSDT",1,1,1,1,1,1,"x",1,1,1],["tETHUSDT",1,1,1,1,1,1,2,"x",1,1]]`)
	p.Poll()
	tickers, err := p.GetTickers()
	require.NoError(t, err)
	require.Empty(t, tickers)

	rs.set("/v2/tickers", `[["tBTCUSDT",1,1,1,1,1,1,100.5,10,1,1]]`)
	p.Poll()
	tickers, _ = p.GetTickers()
	require.Equal(t, "100.5", tickers["BTC/USDT"].Price.Text('f', -1))

	// websocket: non-subscribe events and unknown symbols are ignored, a
	// subscription re-keys the pair by its channel id
	require.NoError(t, p.HandleWsMessage([]byte(`{"event":"info"}`)))
	require.NoError(t, p.HandleWsMessage([]byte(`{"event":"subscribed","symbol":"tXXXUSDT","chanId":1}`)))
	require.NoError(t, p.HandleWsMessage([]byte(`{"event":"subscribed","symbol":"tETHUSDT","chanId":42}`)))
	_, found := p.pairs["42"]
	require.True(t, found)
	_, found = p.pairs["tETHUSDT"]
	require.False(t, found)

	for _, msg := range []string{
		`["x",[1,2,3,4,5,6,7,8,9,10]]`,   // channel id not numeric
		`[42,"hb"]`,                      // heartbeat
		`[42,5]`,                         // not a value list
		`[42,[1,2]]`,                     // wrong number of values
		`[7,[1,2,3,4,5,6,7,8,9,10]]`,     // unknown channel
		`[42,[1,2,3,4,5,6,"x",8,9,10]]`,  // bad price
		`[42,[1,2,3,4,5,6,7,"x",9,10]]`,  // bad volume
		`[42,[1,2,3,4,5,6,2500,3,9,10]]`, // valid
	} {
		require.NoError(t, p.HandleWsMessage([]byte(msg)), msg)
	}
	tickers, _ = p.GetTickers()
	require.Equal(t, "2500", tickers["ETH/USDT"].Price.Text('f', -1))
	require.Equal(t, "3", tickers["ETH/USDT"].Volume.Text('f', -1))

	// Arrays too short to hold [channelId, values] are rejected without
	// panicking: the handler runs in the websocket reader goroutine, where a
	// panic would take down the whole bifrost process.
	for _, msg := range []string{`[]`, `[42]`} {
		require.NotPanics(t, func() { require.NoError(t, p.HandleWsMessage([]byte(msg)), msg) }, msg)
	}
	tickers, _ = p.GetTickers()
	require.Equal(t, "2500", tickers["ETH/USDT"].Price.Text('f', -1), "malformed frames leave tickers untouched")
}

func TestThorchainProviderEdgeCasesCov(t *testing.T) {
	rs := newRouteServerCov(t, map[string]string{
		"/thorchain/network": `{"rune_price_in_tor":"150000000","tor_price_halted":true}`,
	})
	m := metrics.NewMetrics(false)

	// only RUNE/USD is supported
	_, err := NewThorchainProvider(context.Background(), zerolog.Nop(), providerCfgCov(rs.srv.URL, "BTC/USD"), m)
	require.EqualError(t, err, "no available pairs")

	p, err := NewThorchainProvider(context.Background(), zerolog.Nop(), providerCfgCov(rs.srv.URL, "RUNE/USD"), m)
	require.NoError(t, err)

	// halted TOR price produces no ticker
	p.Poll()
	tickers, _ := p.GetTickers()
	require.Empty(t, tickers)

	rs.set("/thorchain/network", `{"rune_price_in_tor":"150000000","tor_price_halted":false}`)
	p.Poll()
	tickers, _ = p.GetTickers()
	require.Equal(t, "1.5", tickers["RUNE/USD"].Price.Text('f', -1))
}

func TestGeminiPollEdgeCasesCov(t *testing.T) {
	rs := newRouteServerCov(t, map[string]string{
		"/v1/symbols":          `["btcusd"]`,
		"/v1/pubticker/btcusd": `500`,
	})
	p, err := NewGeminiProvider(context.Background(), zerolog.Nop(), providerCfgCov(rs.srv.URL, "BTC/USD"), metrics.NewMetrics(false))
	require.NoError(t, err)

	for _, body := range []string{
		`500`,                               // request failure
		`garbage`,                           // undecodable
		`{"last":"1","volume":{"USD":"5"}}`, // no base volume
		`{"last":"1","volume":{"BTC":5}}`,   // base volume is not a string
	} {
		rs.set("/v1/pubticker/btcusd", body)
		before := rs.reqs.Load()
		p.Poll()
		require.Eventually(t, func() bool { return rs.reqs.Load() > before }, 5*time.Second, time.Millisecond)
		require.Never(t, func() bool {
			tickers, _ := p.GetTickers()
			return len(tickers) > 0
		}, 30*time.Millisecond, 5*time.Millisecond, body)
	}

	rs.set("/v1/pubticker/btcusd", `{"last":"2.5","volume":{"BTC":"4"}}`)
	p.Poll()
	require.Eventually(t, func() bool {
		tickers, _ := p.GetTickers()
		return len(tickers) == 1 && tickers["BTC/USD"].Price.Text('f', -1) == "2.5"
	}, 5*time.Second, time.Millisecond)
}

func TestLbankWebsocketPingPongCov(t *testing.T) {
	fs := newFlipServerCov(t)
	p, err := NewLbankProvider(context.Background(), zerolog.Nop(), fs.config(common.ProviderLbank), metrics.NewMetrics(false))
	require.NoError(t, err)

	// tickers for pairs we do not track are ignored
	require.NoError(t, p.HandleWsMessage([]byte(`{"pair":"doge_usdt","tick":{"latest":1,"vol":1}}`)))
	tickers, _ := p.GetTickers()
	require.Empty(t, tickers)

	// a ping is answered with a pong over the live connection
	require.NoError(t, p.connect())
	require.NoError(t, p.HandleWsMessage([]byte(`{"action":"ping","ping":"abc"}`)))

	// once the connection is gone the pong cannot be sent
	require.NoError(t, p.ws.Close(websocket.StatusNormalClosure, ""))
	err = p.HandleWsMessage([]byte(`{"action":"ping","ping":"abc"}`))
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), ErrUnknownWsMsg.Error()))
}
