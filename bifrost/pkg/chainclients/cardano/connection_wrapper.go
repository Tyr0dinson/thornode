package cardano

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/keepalive"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog"
)

//  generic
// ----------------------------------------------------------------------------

type wrapper struct {
	conn   *ouroboros.Connection
	error  chan error
	logger zerolog.Logger
}

func newWrapper(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (*wrapper, error) {
	c := &wrapper{
		error:  make(chan error),
		logger: logger,
	}

	go func() {
		for {
			err := <-c.error
			c.logger.Err(err).Msg("ouroboros network error")
			time.Sleep(time.Millisecond * 500)
		}
	}()

	networkLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	var err error

	if rollBackwardFunc != nil || rollForwardFunc != nil {
		c.conn, err = ouroboros.NewConnection(
			ouroboros.WithPeerSharing(true),
			ouroboros.WithNetworkMagic(networkMagic),
			ouroboros.WithLogger(networkLogger),
			ouroboros.WithErrorChan(c.error),
			ouroboros.WithNodeToNode(true),
			ouroboros.WithKeepAlive(true),
			ouroboros.WithKeepAliveConfig(keepalive.NewConfig(
				keepalive.WithPeriod(10*time.Second),
			)),
			ouroboros.WithChainSyncConfig(chainsync.NewConfig(
				chainsync.WithRollBackwardFunc(rollBackwardFunc),
				chainsync.WithRollForwardFunc(rollForwardFunc),
			)),
		)
	} else {
		c.conn, err = ouroboros.NewConnection(
			ouroboros.WithErrorChan(c.error),
			ouroboros.WithLogger(networkLogger),
			ouroboros.WithNetworkMagic(networkMagic),
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2c connection: %w", err)
	}

	err = c.conn.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ouroboros n2c connection: %w", err)
	}

	return c, nil
}

func (c *wrapper) Dial(address string) error {
	return c.conn.Dial("tcp", address)
}

//  node
// ----------------------------------------------------------------------------

type NodeWrapper struct {
	*wrapper
}

func NewNodeConnection(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (*NodeWrapper, error) {
	c, err := newWrapper(logger, address, rollBackwardFunc, rollForwardFunc)
	if err != nil {
		return nil, err
	}

	return &NodeWrapper{c}, nil
}

//  client
// ----------------------------------------------------------------------------

type ClientWrapper interface {
	GetChainBlockNo() (int64, error)
	GetCurrentProtocolParams() (lcommon.ProtocolParameters, error)
	GetChainPoint() (*pcommon.Point, error)
	GetGenesisConfig() (*localstatequery.GenesisConfigResult, error)
	GetUTxOByAddress([]ledger.Address, *pcommon.Point) (*localstatequery.UTxOsResult, error)
	SubmitTx(uint16, []byte) error
}

type clientWrapper struct {
	*wrapper
	mtx sync.Mutex
}

func NewClientConnection(
	logger zerolog.Logger,
	address string,
) (ClientWrapper, error) {
	c, err := newWrapper(logger, address, nil, nil)
	if err != nil {
		return nil, err
	}

	return &clientWrapper{
		wrapper: c,
		mtx:     sync.Mutex{},
	}, nil
}

func (c *clientWrapper) release() {
	err := c.conn.LocalStateQuery().Client.Release()
	if err != nil {
		c.logger.Err(err).Msg("failed to release client")
	}
	c.mtx.Unlock()
}

func (c *clientWrapper) client() *localstatequery.Client {
	c.mtx.Lock()
	return c.conn.LocalStateQuery().Client
}

func (c *clientWrapper) GetChainBlockNo() (int64, error) {
	client := c.client()
	defer c.release()
	return client.GetChainBlockNo()
}

func (c *clientWrapper) GetCurrentProtocolParams() (lcommon.ProtocolParameters, error) {
	client := c.client()
	defer c.release()
	return client.GetCurrentProtocolParams()
}

func (c *clientWrapper) GetChainPoint() (*pcommon.Point, error) {
	client := c.client()
	defer c.release()
	return client.GetChainPoint()
}

func (c *clientWrapper) GetGenesisConfig() (*localstatequery.GenesisConfigResult, error) {
	client := c.client()
	defer c.release()
	return client.GetGenesisConfig()
}

func (c *clientWrapper) GetUTxOByAddress(
	addresses []ledger.Address,
	point *pcommon.Point,
) (*localstatequery.UTxOsResult, error) {
	client := c.client()

	if point != nil {
		err := client.Acquire(point)
		if err != nil {
			c.release()
			c.logger.Err(err).Msg("failed to acquire point")
			return nil, err
		}
	}

	defer c.release()

	return client.GetUTxOByAddress(addresses)
}

func (c *clientWrapper) SubmitTx(era uint16, bz []byte) error {
	return c.conn.LocalTxSubmission().Client.SubmitTx(era, bz)
}
