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

const (
	minBackoff             = time.Second
	maxBackoff             = time.Minute * 2
	keepaliveCheckInterval = time.Second * 2
)

type websocketProvider struct {
	*baseProvider
	wsEndpoint      string
	ws              *websocket.Conn
	poll            func()
	handleWsMessage func([]byte) error
	subscribe       func() error
	backoff         time.Duration

	lastKeepalive     time.Time
	keepaliveInterval time.Duration
	keepaliveTimeout  time.Duration
	sendKeepalive     func() error
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
		backoff:    minBackoff,
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

func (p *websocketProvider) connect() error {
	p.logger.Info().Msg("connecting")

	var err error

	if p.ws != nil {
		_ = p.ws.Close(websocket.StatusNormalClosure, "")
	}

	p.ws, _, err = websocket.Dial(p.ctx, p.wsEndpoint, nil)
	if err != nil {
		p.logger.Err(err).Msg("failed to connect")
		return err
	}

	err = p.subscribe()
	if err != nil {
		p.logger.Err(err).Msg("failed to subscribe")
		return err
	}

	p.lastKeepalive = time.Now()

	return nil
}

func (p *websocketProvider) reconnect() error {
	p.logger.Warn().Msgf("trying to reconnect in %s", p.backoff.String())

	time.Sleep(p.backoff)

	err := p.connect()
	if err == nil {
		p.backoff = minBackoff
	} else {
		p.backoff = max(p.backoff*2, maxBackoff)
	}

	return err
}

func (p *websocketProvider) setKeepalive(
	keepalive func() error,
	interval, timeout time.Duration,
) {
	p.keepaliveInterval = interval
	p.keepaliveTimeout = timeout
	p.sendKeepalive = keepalive
}

func (p *websocketProvider) Start() {
	p.logger.Info().Msg("starting provider")
	p.wg.Add(1)

	p.poll()

	err := p.connect()

	if err != nil {
		p.logger.Err(err).Msg("failed to connect")
		return
	}

	go func() {
		defer p.ws.Close(websocket.StatusNormalClosure, "")

		var keepalive, check *time.Ticker
		if p.keepaliveInterval > 0 && p.sendKeepalive != nil {
			keepalive = time.NewTicker(p.keepaliveInterval)
			check = time.NewTicker(keepaliveCheckInterval)
			defer keepalive.Stop()
		}

		for {
			if keepalive != nil {
				select {
				case <-keepalive.C:
					err = p.sendKeepalive()
					if err != nil {
						p.logger.Err(err).Msg("failed to send keepalive")
					}
				case <-check.C:
					if time.Since(p.lastKeepalive) > p.keepaliveTimeout {
						p.logger.Error().Msg("keepalive timeout reached")
						err = p.reconnect()
						if err != nil {
							p.logger.Err(err).Msg("failed to reconnect")
						}
					}
				default:
					// pass
				}
			}

			var msg []byte
			_, msg, err = p.ws.Read(p.ctx)
			if err != nil {
				err = p.reconnect()
				if err != nil {
					continue
				}
			}

			if len(msg) == 0 {
				p.logger.Warn().Msg("message is empty")
				continue
			}

			err = p.handleWsMessage(msg)
			if err != nil {
				p.logger.Err(err).
					Str("msg", string(msg)).
					Msg("fail handling message")
			}
		}
	}()
}
