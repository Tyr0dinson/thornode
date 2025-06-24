package providers

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/config"
	"time"
)

type websocketProvider struct {
	*baseProvider
	wsEndpoint      string
	ws              *websocket.Conn
	poll            func()
	handleWsMessage func([]byte) error
	subscribe       func() error
}

func newWebsocketProvider(
	ctx context.Context,
	logger zerolog.Logger,
	name string,
	config config.BifrostOracleProviderConfiguration,
) (*websocketProvider, error) {
	if len(config.WsEndpoints) == 0 {
		return nil, fmt.Errorf("no websocket endpoints provided")
	}

	var err error

	provider := websocketProvider{
		wsEndpoint: config.WsEndpoints[0],
	}

	provider.baseProvider, err = newBaseProvider(ctx, logger, name, config)
	if err != nil {
		return nil, err
	}

	return &provider, nil
}

func (p *websocketProvider) init(provider types.WebsocketProvider) error {
	err := p.baseProvider.init(provider)
	if err != nil {
		p.logger.Err(err).Msg("Error initializing baseProvider")
		return err
	}

	p.poll = provider.Poll
	p.handleWsMessage = provider.HandleWsMessage
	p.subscribe = provider.Subscribe

	return nil
}

func (p *websocketProvider) Start() {
	p.logger.Info().Msg("starting provider")
	p.wg.Add(1)

	p.poll()
	time.Sleep(time.Second)

	go func() {
		var err error
		p.ws, _, err = websocket.Dial(p.ctx, p.wsEndpoint, nil)
		if err != nil {
			p.logger.Err(err).Msg("failed to connect to websocket")
			return
		}
		defer func() {
			err = p.ws.Close(websocket.StatusNormalClosure, "")
			if err != nil {
				p.logger.Err(err).Msg("failed to close websocket")
			}
		}()

		err = p.subscribe()
		if err != nil {
			p.logger.Err(err).Msg("failed to subscribe to websocket")
			return
		}

		for {
			var msg []byte
			_, msg, err = p.ws.Read(p.ctx)
			if err != nil {
				p.logger.Err(err).Msg("read error")
				return
			}

			err = p.handleWsMessage(msg)
			if err != nil {
				p.logger.Err(err).Msg("fail handling ws message")
			}
		}
	}()
}
