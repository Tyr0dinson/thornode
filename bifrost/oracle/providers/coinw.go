package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"time"
)

type CoinwTicker struct {
	Id     int64  `json:"id"`
	Symbol string `json:"symbol"`
	Price  string `json:"last"`
	Volume string `json:"baseVolume"`
}

type CoinwWsSubscriptionMsg struct {
	Channel string `json:"channel"`
}

type CoinwWsTickerMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type CoinwWsTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"last"`
	Volume string `json:"vol"`
}

type CoinwProvider struct {
	*websocketProvider
}

func NewCoinwProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
	metrics *metrics.Metrics,
) (*CoinwProvider, error) {
	wsProvider, err := newWebsocketProvider(ctx, logger, common.ProviderCoinw, config, metrics)
	if err != nil {
		return nil, err
	}

	provider := CoinwProvider{wsProvider}
	err = provider.init(&provider)
	if err != nil {
		return nil, err
	}

	return &provider, nil
}

func (p *CoinwProvider) getTickers() ([]CoinwTicker, error) {
	data, err := p.httpGet("/api/v1/public?command=returnTicker")
	if err != nil {
		return nil, err
	}

	var response struct {
		Data map[string]CoinwTicker `json:"data"`
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	var tickers []CoinwTicker

	for symbol, ticker := range response.Data {
		tickers = append(tickers, CoinwTicker{
			Id:     ticker.Id,
			Symbol: symbol,
			Price:  ticker.Price,
			Volume: ticker.Volume,
		})
	}

	return tickers, nil
}

func (p *CoinwProvider) GetAvailablePairs() ([]string, error) {
	tickers, err := p.getTickers()
	if err != nil {
		p.logger.Err(err).Msg("fail to get tickers")
		return nil, err
	}

	var symbols []string
	for _, item := range tickers {
		symbols = append(symbols, item.Symbol)
	}

	return symbols, nil
}

func (p *CoinwProvider) ToProviderSymbol(base, quote string) string {
	return base + "_" + quote
}

// Polling

func (p *CoinwProvider) Poll() {
	tickers, err := p.getTickers()
	if err != nil {
		p.logger.Err(err).Msg("fail to get tickers")
		return
	}

	for _, ticker := range tickers {
		pair, found := p.pairs[ticker.Symbol]
		if !found {
			continue
		}

		p.setTicker(
			time.Now(),
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.Volume),
			nil,
		)
	}
}

// Websocket

func (p *CoinwProvider) PrepareConnection() error {
	// replace symbols by ticker ids, which is used in ws messages instead
	// e.g.: BTC_USDT -> 78

	tickers, err := p.getTickers()
	if err != nil {
		p.logger.Err(err).Msg("fail to get tickers")
		return err
	}

	for _, ticker := range tickers {
		pair, found := p.pairs[ticker.Symbol]
		if !found {
			continue
		}
		delete(p.pairs, ticker.Symbol)
		p.pairs[fmt.Sprintf("%d", ticker.Id)] = pair
	}

	return nil
}

func (p *CoinwProvider) Subscribe() error {
	for id := range p.pairs {
		msg := map[string]any{
			"event": "sub",
			"params": map[string]any{
				"biz":      "exchange",
				"type":     "ticker",
				"pairCode": id,
			},
		}

		err := wsjson.Write(p.ctx, p.ws, msg)
		if err != nil {
			p.logger.Err(err).Msg("fail to subscribe")
			return err
		}
	}
	return nil
}

func (p *CoinwProvider) HandleWsMessage(msg []byte) error {
	var tickerMsg CoinwWsTickerMsg
	var subscriptionMsg CoinwWsSubscriptionMsg

	err := json.Unmarshal(msg, &tickerMsg)
	if err == nil && tickerMsg.Type == "ticker" && tickerMsg.Data != "" {
		var ticker CoinwWsTicker
		err = json.Unmarshal([]byte(tickerMsg.Data), &ticker)
		if err != nil {
			p.logger.Err(err).Msg("fail to parse ticker")
			return err
		}

		pair, found := p.pairs[ticker.Symbol]
		if !found {
			return nil
		}

		timestamp := time.Now()

		p.setTicker(
			timestamp,
			pair,
			strToFloat(ticker.Price),
			strToFloat(ticker.Volume),
			nil,
		)

		return nil
	}

	err = json.Unmarshal(msg, &subscriptionMsg)
	if err == nil && subscriptionMsg.Channel == "subscribe" {
		return nil
	}

	return ErrUnknownWsMsg
}
