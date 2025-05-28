package providers

import (
	"bytes"
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/config"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultInterval = time.Second * 3

type provider struct {
	name              string
	ctx               context.Context
	wg                *sync.WaitGroup
	logger            zerolog.Logger
	tickers           map[string]types.Ticker
	mtx               sync.Mutex
	http              *http.Client
	config            config.BifrostOracleProviderConfiguration
	endpoint          string
	pairs             map[string]types.CurrencyPair
	mapping           map[string]string
	poll              func()
	toProviderSymbol  func(string, string) string
	getAvailablePairs func() ([]string, error)
}

func newProvider(
	ctx context.Context,
	logger zerolog.Logger,
	name string,
	config config.BifrostOracleProviderConfiguration,
) (*provider, error) {
	if config.PollingInterval == 0 {
		config.PollingInterval = defaultInterval
	}

	if len(config.ApiEndpoints) == 0 {
		return nil, fmt.Errorf("no endpoints provided")
	}

	if len(config.Pairs) == 0 {
		return nil, fmt.Errorf("no pairs provided")
	}

	mapping := map[string]string{}
	for _, item := range config.SymbolMapping {
		parts := strings.Split(item, "=")
		if len(parts) != 2 {
			continue
		}
		mapping[strings.ToUpper(parts[0])] = parts[1]
	}

	baseProvider := provider{
		name:              name,
		ctx:               ctx,
		wg:                &sync.WaitGroup{},
		logger:            logger.With().Str("module", "provider").Str("provider", name).Logger(),
		tickers:           make(map[string]types.Ticker),
		mtx:               sync.Mutex{},
		http:              &http.Client{},
		config:            config,
		endpoint:          config.ApiEndpoints[0],
		pairs:             map[string]types.CurrencyPair{},
		mapping:           mapping,
		poll:              nil,
		toProviderSymbol:  nil,
		getAvailablePairs: nil,
	}

	return &baseProvider, nil
}

func (p *provider) init(pp types.Provider) error {
	p.poll = pp.Poll
	p.toProviderSymbol = pp.ToProviderSymbol
	p.getAvailablePairs = pp.GetAvailablePairs

	available := map[string]struct{}{}
	symbols, err := p.getAvailablePairs()
	if err != nil {
		return err
	}

	for _, symbol := range symbols {
		available[symbol] = struct{}{}
	}

	for _, item := range p.config.Pairs {
		pair, err := types.NewCurrencyPair(item)
		if err != nil {
			p.logger.Err(err).Msg("failed to create pair")
			continue
		}

		// look for a mapping of the entire pair
		// example: BTC/USD -> XXBTZUSD on Kraken
		// (viper does lowercase map indexes)
		symbol, found := p.mapping[pair.String()]
		if !found {
			// if no pair mapping, look for single denoms
			base, found := p.mapping[pair.Base]
			if !found {
				base = pair.Base
			}

			quote, found := p.mapping[pair.Quote]
			if !found {
				quote = pair.Quote
			}

			symbol = p.toProviderSymbol(base, quote)
		}

		_, found = available[symbol]
		if !found {
			p.logger.Warn().
				Str("pair", pair.Join("/")).
				Msg("pair not supported")
			continue
		}

		p.pairs[symbol] = pair
	}

	if len(p.pairs) == 0 {
		return fmt.Errorf("no available pairs")
	}

	return nil
}

func (p *provider) run() {
	defer p.wg.Done()

	p.logger.Info().Msg("starting oracle provider")

	go func() {
		ticker := time.NewTicker(p.config.PollingInterval)
		defer ticker.Stop() // Always stop ticker to release resources

		for {
			select {
			case <-ticker.C:
				p.poll()
			case <-p.ctx.Done():
				return
			}
		}
	}()
}

func (p *provider) Start() {
	p.logger.Info().Msg("starting provider")
	if p.poll == nil {
		p.logger.Fatal().Msg("provider poller is nil")
	}

	p.wg.Add(1)
	p.run()
}

func (p *provider) GetTickers() (map[string]types.Ticker, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return p.tickers, nil
}

func (p *provider) setTicker(
	timestamp time.Time,
	pair types.CurrencyPair,
	price, baseVolume, quoteVolume *big.Float,
) {

	err := checkFloat(price)
	if err != nil {
		p.logger.Err(err).Msg("price is invalid")
		return
	}

	err = checkFloat(baseVolume)
	if err != nil {
		p.logger.Err(err).Msg("base volume is invalid")
		return
	}

	p.mtx.Lock()
	defer p.mtx.Unlock()

	p.tickers[pair.String()] = types.Ticker{
		Time:   timestamp,
		Pair:   pair,
		Price:  price,
		Volume: baseVolume,
	}

	if quoteVolume == nil {
		return
	}

	err = checkFloat(quoteVolume)
	if err != nil {
		p.logger.Err(err).Msg("quoteVolume is invalid")
		return
	}

	p.tickers[pair.Swap().String()] = types.Ticker{
		Time:   timestamp,
		Pair:   pair.Swap(),
		Price:  new(big.Float).Quo(big.NewFloat(1), price),
		Volume: quoteVolume,
	}
}

func (p *provider) httpGet(path string) ([]byte, error) {
	return p.httpRequest(path, "GET", nil, nil)
}

func (p *provider) httpPost(path string, body []byte) ([]byte, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	return p.httpRequest(path, "POST", body, headers)
}

func (p *provider) httpRequest(path string, method string, body []byte, headers map[string]string) ([]byte, error) {
	res, err := p.makeHttpRequest(p.endpoint+path, method, body, headers)
	if err != nil && len(p.config.ApiEndpoints) > 1 {
		index := 0

		for i, endpoint := range p.config.ApiEndpoints {
			if p.endpoint == endpoint {
				index = i
				break
			}
		}

		if index == len(p.config.ApiEndpoints)-1 {
			index = 0
		}

		p.endpoint = p.config.ApiEndpoints[index]

		// urls = append(urls, p.endpoints.Urls[index+1:]...)
		// urls = append(urls, p.endpoints.Urls[:index]...)
		//
		// for _, endpoint := range urls {
		// 	p.logger.Warn().
		// 		Str("endpoint", endpoint).
		// 		Msg("trying alternate http endpoints")
		//
		// 	res, err = p.makeHttpRequest(endpoint+path, method, body, headers)
		// 	if err == nil {
		// 		p.logger.Info().Str("endpoint", endpoint).Msg("selected alternate http endpoint")
		// 		p.httpBase = endpoint
		// 		break
		// 	}
		// }
	}
	return res, err
}

func (p *provider) makeHttpRequest(url string, method string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	res, err := p.http.Do(req)
	if err != nil {
		p.logger.Warn().
			Err(err).
			Msg("http request failed")
		return nil, err
	}

	if res.StatusCode != 200 {
		p.logger.Warn().
			Int("code", res.StatusCode).
			Str("body", string(body)).
			Str("url", url).
			Str("method", method).
			Msg("http request returned invalid status")
		if res.StatusCode == 429 || res.StatusCode == 418 {
			p.logger.Warn().
				Str("url", url).
				Str("retry_after", res.Header.Get("Retry-After")).
				Msg("http ratelimited")
		}
		return nil, fmt.Errorf("http request returned invalid status")
	}
	content, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if len(content) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return content, nil
}
