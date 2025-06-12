package oracle

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"math/big"
	"strings"
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
	queue     chan common.PriceFeed
	address   cosmos.AccAddress
	rates     map[string]*big.Float
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
		mtx:    &sync.Mutex{},
		wg:     &sync.WaitGroup{},
		logger: logger,
		config: config,
		rates:  make(map[string]*big.Float),
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
		ticker := time.NewTicker(time.Second * 2)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				o.getTickers()
			case <-o.ctx.Done():
				return
			}
		}
	}()
}

func (o *Oracle) Start(queue chan common.PriceFeed) {
	// set global price feed queue
	o.queue = queue

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

func (o *Oracle) getTickers() {
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

	o.mtx.Lock()
	for denom, rate := range rates {
		o.rates[denom] = rate
	}
	o.mtx.Unlock()
}

func (o *Oracle) GetTickers() {
	if o.address.Empty() {
		o.logger.Warn().Msg("oracle address is empty")
		return
	}

	o.logger.Info().Int("rates", len(o.rates)).Msg("getting tickers")
	if len(o.rates) == 0 {
		return
	}

	rates := []string{}

	priceFeed := common.PriceFeed{
		Time: time.Now().UnixMilli(),
		// TODO: Might not need the address, can be taken from attestation signer
		Node: o.address,
	}

	o.mtx.Lock()
	for denom, rate := range o.rates {
		rates = append(rates, fmt.Sprintf("%s%s", rate.Text('f', -1), denom))
	}
	o.mtx.Unlock()

	priceFeed.Rates = strings.Join(rates, ",")

	o.queue <- priceFeed
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

func (o *Oracle) SetAccAddress(address cosmos.AccAddress) {
	o.address = address
}
