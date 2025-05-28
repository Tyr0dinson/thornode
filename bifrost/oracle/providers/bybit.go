package providers

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"time"
)

type BybitTickersResponse struct {
	Result struct {
		List []struct {
			Symbol      string `json:"symbol"`
			Price       string `json:"lastPrice"`
			BaseVolume  string `json:"volume24h"`
			QuoteVolume string `json:"turnover24h"`
		} `json:"list"`
	} `json:"result"`
}

type BybitProvider struct {
	*provider
}

func NewBybitProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*BybitProvider, error) {
	p, err := newProvider(ctx, logger, common.ProviderBybit, config)
	if err != nil {
		return nil, err
	}

	pp := BybitProvider{p}
	err = pp.init(&pp)
	if err != nil {
		return nil, err
	}

	return &pp, nil
}

func (p *BybitProvider) Poll() {
	data, err := p.httpGet("/v5/market/tickers?category=spot")
	if err != nil {
		p.logger.Err(err).Msg("error fetching data")
		return
	}

	var response BybitTickersResponse

	err = json.Unmarshal(data, &response)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling data")
		return
	}

	now := time.Now()

	for _, ticker := range response.Result.List {
		pair, found := p.pairs[ticker.Symbol]
		if !found {
			continue
		}

		p.setTicker(
			now,
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.BaseVolume),
			strToFloat(ticker.QuoteVolume),
		)
	}
}

func (p *BybitProvider) GetAvailablePairs() ([]string, error) {
	data, err := p.httpGet("/v5/market/instruments-info?category=spot")
	if err != nil {
		return nil, err
	}

	var response struct {
		Result struct {
			List []struct {
				Symbol string `json:"symbol"`
			} `json:"list"`
		} `json:"result"`
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	symbols := []string{}
	for _, item := range response.Result.List {
		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *BybitProvider) ToProviderSymbol(base, quote string) string {
	return base + quote
}
