package providers

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket/wsjson"
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

type CryptoWsTickersResponse struct {
	Result struct {
		Data []struct {
			Symbol string `json:"i"`
			Price  string `json:"a"`
			Volume string `json:"v"`
		}
	} `json:"result"`
}

type CryptoWsHeartbeat struct {
	Id     uint64 `json:"id"`
	Method string `json:"method"`
	Code   int    `json:"code"`
}

type CryptoProvider struct {
	*websocketProvider
}

func NewCryptoProvider(
	ctx context.Context,
	logger zerolog.Logger,
	config config.BifrostOracleProviderConfiguration,
) (*CryptoProvider, error) {
	wsProvider, err := newWebsocketProvider(ctx, logger, common.ProviderCrypto, config)
	if err != nil {
		return nil, err
	}

	provider := CryptoProvider{wsProvider}
	err = provider.init(&provider)
	if err != nil {
		return nil, err
	}

	return &provider, nil
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

// Websocket

func (p *CryptoProvider) Subscribe() error {
	var channels []string

	for _, pair := range p.pairs {
		channels = append(channels, "ticker."+pair.Join("_"))
	}

	msg := map[string]any{
		"method": "subscribe",
		"params": map[string]any{
			"channels": channels,
		},
		"id":    1,
		"nonce": 1,
	}

	err := wsjson.Write(p.ctx, p.ws, msg)
	if err != nil {
		p.logger.Err(err).Msg("fail to subscribe")
		return err
	}

	return nil
}

func (p *CryptoProvider) HandleWsMessage(msg []byte) error {
	var response CryptoWsTickersResponse

	err := json.Unmarshal(msg, &response)
	if err == nil && response.Result.Data != nil {
		for _, data := range response.Result.Data {

			pair, found := p.pairs[data.Symbol]
			if !found {
				return nil
			}

			timestamp := time.Now()

			p.setTicker(
				timestamp,
				pair,
				strToFloat(data.Price),
				strToFloat(data.Volume),
				nil,
			)
		}

		return nil
	}

	var heartbeat CryptoWsHeartbeat

	err = json.Unmarshal(msg, &heartbeat)
	if err == nil {
		p.logger.Info().Msg("received heartbeat")
		r := map[string]any{
			"id":     heartbeat.Id,
			"method": "public/respond-heartbeat",
		}
		p.logger.Info().Interface("response", r).Msg("sending heartbeat response")
		err = wsjson.Write(p.ctx, p.ws, r)
		if err != nil {
			p.logger.Err(err).Msg("fail to send heartbeat response")
		}
		return nil
	}

	p.logger.Err(err).
		Str("msg", string(msg)).
		Msg("fail to parse ws message")

	return err
}
