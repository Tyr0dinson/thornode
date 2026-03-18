package cardano

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
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

var ErrNoConn = fmt.Errorf("client disconnected")

// Cardano communicates via the Ouroboros mini-protocol suite over persistent
// TCP connections. The wrapper handles automatic reconnection with backoff.
// ----------------------------------------------------------------------------

type wrapper struct {
	conn     *ouroboros.Connection
	mtx      sync.Mutex
	done     chan struct{}
	logger   zerolog.Logger
	address  string
	shutdown atomic.Bool
	syncing  atomic.Bool
}

func newWrapper(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (*wrapper, error) {
	c := &wrapper{
		mtx:     sync.Mutex{},
		done:    make(chan struct{}),
		logger:  logger,
		address: address,
	}

	errChan, err := c.connect(rollBackwardFunc, rollForwardFunc)
	if err != nil {
		return nil, err
	}

	go func() {
		for {
			var ok bool

			select {
			case err, ok = <-errChan:
				if !ok {
					if c.shutdown.Load() {
						return
					}
				} else {
					c.logger.Warn().Err(err).Msg("ouroboros network error")
				}

				interval := time.Second
				for i := 1; ; i++ {
					// if this is intentional, return
					if c.shutdown.Load() {
						return
					}

					// try to reconnect otherwise
					errChan, err = c.connect(rollBackwardFunc, rollForwardFunc)
					if err == nil {
						break
					}

					c.logger.Err(err).Msg("failed to reconnect")

					select {
					case <-time.After(interval):
					case <-c.done:
						return
					}

					if interval <= maxRetryInterval {
						interval = min(interval*2, maxRetryInterval)
					}
				}
			case <-c.done:
				return
			}
		}
	}()

	return c, nil
}

func (c *wrapper) connect(
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (chan error, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	var conn *ouroboros.Connection
	var err error

	if rollBackwardFunc != nil || rollForwardFunc != nil {
		conn, err = ouroboros.NewConnection(
			ouroboros.WithPeerSharing(true),
			ouroboros.WithNetworkMagic(networkMagic),
			ouroboros.WithLogger(logger),
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
		conn, err = ouroboros.NewConnection(
			ouroboros.WithLogger(logger),
			ouroboros.WithNetworkMagic(networkMagic),
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2c connection: %w", err)
	}

	err = conn.Dial("tcp", c.address)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to dial ouroboros n2c connection: %w", err)
	}

	c.close()

	c.conn = conn

	c.syncing.Store(false)

	return c.conn.ErrorChan(), nil
}

func (c *wrapper) close() {
	if c.conn == nil {
		return
	}

	err := c.conn.Close()
	if err != nil {
		c.logger.Error().Err(err).Msg("failed to close ouroboros connection")
	}

	c.conn = nil
}

// Node-to-node (N2N) connection for block synchronization via ChainSync,
// a consumer-driven protocol that delivers block headers through
// rollForward/rollBackward callbacks.
// ----------------------------------------------------------------------------

type NodeWrapper interface {
	Sync(pcommon.Point) error
	IsSyncSet() bool
	GetBlock(pcommon.Point) (ledger.Block, error)
	Close()
}
type nodeWrapper struct {
	*wrapper
}

func NewNodeConnection(
	logger zerolog.Logger,
	address string,
	rollBackwardFunc chainsync.RollBackwardFunc,
	rollForwardFunc chainsync.RollForwardFunc,
) (NodeWrapper, error) {
	c, err := newWrapper(logger, address, rollBackwardFunc, rollForwardFunc)
	if err != nil {
		return nil, err
	}

	return &nodeWrapper{c}, nil
}

func (c *nodeWrapper) Sync(point pcommon.Point) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.conn == nil {
		return ErrNoConn
	}
	err := c.conn.ChainSync().Client.Sync([]pcommon.Point{point})
	if err == nil {
		c.syncing.Store(true)
	}
	return err
}

func (c *nodeWrapper) IsSyncSet() bool {
	return c.syncing.Load()
}

func (c *nodeWrapper) GetBlock(point pcommon.Point) (ledger.Block, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.conn == nil {
		return nil, ErrNoConn
	}

	return c.conn.BlockFetch().Client.GetBlock(point)
}

func (c *nodeWrapper) Close() {
	c.shutdown.Store(true)
	close(c.done)

	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.conn == nil {
		return
	}

	err := c.conn.ChainSync().Client.Stop()
	if err != nil {
		c.logger.Error().Err(err).Msg("failed to stop client")
	}

	c.close()
}

// Node-to-client (N2C) connection for state queries (LocalStateQuery) and
// transaction submission (LocalTxSubmission).
// State queries use Acquire/Release semantics to pin the ledger to
// a specific Point (slot + block hash) for consistent reads.
// ----------------------------------------------------------------------------

type ClientWrapper interface {
	GetChainBlockNo() (int64, error)
	GetCurrentProtocolParams() (lcommon.ProtocolParameters, error)
	GetChainPoint() (*pcommon.Point, error)
	GetGenesisConfig() (*localstatequery.GenesisConfigResult, error)
	GetUTxOByAddress([]ledger.Address, *pcommon.Point) (*localstatequery.UTxOsResult, error)
	GetUTxOByTxIn([]ledger.TransactionInput, *pcommon.Point) (*localstatequery.UTxOByTxInResult, error)
	SubmitTx(uint16, []byte) error
	Close()
}

type clientWrapper struct {
	*wrapper
}

func NewClientConnection(
	logger zerolog.Logger,
	address string,
) (ClientWrapper, error) {
	c, err := newWrapper(logger, address, nil, nil)
	if err != nil {
		return nil, err
	}

	return &clientWrapper{wrapper: c}, nil
}

func (c *clientWrapper) release() {
	defer c.mtx.Unlock()
	err := c.conn.LocalStateQuery().Client.Release()
	if err != nil {
		c.logger.Err(err).Msg("failed to release client")
	}
}

// client returns a LocalStateQuery client, optionally acquiring
// a specific chain state Point. Acquiring a Point pins the
// ledger to that slot for consistent reads across queries.
func (c *clientWrapper) client(point *pcommon.Point) (*localstatequery.Client, error) {
	c.mtx.Lock()
	if c.conn == nil {
		c.mtx.Unlock()
		return nil, ErrNoConn
	}

	if point != nil {
		err := c.conn.LocalStateQuery().Client.Acquire(point)
		if err != nil {
			c.mtx.Unlock()
			return nil, fmt.Errorf("failed to acquire point: %w", err)
		}
	}

	return c.conn.LocalStateQuery().Client, nil
}

func (c *clientWrapper) GetChainBlockNo() (int64, error) {
	client, err := c.client(nil)
	if err != nil {
		return 0, err
	}
	defer c.release()
	return client.GetChainBlockNo()
}

func (c *clientWrapper) GetCurrentProtocolParams() (lcommon.ProtocolParameters, error) {
	client, err := c.client(nil)
	if err != nil {
		return nil, err
	}
	defer c.release()
	return client.GetCurrentProtocolParams()
}

func (c *clientWrapper) GetChainPoint() (*pcommon.Point, error) {
	client, err := c.client(nil)
	if err != nil {
		return nil, err
	}
	defer c.release()
	return client.GetChainPoint()
}

func (c *clientWrapper) GetGenesisConfig() (*localstatequery.GenesisConfigResult, error) {
	client, err := c.client(nil)
	if err != nil {
		return nil, err
	}
	defer c.release()
	return client.GetGenesisConfig()
}

func (c *clientWrapper) GetUTxOByAddress(
	addresses []ledger.Address,
	point *pcommon.Point,
) (*localstatequery.UTxOsResult, error) {
	client, err := c.client(point)
	if err != nil {
		return nil, err
	}
	defer c.release()
	return client.GetUTxOByAddress(addresses)
}

func (c *clientWrapper) GetUTxOByTxIn(
	inputs []ledger.TransactionInput,
	point *pcommon.Point,
) (*localstatequery.UTxOByTxInResult, error) {
	client, err := c.client(point)
	if err != nil {
		return nil, err
	}
	defer c.release()
	return client.GetUTxOByTxIn(inputs)
}

func (c *clientWrapper) SubmitTx(era uint16, bz []byte) error {
	c.mtx.Lock()
	if c.conn == nil {
		c.mtx.Unlock()
		return ErrNoConn
	}
	defer c.mtx.Unlock()
	return c.conn.LocalTxSubmission().Client.SubmitTx(era, bz)
}

func (c *clientWrapper) Close() {
	c.shutdown.Store(true)
	close(c.done)

	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.close()
}
