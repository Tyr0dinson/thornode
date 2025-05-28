package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"time"
)

type CoinbaseTicker struct {
	Price  string `json:"price"`
	Volume string `json:"volume"`
	Time   string `json:"time"`
}

type CoinbaseProvider struct {
	*provider
}

func NewCoinbaseProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*CoinbaseProvider, error) {
	p, err := newProvider(ctx, logger, common.ProviderCoinbase, config)
	if err != nil {
		return nil, err
	}

	pp := CoinbaseProvider{p}
	err = pp.init(&pp)
	if err != nil {
		return nil, err
	}

	return &pp, nil
}

func (p *CoinbaseProvider) Poll() {
	i := 0
	for symbol, pair := range p.pairs {
		go func(p *CoinbaseProvider, symbol string, pair types.CurrencyPair) {
			path := fmt.Sprintf("/products/%s/ticker", symbol)
			data, err := p.httpGet(path)
			if err != nil {
				return
			}

			var ticker CoinbaseTicker
			err = json.Unmarshal(data, &ticker)
			if err != nil {
				return
			}

			now := time.Now()

			p.setTicker(
				now,
				pair,
				strToFloat(ticker.Price),
				strToFloat(ticker.Volume),
				nil,
			)
		}(p, symbol, pair)
		// Coinbase has a rate limit of 10req/s, sleeping 1.2s before running
		// the next batch of requests
		i = i + 1
		if i == 10 {
			i = 0
			time.Sleep(time.Millisecond * 1200)
		}
	}
}

func (p *CoinbaseProvider) GetAvailablePairs() ([]string, error) {
	data, err := p.httpGet("/products")
	if err != nil {
		return nil, err
	}

	var response []struct {
		Id     string `json:"id"`
		Status string `json:"status"`
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	symbols := []string{}
	for _, product := range response {
		if product.Status != "online" {
			continue
		}
		symbols = append(symbols, product.Id)
	}

	return symbols, nil
}

func (p *CoinbaseProvider) ToProviderSymbol(base, quote string) string {
	return base + "-" + quote
}
