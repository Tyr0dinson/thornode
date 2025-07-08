package providers

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"math/big"
	"time"
)

type KrakenTickersResponse struct {
	Result map[string]struct {
		Price  [2]string `json:"c"` // ex.: ["0.52900","94.23583387"]
		Volume [2]string `json:"v"` // ex.: ["6512.53593495","9341.68221855"]
	} `json:"result"`
}

type KrakenWsTickerResponse struct {
	Data []struct {
		Symbol string  `json:"symbol"`
		Price  float64 `json:"last"`
		Volume float64 `json:"volume"`
	} `json:"data"`
}

type KrakenProvider struct {
	*websocketProvider
}

func NewKrakenProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
	metrics *metrics.Metrics,
) (*KrakenProvider, error) {
	wsProvider, err := newWebsocketProvider(ctx, logger, common.ProviderKraken, config, metrics)
	if err != nil {
		return nil, err
	}

	provider := KrakenProvider{wsProvider}

	err = provider.init(&provider)
	if err != nil {
		return nil, err
	}

	return &provider, nil
}

func (p *KrakenProvider) Poll() {
	data, err := p.httpGet("/0/public/Ticker")
	if err != nil {
		p.logger.Err(err).Msg("error fetching data")
		return
	}

	var response KrakenTickersResponse

	err = json.Unmarshal(data, &response)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling data")
		return
	}

	now := time.Now()

	for symbol, ticker := range response.Result {
		pair, found := p.pairs[symbol]
		if !found {
			continue
		}

		p.setTicker(
			now,
			pair,
			strToFloat(ticker.Price[0]),
			strToFloat(ticker.Volume[1]),
			nil,
		)
	}
}

func (p *KrakenProvider) GetAvailablePairs() ([]string, error) {
	data, err := p.httpGet("/0/public/AssetPairs")
	if err != nil {
		return nil, err
	}

	var response struct {
		Result map[string]struct {
			Status string `json:"status"`
		} `json:"result"`
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	var symbols []string
	for symbol, item := range response.Result {
		if item.Status != "online" {
			continue
		}
		symbols = append(symbols, symbol)
	}

	return symbols, nil
}

func (p *KrakenProvider) ToProviderSymbol(base, quote string) string {
	return base + quote
}

// Websocket

func (p *KrakenProvider) GetSubscriptionMsgs() ([]map[string]any, error) {
	var symbols []string

	for _, pair := range p.pairs {
		symbols = append(symbols, pair.String())
	}

	msgs := []map[string]any{{
		"method": "subscribe",
		"params": map[string]any{
			"channel": "ticker",
			"symbol":  symbols,
		},
	}}

	return msgs, nil
}

func (p *KrakenProvider) HandleWsMessage(msg []byte) error {
	var response KrakenWsTickerResponse

	err := json.Unmarshal(msg, &response)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling msg")
		return err
	}

	for _, data := range response.Data {
		pair, err := types.NewCurrencyPair(data.Symbol)
		if err != nil {
			p.logger.Err(err).Msg("fail to create pair")
			continue
		}

		symbol := p.toMappedProviderSymbol(pair)

		_, found := p.pairs[symbol]
		if !found {
			return nil
		}

		timestamp := time.Now()

		p.setTicker(
			timestamp,
			pair,
			big.NewFloat(data.Price),
			big.NewFloat(data.Volume),
			nil,
		)
	}

	return nil
}
