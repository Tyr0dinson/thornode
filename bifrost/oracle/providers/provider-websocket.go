package providers

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/oracle/types"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	minBackoff             = time.Second
	maxBackoff             = time.Minute * 5
	keepaliveCheckInterval = time.Second * 2
)

type websocketProvider struct {
	*baseProvider
	wsEndpoint      string
	ws              atomic.Pointer[websocket.Conn]
	poll            func()
	handleWsMessage func([]byte) error
	preconnect      func() error
	ping            func() error
	getSubs         func() ([]map[string]any, error)
	backoff         time.Duration

	lastKeepalive     atomic.Int64
	keepaliveInterval time.Duration
	keepaliveTimeout  time.Duration
}

func newWebsocketProvider(
	ctx context.Context,
	logger zerolog.Logger,
	name string,
	config config.BifrostOracleProviderConfiguration,
	metrics *metrics.Metrics,
) (*websocketProvider, error) {
	if len(config.WsEndpoints) == 0 {
		return nil, fmt.Errorf("no websocket endpoints provided")
	}

	var err error

	provider := websocketProvider{
		wsEndpoint: config.WsEndpoints[0],
		backoff:    minBackoff,
	}

	provider.baseProvider, err = newBaseProvider(ctx, logger, name, config, metrics)
	if err != nil {
		return nil, err
	}

	return &provider, nil
}

func (p *websocketProvider) writeJson(msg any) error {
	ws := p.ws.Load()
	if ws == nil {
		return fmt.Errorf("ws not connected")
	}
	return wsjson.Write(p.ctx, ws, msg)
}

func (p *websocketProvider) writeRaw(t websocket.MessageType, data []byte) error {
	ws := p.ws.Load()
	if ws == nil {
		return fmt.Errorf("ws not connected")
	}
	return ws.Write(p.ctx, t, data)
}

func (p *websocketProvider) init(provider types.WebsocketProvider) error {
	err := p.baseProvider.init(provider)
	if err != nil {
		p.logger.Err(err).Msg("Error initializing baseProvider")
		return err
	}

	p.poll = provider.Poll
	p.getSubs = provider.GetSubscriptionMsgs
	p.handleWsMessage = provider.HandleWsMessage
	p.preconnect = provider.PrepareConnection
	p.ping = provider.Ping
	return nil
}

func (p *websocketProvider) subscribe() error {
	msgs, err := p.getSubs()
	if err != nil {
		return err
	}

	for i, msg := range msgs {
		err = p.writeJson(msg)
		if err != nil {
			p.logger.Err(err).Msg("fail to subscribe")
			return err
		}

		if i > 0 {
			time.Sleep(time.Millisecond * 100)
		}
	}

	return nil
}

func (p *websocketProvider) connect() error {
	p.logger.Info().Msg("connecting")

	ws := p.ws.Load()
	if ws != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		time.Sleep(time.Second)
	}

	err := p.preconnect()
	if err != nil {
		p.logger.Err(err).Msg("failed preparing websocket connection")
		return err
	}

	ws, _, err = websocket.Dial(p.ctx, p.wsEndpoint, nil)
	if err != nil {
		p.logger.Err(err).Msg("failed to connect")
		return err
	}
	p.ws.Store(ws)

	err = p.subscribe()
	if err != nil {
		p.logger.Err(err).Msg("failed to subscribe")
		return err
	}

	p.markAlive()

	time.Sleep(time.Second)

	return nil
}

func (p *websocketProvider) reconnect() error {
	select {
	case <-p.ctx.Done():
		return fmt.Errorf("context canceled")
	case <-time.After(p.backoff):
		p.backoff = min(p.backoff*2, maxBackoff)
		return p.connect()
	}
}

func (p *websocketProvider) markAlive() {
	p.lastKeepalive.Store(time.Now().UnixNano())
}

func (p *websocketProvider) sinceAlive() time.Duration {
	return time.Since(time.Unix(0, p.lastKeepalive.Load()))
}

func (p *websocketProvider) PrepareConnection() error {
	return nil
}

func (p *websocketProvider) Ping() error {
	return nil
}

func (p *websocketProvider) Start() {
	p.logger.Info().Msg("starting provider")
	// p.wg.Add(1)

	p.poll()

	err := p.connect()
	if err != nil {
		p.logger.Err(err).Msg("failed to connect")
		return
	}

	if p.keepaliveTimeout > 0 {
		go func() {
			ticker := time.NewTicker(keepaliveCheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-p.ctx.Done():
					return
				case <-ticker.C:
					ws := p.ws.Load()
					if ws != nil && p.sinceAlive() > p.keepaliveTimeout {
						_ = ws.Close(websocket.StatusInternalError, "keepalive timeout")
					}
				}
			}
		}()
	}

	go func() {
		defer func() {
			// get pointer and replace it with nil so no further action can
			// be made to it
			ws := p.ws.Swap(nil)
			if ws != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
			}
		}()

		var ticker *time.Ticker
		if p.keepaliveInterval > 0 {
			ticker = time.NewTicker(p.keepaliveInterval)
			defer ticker.Stop()
		}

		for {
			select {
			case <-p.ctx.Done():
				return
			default:
			}

			ws := p.ws.Load()

			if ws == nil {
				_ = p.reconnect()
				continue
			}

			var msg []byte
			_, msg, err = ws.Read(p.ctx)
			if err != nil {
				_ = p.reconnect()
				continue
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

			// reset backoff
			p.backoff = minBackoff

			if ticker == nil {
				continue
			}

			select {
			case <-ticker.C:
				err = p.ping()
				if err != nil {
					p.logger.Err(err).Msg("failed to send ping")
				}
			default:
			}
		}
	}()
}
