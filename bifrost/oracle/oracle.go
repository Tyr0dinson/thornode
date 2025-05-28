package oracle

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type Oracle struct {
	ctx       context.Context
	cancel    context.CancelFunc
	wg        *sync.WaitGroup
	logger    zerolog.Logger
	providers map[string]types.Provider
	config    config.Bifrost
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
		ctx:    ctx,
		cancel: cancel,
		wg:     &sync.WaitGroup{},
		logger: logger,
		config: config,
	}

	err = o.LoadProviders()
	if err != nil {
		return nil, err
	}

	return &o, nil
}

func (o *Oracle) run() {
	defer o.wg.Done()

	go func() {
		ticker := time.NewTicker(time.Second * 5)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				o.GetTickers()
			case <-o.ctx.Done():
				return
			}
		}
	}()
}

func (o *Oracle) Start() {
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

func (o *Oracle) GetTickers() {
	o.logger.Info().Msg("getting tickers")

	tickerBySymbol := map[string]map[string]types.Ticker{}

	for name, provider := range o.providers {
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
		}
	}

	converter := NewConverter(tickerBySymbol)
	rates, err := converter.ConvertToUsd()
	if err != nil {
		o.logger.Err(err).Msg("Error converting usd")
		return
	}

	for denom, rate := range rates {
		o.logger.Info().Msgf("%s: %s", denom, rate.String())
	}
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
		case common.ProviderBybit:
			provider, err = providers.NewBybitProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderCrypto:
			provider, err = providers.NewCryptoProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderCoinbase:
			provider, err = providers.NewCoinbaseProvider(o.ctx, o.logger, providerConfig)
		case common.ProviderKraken:
			provider, err = providers.NewKrakenProvider(o.ctx, o.logger, providerConfig)
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
