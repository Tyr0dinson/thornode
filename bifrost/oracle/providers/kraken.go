package providers

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"time"
)

type KrakenTickersResponse struct {
	Result map[string]struct {
		Price  [2]string `json:"c"` // ex.: ["0.52900","94.23583387"]
		Volume [2]string `json:"v"` // ex.: ["6512.53593495","9341.68221855"]
	} `json:"result"`
}

type KrakenProvider struct {
	*provider
}

func NewKrakenProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*KrakenProvider, error) {
	p, err := newProvider(ctx, logger, common.ProviderKraken, config)
	if err != nil {
		return nil, err
	}

	pp := KrakenProvider{p}

	err = pp.init(&pp)
	if err != nil {
		return nil, err
	}

	return &pp, nil
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

	symbols := []string{}
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
