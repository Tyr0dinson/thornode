package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"strings"
	"time"
)

type BinanceTicker struct {
	Time        int64  `json:"closeTime"`
	Symbol      string `json:"symbol"`
	BaseVolume  string `json:"volume"`
	QuoteVolume string `json:"quoteVolume"`
	Price       string `json:"lastPrice"`
}

type BinanceProvider struct {
	*provider
}

func NewBinanceProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*BinanceProvider, error) {
	p, err := newProvider(ctx, logger, common.ProviderBinance, config)
	if err != nil {
		return nil, err
	}

	pp := BinanceProvider{p}
	err = pp.init(&pp)
	if err != nil {
		return nil, err
	}

	return &pp, nil
}

func (p *BinanceProvider) Poll() {
	symbols := []string{}
	for symbol := range p.pairs {
		symbols = append(symbols, symbol)
	}
	path := fmt.Sprintf(
		`/api/v3/ticker/24hr?type=MINI&symbols=["%s"]`,
		strings.Join(symbols, ","),
	)

	data, err := p.httpGet(path)
	if err != nil {
		p.logger.Err(err).Msg("error fetching data")
		return
	}

	var response []BinanceTicker

	err = json.Unmarshal(data, &response)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling data")
		return
	}

	for _, ticker := range response {
		pair, found := p.pairs[ticker.Symbol]
		if !found {
			continue
		}

		timestamp := time.Unix(ticker.Time, 0)

		p.setTicker(
			timestamp,
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.BaseVolume),
			strToFloat(ticker.QuoteVolume),
		)
	}
}

func (p *BinanceProvider) GetAvailablePairs() ([]string, error) {
	data, err := p.httpGet("/api/v3/ticker/price")
	if err != nil {
		return nil, err
	}

	var response []struct {
		Symbol string `json:"symbol"`
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	symbols := []string{}
	for _, item := range response {
		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *BinanceProvider) ToProviderSymbol(base, quote string) string {
	return base + quote
}
