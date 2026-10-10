package providers

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/config"
)

// fakeProviderCov is a minimal types.WebsocketProvider used to drive the
// shared base, polling and websocket provider plumbing.
type fakeProviderCov struct {
	available    []string
	availableErr error
	subs         []map[string]any
	subsErr      error
	preErr       error
	handleErr    error
	handled      chan []byte
	polls        atomic.Int32
	pings        atomic.Int32
	prepares     atomic.Int32
}

func (f *fakeProviderCov) Start()                                       {}
func (f *fakeProviderCov) GetTickers() (map[string]types.Ticker, error) { return nil, nil }
func (f *fakeProviderCov) GetAvailablePairs() ([]string, error)         { return f.available, f.availableErr }
func (f *fakeProviderCov) ToProviderSymbol(b, q string) string          { return b + q }
func (f *fakeProviderCov) Poll()                                        { f.polls.Add(1) }
func (f *fakeProviderCov) GetSubscriptionMsgs() ([]map[string]any, error) {
	return f.subs, f.subsErr
}

func (f *fakeProviderCov) PrepareConnection() error {
	f.prepares.Add(1)
	return f.preErr
}

func (f *fakeProviderCov) Ping() error {
	f.pings.Add(1)
	return errors.New("ping failed")
}

func (f *fakeProviderCov) HandleWsMessage(msg []byte) error {
	if f.handled != nil {
		select {
		case f.handled <- msg:
		default:
		}
	}
	return f.handleErr
}

func baseCfgCov(endpoints ...string) config.BifrostOracleProviderConfiguration {
	return config.BifrostOracleProviderConfiguration{
		ApiEndpoints:    endpoints,
		WsEndpoints:     []string{"ws://127.0.0.1:1/ws"},
		Pairs:           []string{"BTC/USDT"},
		PollingInterval: 5 * time.Millisecond,
	}
}

func TestNewBaseProviderCov(t *testing.T) {
	ctx := context.Background()
	m := metrics.NewMetrics(false)

	cfg := baseCfgCov()
	_, err := newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.EqualError(t, err, "no api endpoints provided")

	cfg = baseCfgCov("http://a")
	cfg.Pairs = nil
	_, err = newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.EqualError(t, err, "no pairs provided")

	cfg = baseCfgCov("http://a", "http://b")
	cfg.SymbolMapping = []string{"btc=XBT", "invalid", "a=b=c", "ETH/USD=ETHZUSD"}
	p, err := newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"BTC": "XBT", "ETH/USD": "ETHZUSD"}, p.mapping)
	require.Equal(t, "http://a", p.apiEndpoint)
}

func TestBaseProviderInitCov(t *testing.T) {
	ctx := context.Background()
	m := metrics.NewMetrics(false)

	cfg := baseCfgCov("http://a")
	cfg.Pairs = []string{"BTC/USDT", "ETH/USD", "not-a-pair", "DOGE/USDT"}
	cfg.SymbolMapping = []string{"BTC=XBT", "ETH/USD=ETHZUSD"}
	p, err := newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.NoError(t, err)
	fake := &fakeProviderCov{available: []string{"XBTUSDT", "ETHZUSD"}}
	require.NoError(t, p.init(fake))
	// single-denom mapping, full-pair mapping; invalid/unsupported pairs dropped
	require.Equal(t, map[string]types.CurrencyPair{
		"XBTUSDT": {Base: "BTC", Quote: "USDT"},
		"ETHZUSD": {Base: "ETH", Quote: "USD"},
	}, p.pairs)
	require.Equal(t, []types.CurrencyPair{{Base: "BTC", Quote: "USDT"}, {Base: "ETH", Quote: "USD"}}, p.sortedPairs())

	// nothing supported
	p, err = newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.NoError(t, err)
	require.EqualError(t, p.init(&fakeProviderCov{}), "no available pairs")

	// lookup failure
	p, err = newBaseProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.NoError(t, err)
	require.EqualError(t, p.init(&fakeProviderCov{availableErr: errors.New("down")}), "down")
}

func TestBaseProviderSetTickerCov(t *testing.T) {
	p, err := newBaseProvider(context.Background(), zerolog.Nop(), "x", baseCfgCov("http://a"), metrics.NewMetrics(true))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	btcUsdt := types.CurrencyPair{Base: "BTC", Quote: "USDT"}

	// invalid price / base volume are ignored
	p.setTicker(now, btcUsdt, nil, big.NewFloat(1), nil)
	p.setTicker(now, btcUsdt, big.NewFloat(1), big.NewFloat(0), nil)
	tickers, err := p.GetTickers()
	require.NoError(t, err)
	require.Empty(t, tickers)

	// invalid quote volume keeps only the direct ticker
	p.setTicker(now, btcUsdt, big.NewFloat(4), big.NewFloat(2), big.NewFloat(-1))
	tickers, _ = p.GetTickers()
	require.Len(t, tickers, 1)
	require.Equal(t, "4", tickers["BTC/USDT"].Price.Text('f', -1))

	// a valid quote volume also stores the inverse pair
	p.setTicker(now, btcUsdt, big.NewFloat(4), big.NewFloat(2), big.NewFloat(8))
	tickers, _ = p.GetTickers()
	require.Len(t, tickers, 2)
	inv := tickers["USDT/BTC"]
	require.Equal(t, "0.25", inv.Price.Text('f', -1))
	require.Equal(t, "8", inv.Volume.Text('f', -1))
	require.Equal(t, now, inv.Time)

	// USD quoted pairs are never inverted
	p.setTicker(now, types.CurrencyPair{Base: "ETH", Quote: "USD"}, big.NewFloat(2), big.NewFloat(1), big.NewFloat(2))
	tickers, _ = p.GetTickers()
	require.Len(t, tickers, 3)
	_, found := tickers["USD/ETH"]
	require.False(t, found)

	// returned tickers are copies
	tickers["BTC/USDT"].Price.SetInt64(999)
	again, _ := p.GetTickers()
	require.Equal(t, "4", again["BTC/USDT"].Price.Text('f', -1))
}

func TestBaseProviderHttpCov(t *testing.T) {
	var lastMethod, lastContentType string
	var mu sync.Mutex
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastMethod, lastContentType = r.Method, r.Header.Get("Content-Type")
		mu.Unlock()
		switch r.URL.Path {
		case "/empty":
			w.WriteHeader(http.StatusOK)
		case "/limited":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer bad.Close()

	p, err := newBaseProvider(context.Background(), zerolog.Nop(), "x", baseCfgCov(bad.URL, good.URL), metrics.NewMetrics(false))
	require.NoError(t, err)

	// first endpoint fails and the provider rotates to the next one
	_, err = p.httpGet("/x")
	require.EqualError(t, err, "http request returned invalid status")
	require.Equal(t, good.URL, p.apiEndpoint)

	body, err := p.httpPost("/x", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
	mu.Lock()
	require.Equal(t, http.MethodPost, lastMethod)
	require.Equal(t, "application/json", lastContentType)
	mu.Unlock()

	_, err = p.httpGet("/empty")
	require.EqualError(t, err, "empty response")
	// wraps around to the first endpoint after the last one fails
	require.Equal(t, bad.URL, p.apiEndpoint)

	p.apiEndpoint = good.URL
	_, err = p.httpGet("/limited")
	require.EqualError(t, err, "http request returned invalid status")

	// transport and request construction failures
	_, err = p.makeHttpRequest("http://127.0.0.1:1/unreachable", http.MethodGet, nil, nil)
	require.Error(t, err)
	_, err = p.makeHttpRequest("http://a", "BAD METHOD", nil, nil)
	require.Error(t, err)
}

func TestPollingProviderCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := metrics.NewMetrics(false)

	cfg := baseCfgCov("http://a")
	cfg.PollingInterval = 0
	_, err := newPollingProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.EqualError(t, err, "polling interval cannot be zero")

	cfg = baseCfgCov()
	_, err = newPollingProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.EqualError(t, err, "no api endpoints provided")

	p, err := newPollingProvider(ctx, zerolog.Nop(), "x", baseCfgCov("http://a"), m)
	require.NoError(t, err)
	require.Error(t, p.init(&fakeProviderCov{}))

	fake := &fakeProviderCov{available: []string{"BTCUSDT"}}
	require.NoError(t, p.init(fake))
	p.Start()
	require.Eventually(t, func() bool { return fake.polls.Load() >= 2 }, 5*time.Second, time.Millisecond)
	cancel()
}

// wsServerCov is a websocket exchange double: every connection records the
// subscription messages and then plays the scripted frames for that
// connection index, finally holding (or closing) the connection.
type wsServerCov struct {
	srv         *httptest.Server
	conns       atomic.Int32
	subsMu      sync.Mutex
	subs        []string
	scripts     [][][]byte
	closeAfter  []bool
	stream      bool // keep sending "tick" until the client goes away
	numSubsWant int
}

func newWsServerCov(t *testing.T, numSubs int, stream bool, scripts [][][]byte, closeAfter []bool) *wsServerCov {
	t.Helper()
	ws := &wsServerCov{scripts: scripts, closeAfter: closeAfter, stream: stream, numSubsWant: numSubs}
	ws.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(ws.conns.Add(1)) - 1
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		for i := 0; i < ws.numSubsWant; i++ {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			ws.subsMu.Lock()
			ws.subs = append(ws.subs, string(msg))
			ws.subsMu.Unlock()
		}
		if idx < len(ws.scripts) {
			for _, frame := range ws.scripts[idx] {
				if err := c.Write(ctx, websocket.MessageText, frame); err != nil {
					return
				}
			}
			if idx < len(ws.closeAfter) && ws.closeAfter[idx] {
				_ = c.Close(websocket.StatusGoingAway, "bye")
				return
			}
		}
		for ws.stream {
			if err := c.Write(ctx, websocket.MessageText, []byte("tick")); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
		// keep reading so close handshakes from the client complete promptly
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ws.srv.Close)
	return ws
}

func (ws *wsServerCov) url() string {
	return "ws" + ws.srv.URL[len("http"):]
}

func (ws *wsServerCov) subscriptions() []string {
	ws.subsMu.Lock()
	defer ws.subsMu.Unlock()
	return append([]string(nil), ws.subs...)
}

func newWsProviderCov(t *testing.T, ctx context.Context, endpoint string, fake *fakeProviderCov) *websocketProvider {
	t.Helper()
	cfg := baseCfgCov("http://a")
	cfg.WsEndpoints = []string{endpoint}
	p, err := newWebsocketProvider(ctx, zerolog.Nop(), "x", cfg, metrics.NewMetrics(false))
	require.NoError(t, err)
	require.NoError(t, p.init(fake))
	return p
}

func TestWebsocketProviderConstructionCov(t *testing.T) {
	ctx := context.Background()
	m := metrics.NewMetrics(false)

	cfg := baseCfgCov("http://a")
	cfg.WsEndpoints = nil
	_, err := newWebsocketProvider(ctx, zerolog.Nop(), "x", cfg, m)
	require.EqualError(t, err, "no websocket endpoints provided")

	_, err = newWebsocketProvider(ctx, zerolog.Nop(), "x", baseCfgCov(), m)
	require.EqualError(t, err, "no api endpoints provided")

	p, err := newWebsocketProvider(ctx, zerolog.Nop(), "x", baseCfgCov("http://a"), m)
	require.NoError(t, err)
	require.Error(t, p.init(&fakeProviderCov{}))
	require.Equal(t, minBackoff, p.backoff)
	// default hooks are no-ops
	require.NoError(t, p.PrepareConnection())
	require.NoError(t, p.Ping())
}

func TestWebsocketProviderConnectCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newWsServerCov(t, 2, false, nil, nil)

	fake := &fakeProviderCov{
		available: []string{"BTCUSDT"},
		subs:      []map[string]any{{"op": "sub", "id": 1}, {"op": "sub", "id": 2}},
	}
	p := newWsProviderCov(t, ctx, srv.url(), fake)

	// preconnect failure
	fake.preErr = errors.New("prep failed")
	require.EqualError(t, p.connect(), "prep failed")
	fake.preErr = nil

	// subscription message failure
	fake.subsErr = errors.New("no subs")
	require.EqualError(t, p.connect(), "no subs")
	fake.subsErr = nil

	require.NoError(t, p.connect())
	// reconnecting closes the previous connection first
	require.NoError(t, p.connect())
	require.Eventually(t, func() bool { return len(srv.subscriptions()) == 4 }, 2*time.Second, time.Millisecond, "%q", srv.subscriptions())
	require.Equal(t, "{\"id\":1,\"op\":\"sub\"}\n", srv.subscriptions()[0])
	require.Equal(t, "{\"id\":2,\"op\":\"sub\"}\n", srv.subscriptions()[1])

	// reconnect waits for the backoff and doubles it
	p.backoff = time.Millisecond
	require.NoError(t, p.reconnect())
	require.Equal(t, 2*time.Millisecond, p.backoff)

	// dial failure
	p.wsEndpoint = "ws://127.0.0.1:1/ws"
	require.Error(t, p.connect())

	// a canceled context aborts reconnecting
	cancel()
	p.backoff = time.Hour
	require.EqualError(t, p.reconnect(), "context canceled")
	require.Nil(t, p.ws)
}

func TestWebsocketProviderStartLoopCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// first connection: an empty frame, a frame the handler rejects, then a
	// close which forces a reconnect; second connection: a good frame.
	srv := newWsServerCov(t, 1, false,
		[][][]byte{{{}, []byte("bad")}, {[]byte("good")}},
		[]bool{true, false},
	)
	fake := &fakeProviderCov{
		available: []string{"BTCUSDT"},
		subs:      []map[string]any{{"op": "sub"}},
		handleErr: errors.New("cannot handle"),
		handled:   make(chan []byte, 10),
	}
	p := newWsProviderCov(t, ctx, srv.url(), fake)
	// keepalive is disabled here, so the read loop never resets the backoff
	// to minBackoff: the reconnect after the server closes waits this 1ms
	p.backoff = time.Millisecond

	p.Start()
	require.Equal(t, int32(1), fake.polls.Load())

	var got []string
	require.Eventually(t, func() bool {
		select {
		case msg := <-fake.handled:
			got = append(got, string(msg))
		default:
		}
		return len(got) == 2
	}, 5*time.Second, time.Millisecond)
	// the empty frame never reaches the handler
	require.Equal(t, []string{"bad", "good"}, got)
	require.Equal(t, int32(2), srv.conns.Load())
	require.Equal(t, int32(2), fake.prepares.Load())
	cancel()
}

func TestWebsocketProviderKeepalivePingCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newWsServerCov(t, 0, true, nil, nil)
	fake := &fakeProviderCov{available: []string{"BTCUSDT"}}
	p := newWsProviderCov(t, ctx, srv.url(), fake)
	p.keepaliveInterval = time.Millisecond
	p.keepaliveTimeout = time.Hour

	p.Start()
	require.Eventually(t, func() bool { return fake.pings.Load() >= 2 }, 5*time.Second, time.Millisecond)
	cancel()
}

func TestWebsocketProviderStartConnectFailureCov(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeProviderCov{available: []string{"BTCUSDT"}, preErr: errors.New("prep failed")}
	p := newWsProviderCov(t, ctx, "ws://127.0.0.1:1/ws", fake)
	p.Start()
	// the initial poll still happens, but no read loop is started
	require.Equal(t, int32(1), fake.polls.Load())
	require.Equal(t, int32(1), fake.prepares.Load())
	require.Nil(t, p.ws)
}
