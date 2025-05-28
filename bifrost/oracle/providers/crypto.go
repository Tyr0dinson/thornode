package providers

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"strings"
	"time"
)

type CryptoTickersResponse struct {
	Result struct {
		Data []struct {
			Time   int64  `json:"t"`
			Symbol string `json:"i"`
			Price  string `json:"a"`
			Volume string `json:"v"`
		} `json:"data"`
	} `json:"result"`
}

type CryptoProvider struct {
	*provider
}

func NewCryptoProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*CryptoProvider, error) {
	p, err := newProvider(ctx, logger, common.ProviderCrypto, config)
	if err != nil {
		return nil, err
	}

	pp := CryptoProvider{p}
	err = pp.init(&pp)
	if err != nil {
		return nil, err
	}

	return &pp, nil
}

func (p *CryptoProvider) Poll() {
	data, err := p.httpGet("/exchange/v1/public/get-tickers")
	if err != nil {
		p.logger.Err(err).Msg("error fetching data")
		return
	}

	var response CryptoTickersResponse

	err = json.Unmarshal(data, &response)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling data")
		return
	}

	for _, ticker := range response.Result.Data {
		pair, found := p.pairs[ticker.Symbol]
		if !found {
			continue
		}

		p.setTicker(
			time.UnixMilli(ticker.Time),
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.Volume),
			nil,
		)
	}
}

func (p *CryptoProvider) GetAvailablePairs() ([]string, error) {
	data, err := p.httpGet("/exchange/v1/public/get-tickers")
	if err != nil {
		return nil, err
	}

	var response CryptoTickersResponse

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	symbols := []string{}
	for _, item := range response.Result.Data {
		if strings.HasSuffix(item.Symbol, "-PERP") {
			continue
		}

		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *CryptoProvider) ToProviderSymbol(base, quote string) string {
	return base + "_" + quote
}
