package oracle

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type Oracle struct {
	ctx       context.Context
	cancel    context.CancelFunc
	wg        *sync.WaitGroup
	mtx       *sync.Mutex
	logger    zerolog.Logger
	providers map[string]types.Provider
	config    config.Bifrost
	rates     map[string]*big.Float
	metrics   *Metric
}

func NewOracle(config config.Bifrost) (*Oracle, error) {
	logger := log.With().Str("module", "oracle").Logger()

	logLevel, err := zerolog.ParseLevel(config.Oracle.LogLevel)
	if err != nil {
		logger.Error().Err(err).Msg("invalid log level")
	} else {
		logger = logger.Level(logLevel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	o := Oracle{
		ctx:     ctx,
		cancel:  cancel,
		mtx:     &sync.Mutex{},
		wg:      &sync.WaitGroup{},
		logger:  logger,
		config:  config,
		rates:   make(map[string]*big.Float),
		metrics: NewMetric(),
	}

	err = o.LoadProviders()
	if err != nil {
		return nil, err
	}

	// TODO: Add config for metrics
	o.metrics.Register()

	return &o, nil
}

func (o *Oracle) run() {
	defer o.wg.Done()

	go func() {
		ticker := time.NewTicker(time.Second * 2)
		defer ticker.Stop()

		for {
			select {
			case <-o.ctx.Done():
				return
			}
		}
	}()
}

func (o *Oracle) Start() {
	// set global price feed queue
	o.logger.Info().Msg("starting oracle")

	for name, provider := range o.providers {
		o.wg.Add(1)
		o.logger.Info().Str("provider", name).Msg("starting provider")
		provider.Start()
	}

	o.wg.Add(1)
	o.run()
}

func (o *Oracle) Stop() {
	o.logger.Info().Msg("stopping oracle")
	o.cancel()
	o.wg.Wait()
}

func (o *Oracle) GetTickers() []*common.OraclePrice {
	tickerBySymbol := map[string]map[string]types.Ticker{}
	statsByProvider := map[string]map[string]uint64{}

	var err error

	for name, provider := range o.providers {

		statsByProvider[name], err = provider.GetStats()
		if err != nil {
			o.logger.Err(err).Str("provider", name).Msg("Error getting stats")
		}

		tickers, err := provider.GetTickers()
		if err != nil {
			o.logger.Err(err).Str("provider", name).Msg("Error getting prices")
		}

		for symbol, ticker := range tickers {
			_, found := tickerBySymbol[symbol]
			if !found {
				tickerBySymbol[symbol] = map[string]types.Ticker{}
			}
			tickerBySymbol[symbol][name] = ticker

			o.metrics.UpdateProviderTicker(name, ticker)

			//   DEBUG
			// ------------------------

			if name != "binance" || ticker.Pair.Base != "BTC" {
				continue
			}

			if os.Getuid() != 1001 {
				continue
			}

			symbol = ticker.Pair.Join("")

			filename := fmt.Sprintf("/dev/shm/debug-bifrost-%s-%s.txt", name, symbol)
			f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				continue
			}
			defer f.Close()

			line := fmt.Sprintf(
				"enshrined_oracle,source=bifrost,provider=%s,symbol=%s value=%s %d\n",
				name,
				symbol,
				ticker.Price.Text('f', -1),
				time.Now().UnixNano(),
			)

			_, _ = f.WriteString(line)
		}
	}

	converter := NewConverter(tickerBySymbol)
	rates, err := converter.ConvertToUsd()
	if err != nil {
		o.logger.Err(err).Msg("Error converting usd")
		return nil
	}

	var prices []*common.OraclePrice

	for symbol, rate := range rates {
		price, err := common.NewOraclePrice(rate, symbol)
		if err != nil {
			o.logger.Err(err).Msg("Error creating oracle price")
			continue
		}
		prices = append(prices, price)
	}

	o.logger.Info().Int("rates", len(rates)).Msg("getting tickers")

	for provider, stats := range statsByProvider {
		for symbol, count := range stats {
			o.logger.Info().
				Str("provider", provider).
				Str("symbol", symbol).
				Uint64("count", count).
				Msg("stats")
		}
	}

	return prices

}

func (o *Oracle) LoadProviders() error {
	o.logger.Info().Msg("loading providers")
	o.providers = make(map[string]types.Provider)

	for providerName, providerConfig := range o.config.GetProviders() {
		var provider types.Provider
		var err error

		switch providerName {
		case common.ProviderBinance:
			provider, err = providers.NewBinanceProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderBitget:
			provider, err = providers.NewBitgetProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderBitstamp:
			provider, err = providers.NewBitstampProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderBybit:
			provider, err = providers.NewBybitProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderCrypto:
			provider, err = providers.NewCryptoProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderCoinbase:
			provider, err = providers.NewCoinbaseProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderKraken:
			provider, err = providers.NewKrakenProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderOkx:
			provider, err = providers.NewOkxProvider(o.ctx, o.logger, providerConfig)
		default:
			o.logger.Error().Msgf("unknown provider: %s", providerName)
			continue
		}

		if err != nil {
			o.logger.Err(err).Msgf("error loading provider: %s", providerName)
			continue
		}

		o.providers[providerName] = provider
	}

	if len(o.providers) == 0 {
		return fmt.Errorf("no providers loaded")
	}

	return nil
}
