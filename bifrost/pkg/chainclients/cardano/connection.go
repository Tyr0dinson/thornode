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

type connection struct {
	conn   *ouroboros.Connection
	error  chan error
	logger zerolog.Logger
}

type NodeConnection struct {
	*connection
}

type ClientConnection struct {
	*connection
	mtx sync.Mutex
}

//  generic
// ----------------------------------------------------------------------------

func newConnection(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (*connection, error) {
	c := &connection{
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

func (c *connection) Dial(address string) error {
	return c.conn.Dial("tcp", address)
}

//  node
// ----------------------------------------------------------------------------

func NewNodeConnection(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (*NodeConnection, error) {
	c, err := newConnection(logger, address, rollBackwardFunc, rollForwardFunc)
	if err != nil {
		return nil, err
	}

	return &NodeConnection{c}, nil
}

//  client
// ----------------------------------------------------------------------------

func NewClientConnection(
	logger zerolog.Logger,
	address string,
) (*ClientConnection, error) {
	c, err := newConnection(logger, address, nil, nil)
	if err != nil {
		return nil, err
	}

	return &ClientConnection{
		connection: c,
		mtx:        sync.Mutex{},
	}, nil
}

func (c *ClientConnection) release() {
	err := c.conn.LocalStateQuery().Client.Release()
	if err != nil {
		c.logger.Err(err).Msg("failed to release client")
	}
	c.mtx.Unlock()
}

func (c *ClientConnection) client() *localstatequery.Client {
	c.mtx.Lock()
	return c.conn.LocalStateQuery().Client
}

func (c *ClientConnection) GetChainBlockNo() (int64, error) {
	client := c.client()
	defer c.release()
	return client.GetChainBlockNo()
}

func (c *ClientConnection) GetCurrentProtocolParams() (lcommon.ProtocolParameters, error) {
	client := c.client()
	defer c.release()
	return client.GetCurrentProtocolParams()
}

func (c *ClientConnection) GetChainPoint() (*pcommon.Point, error) {
	client := c.client()
	defer c.release()
	return client.GetChainPoint()
}

func (c *ClientConnection) GetGenesisConfig() (*localstatequery.GenesisConfigResult, error) {
	client := c.client()
	defer c.release()
	return client.GetGenesisConfig()
}

func (c *ClientConnection) GetUTxOByAddress(
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

func (c *ClientConnection) SubmitTx(era uint16, bz []byte) error {
	return c.conn.LocalTxSubmission().Client.SubmitTx(era, bz)
}
