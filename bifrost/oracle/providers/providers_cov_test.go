package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/mock"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	modeProxyCov int32 = iota
	modeGarbageCov
	modeErrorCov
	modeEmptyCov
)

// flipServerCov proxies the mock exchange server, or replaces every REST
// response with garbage, a 500 or an empty body depending on its mode.
type flipServerCov struct {
	backend *httptest.Server
	front   *httptest.Server
	mode    atomic.Int32
	reqs    atomic.Int64
}

func newFlipServerCov(t *testing.T) *flipServerCov {
	t.Helper()
	fs := &flipServerCov{backend: mock.NewServer()}
	target, err := url.Parse(fs.backend.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	fs.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := fs.mode.Load()
		fs.reqs.Add(1)
		switch mode {
		case modeGarbageCov:
			_, _ = w.Write([]byte("garbage"))
		case modeErrorCov:
			w.WriteHeader(http.StatusInternalServerError)
		case modeEmptyCov:
			w.WriteHeader(http.StatusOK)
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(func() {
		fs.front.Close()
		fs.backend.Close()
	})
	return fs
}

func (fs *flipServerCov) config(name string) config.BifrostOracleProviderConfiguration {
	return config.BifrostOracleProviderConfiguration{
		Name:            name,
		PollingInterval: time.Second,
		ApiEndpoints:    []string{fs.front.URL + "/" + name},
		WsEndpoints:     []string{strings.Replace(fs.backend.URL, "http", "ws", 1) + "/" + name + "/ws"},
		Pairs:           []string{"BTC/USDT", "ETH/USDT", "BTC/USD", "ETH/USD"},
	}
}

type covProvider interface {
	Poll()
	GetTickers() (map[string]types.Ticker, error)
}

type covWsProvider interface {
	HandleWsMessage([]byte) error
}

type covCtor func(context.Context, zerolog.Logger, config.BifrostOracleProviderConfiguration, *metrics.Metrics) (covProvider, error)

func wrapCov[T covProvider](f func(context.Context, zerolog.Logger, config.BifrostOracleProviderConfiguration, *metrics.Metrics) (T, error)) covCtor {
	return func(ctx context.Context, l zerolog.Logger, c config.BifrostOracleProviderConfiguration, m *metrics.Metrics) (covProvider, error) {
		p, err := f(ctx, l, c, m)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
}

var providerCtorsCov = map[string]covCtor{
	common.ProviderBinance:   wrapCov(NewBinanceProvider),
	common.ProviderBitfinex:  wrapCov(NewBitfinexProvider),
	common.ProviderBitget:    wrapCov(NewBitgetProvider),
	common.ProviderBitmart:   wrapCov(NewBitmartProvider),
	common.ProviderBitstamp:  wrapCov(NewBitstampProvider),
	common.ProviderBybit:     wrapCov(NewBybitProvider),
	common.ProviderCoinbase:  wrapCov(NewCoinbaseProvider),
	common.ProviderCoinw:     wrapCov(NewCoinwProvider),
	common.ProviderCrypto:    wrapCov(NewCryptoProvider),
	common.ProviderDigifinex: wrapCov(NewDigifinexProvider),
	common.ProviderGate:      wrapCov(NewGateProvider),
	common.ProviderGemini:    wrapCov(NewGeminiProvider),
	common.ProviderHtx:       wrapCov(NewHtxProvider),
	common.ProviderKraken:    wrapCov(NewKrakenProvider),
	common.ProviderKucoin:    wrapCov(NewKucoinProvider),
	common.ProviderLbank:     wrapCov(NewLbankProvider),
	common.ProviderMexc:      wrapCov(NewMexcProvider),
	common.ProviderOkx:       wrapCov(NewOkxProvider),
	common.ProviderThorchain: wrapCov(NewThorchainProvider),
}

// providers whose Poll fetches every pair in its own goroutine
var asyncPollCov = map[string]bool{
	common.ProviderCoinbase: true,
	common.ProviderLbank:    true,
	common.ProviderGemini:   true,
}

// providers whose Poll is intentionally a no-op (websocket data only)
var wsOnlyCov = map[string]bool{
	common.ProviderCoinw:     true,
	common.ProviderDigifinex: true,
}

func TestProvidersFailureModesCov(t *testing.T) {
	fs := newFlipServerCov(t)
	ctx := context.Background()
	logger := zerolog.Nop()
	m := metrics.NewMetrics(false)

	for name, ctor := range providerCtorsCov {
		t.Run(name, func(t *testing.T) {
			cfg := fs.config(name)
			staticPairs := name == common.ProviderThorchain
			if staticPairs {
				// thorchain only serves RUNE/USD and needs no REST call to list it
				cfg.Pairs = []string{"RUNE/USD"}
			}
			if name == common.ProviderGemini {
				// gemini sleeps 1.2s after every second pair to respect rate limits
				cfg.Pairs = []string{"BTC/USD"}
			}

			// constructor validation
			noEndpoints := cfg
			noEndpoints.ApiEndpoints = nil
			noEndpoints.WsEndpoints = nil
			_, err := ctor(ctx, logger, noEndpoints, m)
			require.Error(t, err)

			// available pairs cannot be fetched
			for _, mode := range []int32{modeGarbageCov, modeErrorCov} {
				fs.mode.Store(mode)
				_, err = ctor(ctx, logger, cfg, m)
				if staticPairs {
					require.NoError(t, err)
				} else {
					require.Error(t, err, "mode %d", mode)
				}
			}

			fs.mode.Store(modeProxyCov)
			p, err := ctor(ctx, logger, cfg, m)
			require.NoError(t, err)

			// failed polls leave no tickers behind
			for _, mode := range []int32{modeGarbageCov, modeErrorCov, modeEmptyCov} {
				fs.mode.Store(mode)
				before := fs.reqs.Load()
				p.Poll()
				if asyncPollCov[name] {
					// every per-pair request must have been served in this mode,
					// and none of them may produce a ticker
					lister, ok := p.(interface{ sortedPairs() []types.CurrencyPair })
					require.True(t, ok)
					want := before + int64(len(lister.sortedPairs()))
					require.Eventually(t, func() bool { return fs.reqs.Load() >= want }, 5*time.Second, time.Millisecond)
					require.Never(t, func() bool {
						tickers, _ := p.GetTickers()
						return len(tickers) > 0
					}, 30*time.Millisecond, 5*time.Millisecond, "mode %d", mode)
				}
				tickers, tErr := p.GetTickers()
				require.NoError(t, tErr)
				require.Empty(t, tickers, "mode %d", mode)
			}

			if ws, ok := p.(covWsProvider); ok {
				require.Error(t, ws.HandleWsMessage([]byte("garbage")))
			}

			fs.mode.Store(modeProxyCov)
			p.Poll()
			var tickers map[string]types.Ticker
			if asyncPollCov[name] {
				require.Eventually(t, func() bool {
					tickers, _ = p.GetTickers()
					return len(tickers) > 0
				}, 5*time.Second, 10*time.Millisecond)
			}
			tickers, err = p.GetTickers()
			require.NoError(t, err)
			if wsOnlyCov[name] {
				require.Empty(t, tickers)
			} else {
				require.NotEmpty(t, tickers)
				for _, tk := range tickers {
					require.Positive(t, tk.Price.Sign())
				}
			}
		})
	}
}
