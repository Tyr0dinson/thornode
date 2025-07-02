package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/providers/metrics"
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

type BinanceWsTicker struct {
	// Time        uint64 `json:"E"`
	Symbol      string `json:"s"`
	BaseVolume  string `json:"v"`
	QuoteVolume string `json:"q"`
	Price       string `json:"c"`
}

type BinanceProvider struct {
	*websocketProvider
}

func NewBinanceProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
	metrics *metrics.Metrics,
) (*BinanceProvider, error) {
	wsProvider, err := newWebsocketProvider(ctx, logger, common.ProviderBinance, config, metrics)
	if err != nil {
		return nil, err
	}

	provider := BinanceProvider{wsProvider}
	err = provider.init(&provider)
	if err != nil {
		return nil, err
	}

	return &provider, nil
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

	var symbols []string
	for _, item := range response {
		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *BinanceProvider) ToProviderSymbol(base, quote string) string {
	return base + quote
}

// Polling

func (p *BinanceProvider) Poll() {
	var symbols []string
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

// Websocket

func (p *BinanceProvider) Subscribe() error {
	var params []string

	for _, pair := range p.pairs {
		params = append(params, strings.ToLower(pair.Join(""))+"@miniTicker")
	}

	msg := map[string]any{
		"method": "SUBSCRIBE",
		"params": params,
		"id":     1,
	}

	err := wsjson.Write(p.ctx, p.ws, msg)
	if err != nil {
		p.logger.Err(err).Msg("fail to subscribe")
		return err
	}

	return nil
}

func (p *BinanceProvider) HandleWsMessage(msg []byte) error {
	var ticker BinanceWsTicker
	err := json.Unmarshal(msg, &ticker)

	if err == nil && ticker.Symbol != "" {
		p.logger.Err(err).Msg("error unmarshalling msg")

		pair, found := p.pairs[ticker.Symbol]
		if !found {
			return nil
		}

		timestamp := time.Now()

		p.setTicker(
			timestamp,
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.BaseVolume),
			strToFloat(ticker.QuoteVolume),
		)

		return nil
	}

	return ErrUnknownWsMsg
}

func (p *BinanceProvider) Pong() {
	msg := map[string]interface{}{}
	err := wsjson.Write(p.ctx, p.ws, msg)
	if err != nil {
		p.logger.Err(err).Msg("error writing pong")
	}
}
