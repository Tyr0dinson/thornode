package cardano

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ouroboroscommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	leveldbutil "github.com/syndtr/goleveldb/leveldb/util"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cardano"
	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	_ shared.ChainClient               = &Client{}
	_ blockscanner.BlockScannerFetcher = &Client{}
	// epoch 542 started on Feb 28, 2025 3:44:51 PM and ends at Mar 5, 2025 3:44:51 PM
	epoch542StartBlock = ouroboroscommon.Tip{
		Point: ouroboroscommon.Point{
			Slot: 149563179,
			// hash: 0xdad58ad00057569d7e32f2939ec7fee828971823b7235a748d925868c5836c30
			Hash: []byte{218, 213, 138, 208, 0, 87, 86, 157, 126, 50, 242, 147, 158, 199, 254, 232, 40, 151, 24, 35, 183, 35, 90, 116, 141, 146, 88, 104, 197, 131, 108, 48},
		},
		BlockNumber: 11561640,
	}
)

type Client struct {
	logger          zerolog.Logger
	networkError    chan error
	config          config.BifrostChainConfiguration
	nodeConn        *ouroboros.Connection
	clientConn      *ouroboros.Connection
	thorchainBridge thorclient.ThorchainBridge
	storage         *blockscanner.BlockScannerStorage
	scanner         *blockscanner.BlockScanner

	startHeight uint64
	n2cLock     sync.Mutex
}

func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	thorchainBridge thorclient.ThorchainBridge,
	metrics *metrics.Metrics,
) (*Client, error) {
	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	c := &Client{
		thorchainBridge: thorchainBridge,
		logger:          log.With().Str("module", cfg.ChainID.String()).Logger(),
		config:          cfg,
	}

	_, err := tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}
	_, err = thorKeys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	var path string // if not set later, will in memory storage
	c.logger.Info().Msgf("block scanner db path: %s", path)
	if len(cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", cfg.BlockScanner.DBPath, cfg.BlockScanner.ChainID)
	}
	c.logger.Info().Msgf("final path: %s", path)
	c.storage, err = blockscanner.NewBlockScannerStorage(path, cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

	// create ouroboros connection
	networkID := uint64(1)
	if cfg.ChainNetwork != "" {
		networkID, err = strconv.ParseUint(cfg.ChainNetwork, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("error parsing chain network as uint, %w", err)
		}
	}
	network, ok := ouroboros.NetworkById(uint8(networkID))
	if !ok {
		return nil, fmt.Errorf("invalid network id %d", networkID)
	}
	c.networkError = make(chan error)
	go func() {
		for {
			err := <-c.networkError
			c.logger.Err(err).Msg("ouroboros network error")
			time.Sleep(500)
		}
	}()
	networkLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	peer := network.BootstrapPeers[0]
	addr := fmt.Sprintf("%s:%d", peer.Address, peer.Port)
	n2nConn, err := c.dialTCP(addr)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2n host: %w", err)
	}
	c.nodeConn, err = ouroboros.NewConnection(
		ouroboros.WithPeerSharing(true),
		ouroboros.WithConnection(n2nConn),
		ouroboros.WithNetwork(network),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithErrorChan(c.networkError),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithKeepAlive(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollBackwardFunc(c.chainSyncRollBackwardHandler),
			chainsync.WithRollForwardFunc(c.chainSyncRollForwardHandler),
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros n2n connection: %w", err)
	}

	n2cConn, err := c.dialTCP(cfg.RPCHost)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2c host: %w", err)
	}
	c.clientConn, err = ouroboros.NewConnection(
		ouroboros.WithConnection(n2cConn),
		ouroboros.WithErrorChan(c.networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetwork(network),
		// ouroboros.WithLocalStateQueryConfig(localstatequery.NewConfig()),
		// ouroboros.WithLocalTxSubmissionConfig(
		// 	localtxsubmission.NewConfig(
		// 		localtxsubmission.WithSubmitTxFunc(localSubmitTxCallbackHandler),
		// 	),
		// ),
		ouroboros.WithKeepAlive(true),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros n2c connection: %w", err)
	}

	c.startHeight, _, _ = c.getLastSeenBlock()
	if cfg.BlockScanner.StartBlockHeight > 0 {
		slot, _, _ := c.getBlockId(uint64(cfg.BlockScanner.StartBlockHeight))
		if slot == 0 {
			// assume we're missing data and start from epoch
			err := c.setBlockId(epoch542StartBlock.BlockNumber, epoch542StartBlock.Point.Slot, epoch542StartBlock.Point.Hash)
			if err != nil {
				return nil, fmt.Errorf("fail to set epoch block id: %w", err)
			}
			c.startHeight = epoch542StartBlock.BlockNumber
		} else {
			c.startHeight = uint64(cfg.BlockScanner.StartBlockHeight)
		}
	}
	if c.startHeight == 0 {
		tip, err := c.nodeConn.ChainSync().Client.GetCurrentTip()
		if err != nil {
			return nil, fmt.Errorf("fail to get current tip: %w", err)
		}
		c.logger.Info().Msgf("no last seen block, current tip slot=%d hash=%x", tip.Point.Slot, tip.Point.Hash)

		if err := c.setBlockId(tip.BlockNumber, tip.Point.Slot, tip.Point.Hash); err != nil {
			return nil, fmt.Errorf("fail to set block id: %w", err)
		}
		c.startHeight = tip.BlockNumber
	}

	// create block scanner
	c.scanner, err = blockscanner.NewBlockScanner(cfg.BlockScanner, c.storage, metrics, thorchainBridge, c)
	if err != nil {
		return nil, fmt.Errorf("fail to create block scanner: %w", err)
	}

	return c, nil
}

func (c *Client) dialTCP(addr string) (conn net.Conn, err error) {
	if c.config.DisableTLS {
		conn, err = net.Dial("tcp", addr)
	} else {
		conn, err = tls.Dial("tcp", addr, nil)
	}
	return
}

func (c *Client) BroadcastTx(_ types.TxOutItem, _ []byte) (string, error) {
	panic("unimplemented")
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	panic("unimplemented")
}

func (c *Client) GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error) {
	// is this pubkey expected to be an ed25519 public key?
	addr, err := poolPubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get address")
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	addr, err := ledger.NewAddress(address)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to create address: %w", err)
	}
	queryClient := c.clientConn.LocalStateQuery().Client

	h := height.Uint64()
	slot, hash, err := c.getBlockId(h)
	if err != nil {
		c.logger.Warn().Err(err).Uint64("height", h).Msg("fail to get block id for height, falling back to tip")
	} else {
		c.n2cLock.Lock()
		defer c.n2cLock.Unlock()
		point := ouroboroscommon.NewPoint(slot, hash)
		err = queryClient.Acquire(&point)
		if err != nil {
			return common.Account{}, fmt.Errorf("fail to acquire point: %w", err)
		}
		defer func(client *localstatequery.Client) {
			if err := client.Release(); err != nil {
				c.logger.Err(err).Msg("fail to release point")
			}
		}(queryClient)
	}

	res, err := queryClient.GetUTxOByAddress([]ledger.Address{addr})
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to query address: %w", err)
	}
	var balance uint64
	for _, utxo := range res.Results {
		balance += utxo.Amount()
	}
	coin := common.NewCoin(common.ADAAsset, sdkmath.NewUint(balance))
	return common.Account{
		Coins: common.Coins{coin},
	}, nil
}

func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	// is this pubkey expected to be an ed25519 public key?
	addr, err := poolPubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get address")
		return ""
	}
	return addr.String()
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.scanner.PreviousHeight(), nil
}

func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.config
}

func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	//  n2c call may be slow, may also query leveldb if chain-sync is current
	tip, err := c.clientConn.LocalStateQuery().Client.GetChainBlockNo()
	if err != nil {
		c.logger.Err(err).Str("op", "GetConfirmationCount").Msg("fail to get current tip")
		return 0
	}
	var height int64
	for _, tx := range txIn.TxArray {
		if tx.BlockHeight > height {
			height = tx.BlockHeight
		}
	}
	return tip - height
}

// GetHeight returns the current height of the chain.
// Implements both ChainClient and BlockScannerFetcher interfaces.
func (c *Client) GetHeight() (int64, error) {
	height, _, _ := c.getLastSeenBlock()
	return int64(height), nil
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	panic("unimplemented")
}

func (c *Client) IsBlockScannerHealthy() bool {
	return c.scanner.IsHealthy()
}

func (c *Client) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {
	panic("unimplemented")
}

func (c *Client) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	panic("unimplemented")
}

func (c *Client) Start(
	globalTxsQueue chan types.TxIn,
	globalErrataQueue chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
) {
	slot, hash, err := c.getBlockId(c.startHeight)
	if err != nil {
		c.logger.Err(err).Msgf("client start failed, unable to find block id for height=%d", c.startHeight)
		return
	}
	err = c.nodeConn.ChainSync().Client.Sync([]ouroboroscommon.Point{ouroboroscommon.NewPoint(slot, hash)})
	if err != nil {
		c.logger.Err(err).Msg("failed to start chain-sync client")
		return
	}

	c.scanner.Start(globalTxsQueue)
	c.logger.Info().Msg("started cardano client")
}

func (c *Client) Stop() {
	if err := c.nodeConn.ChainSync().Client.Stop(); err != nil {
		c.logger.Err(err).Msg("fail to stop chain-sync client")
	}
}

// FetchMemPool implements blockscanner.BlockScannerFetcher.
func (c *Client) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

// FetchTxs implements blockscanner.BlockScannerFetcher.
func (c *Client) FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error) {
	c.logger.Info().Msgf("FetchTxs: fetch_height=%d chain_height=%d", fetchHeight, chainHeight)
	slot, hash, err := c.getBlockId(uint64(fetchHeight))
	if err != nil {
		return types.TxIn{}, err
	}
	block, err := c.nodeConn.BlockFetch().Client.GetBlock(ouroboroscommon.NewPoint(slot, hash))
	if err != nil {
		return types.TxIn{}, err
	}

	var txIns []types.TxInItem
	for _, tx := range block.Transactions() {
		if !tx.IsValid() {
			continue
		}

		// try to get sender from witness
		// if we make certain assumptions about tx origins we can skip n2c protocols and utxo queries
		if len(tx.Witnesses().Vkey()) != 1 {
			// c.logger.Warn().Msgf("skip tx with %d witnesses", len(tx.Witnesses().Vkey()))
			continue
		}
		senderAddr, err := cardano.PaymentOnlyMainnetAddressFromPubkey(tx.Witnesses().Vkey()[0].Vkey)
		if err != nil {
			c.logger.Err(err).Msg("fail to get sender address")
			continue
		}

		isSenderAsgard := c.isAsgardAddress(senderAddr)
		// expectation is one output address per filtered tx
		// see: https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/utxo/client_internal.go#L724
		// which collapses multiple VOUTs into a single address based on filter criteria
		var (
			toAddress *ledger.Address
			amount    uint64
		)
		for _, txOut := range tx.Outputs() {
			if !isSenderAsgard && !c.isAsgardAddress(txOut.Address().Bytes()) {
				// skip txs that don't involve asgard addresses
				continue
			}
			addr := txOut.Address()
			toAddress = &addr
			amount = txOut.Amount()
		}
		if toAddress == nil {
			continue
		}

		memo, err := DecodeMemo(tx)
		if err != nil {
			c.logger.Err(err).Msg("fail to decode tx memo")
			continue
		}

		txIns = append(txIns, types.TxInItem{
			Tx:          tx.Hash(),
			BlockHeight: fetchHeight,
			Memo:        memo,
			Sender:      senderAddr.String(),
			To:          toAddress.String(),
			Coins:       common.Coins{common.NewCoin(common.ADAAsset, sdkmath.NewUint(amount))},
			Gas:         common.Gas{common.NewCoin(common.ADAAsset, sdkmath.NewUint(tx.Fee()))},
		})
	}

	return types.TxIn{
		Count:    strconv.Itoa(len(txIns)),
		Chain:    c.config.ChainID,
		TxArray:  txIns,
		Filtered: false,
		MemPool:  false,
	}, nil
}

func (c *Client) isAsgardAddress(_ []byte) bool {
	// TODO
	// ref impl: https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/utxo/client_internal.go#L49
	return false
}

func (c *Client) chainSyncRollForwardHandler(
	ctx chainsync.CallbackContext,
	blockType uint,
	blockData any,
	tip chainsync.Tip,
) error {
	switch h := blockData.(type) {
	case ledger.Block:
		c.logger.Warn().Msg("block type is ledger.Block, chain-sync n2n protocol expects ledger.BlockHeader")
	case ledger.BlockHeader:
		// c.logger.Info().Msgf("roll forward: block_no=%d slot=%d hash=%x ", h.BlockNumber(), h.SlotNumber(), h.Hash())
		hashBz, err := hex.DecodeString(h.Hash())
		if err != nil {
			c.logger.Err(err).Msg("fail to decode block hash")
		}
		err = c.setBlockId(h.BlockNumber(), h.SlotNumber(), hashBz)
		if err != nil {
			c.logger.Err(err).Msg("fail to set block id")
		}
	}
	return nil
}

func (c *Client) chainSyncRollBackwardHandler(
	ctx chainsync.CallbackContext,
	point ouroboroscommon.Point,
	tip chainsync.Tip,
) error {
	fmt.Printf("roll backward: point = (%d, %x), tip = (%d, %x)\n",
		point.Slot, point.Hash,
		tip.Point.Slot, tip.Point.Hash,
	)
	return nil
}

// STORAGE

const (
	blockIDKeyPrefix = "chainblock"
)

func (c *Client) getLastSeenBlock() (height uint64, slot uint64, hash []byte) {
	itr := c.storage.GetInternalDb().NewIterator(&leveldbutil.Range{Start: []byte(blockIDKeyPrefix)}, nil)
	ok := itr.First()
	if !ok {
		return
	}
	key := itr.Key()
	value := itr.Value()
	slot = binary.BigEndian.Uint64(value[:8])
	hash = value[8:]
	invertedHeight := binary.BigEndian.Uint64(key[len(blockIDKeyPrefix):])
	height = ^invertedHeight
	return
}

func (c *Client) blockIDKey(height uint64) []byte {
	buf := make([]byte, 8+len(blockIDKeyPrefix))
	copy(buf[:], blockIDKeyPrefix)
	inverted := ^height
	binary.BigEndian.PutUint64(buf[len(blockIDKeyPrefix):], inverted)
	return buf
}

func (c *Client) setBlockId(height uint64, slot uint64, hash []byte) error {
	buf := make([]byte, 8+32)
	binary.BigEndian.PutUint64(buf[:8], slot)
	copy(buf[8:], hash)
	return c.storage.GetInternalDb().Put(c.blockIDKey(height), buf, nil)
}

func (c *Client) getBlockId(height uint64) (slot uint64, hash []byte, err error) {
	buf, err := c.storage.GetInternalDb().Get(c.blockIDKey(height), nil)
	if err != nil {
		return
	}
	slot = binary.BigEndian.Uint64(buf[:8])
	hash = buf[8:]
	return
}
