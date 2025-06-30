package providers

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/metrics"
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

type BybitWsTickerEvent struct {
	Data struct {
		Symbol      string `json:"symbol"`
		Price       string `json:"lastPrice"`
		BaseVolume  string `json:"volume24h"`
		QuoteVolume string `json:"turnover24h"`
	} `json:"data"`
}

type BybitProvider struct {
	*websocketProvider
}

func NewBybitProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
	metrics *metrics.Metrics,
) (*BybitProvider, error) {
	wsProvider, err := newWebsocketProvider(ctx, logger, common.ProviderBybit, config, metrics)
	if err != nil {
		return nil, err
	}

	provider := BybitProvider{wsProvider}
	err = provider.init(&provider)
	if err != nil {
		return nil, err
	}

	return &provider, nil
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

	var symbols []string
	for _, item := range response.Result.List {
		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *BybitProvider) ToProviderSymbol(base, quote string) string {
	return base + quote
}

func (p *BybitProvider) Subscribe() error {
	var args []string

	for _, pair := range p.pairs {
		args = append(args, "tickers."+pair.Join(""))
	}

	msg := map[string]any{
		"op":   "subscribe",
		"args": args,
	}

	err := wsjson.Write(p.ctx, p.ws, msg)
	if err != nil {
		p.logger.Err(err).Msg("fail to subscribe")
		return err
	}

	return nil
}

func (p *BybitProvider) HandleWsMessage(msg []byte) error {
	var event BybitWsTickerEvent
	err := json.Unmarshal(msg, &event)
	if err != nil {
		p.logger.Err(err).Msg("error unmarshalling data")
		return err
	}

	pair, found := p.pairs[event.Data.Symbol]
	if !found {
		return nil
	}

	timestamp := time.Now()

	p.setTicker(
		timestamp,
		pair,
		strToFloat(event.Data.Price),
		strToFloat(event.Data.BaseVolume),
		strToFloat(event.Data.QuoteVolume),
	)

	return nil
}
