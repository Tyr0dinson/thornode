package cardano

import (
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strconv"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ouroboroscommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

var (
	_ shared.ChainClient               = &Client{}
	_ blockscanner.BlockScannerFetcher = &Client{}
)

type Client struct {
	logger          zerolog.Logger
	networkError    chan error
	config          config.BifrostChainConfiguration
	node            *ouroboros.Connection
	thorchainBridge thorclient.ThorchainBridge
	storage         *blockscanner.BlockScannerStorage
}

func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	thorchainBridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
) (*Client, error) {
	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	c := &Client{
		thorchainBridge: thorchainBridge,
		logger:          log.With().Str("module", cfg.ChainID.String()).Logger(),
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
	if len(c.config.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.config.BlockScanner.DBPath, c.config.BlockScanner.ChainID)
	}
	c.storage, err = blockscanner.NewBlockScannerStorage(path, c.config.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

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
	// Create error channel
	c.networkError = make(chan error)
	// start error handler
	go func() {
		for {
			err := <-c.networkError
			c.logger.Err(err).Msg("ouroboros error occurred")
		}
	}()
	networkLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	peer := network.BootstrapPeers[0]
	addr := fmt.Sprintf("%s:%d", peer.Address, peer.Port)
	var client net.Conn
	if cfg.DisableTLS {
		client, err = net.Dial("tcp", addr)
	} else {
		client, err = tls.Dial("tcp", addr, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to dial: %w", err)
	}
	c.node, err = ouroboros.NewConnection(
		ouroboros.WithConnection(client),
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
		return nil, fmt.Errorf("fail to create ouroboros connection: %w", err)
	}
	return c, nil
}

func (c *Client) BroadcastTx(_ types.TxOutItem, _ []byte) (string, error) {
	panic("unimplemented")
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	panic("unimplemented")
}

func (c *Client) GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error) {
	panic("unimplemented")
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	panic("unimplemented")
}

func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	panic("unimplemented")
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	panic("unimplemented")
}

func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.config
}

func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	panic("unimplemented")
}

func (c *Client) GetHeight() (int64, error) {
	panic("unimplemented")
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	panic("unimplemented")
}

func (c *Client) IsBlockScannerHealthy() bool {
	panic("unimplemented")
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
	c.logger.Info().Msg("starting cardano client")
}

func (c *Client) Stop() {
	panic("unimplemented")
}

// FetchMemPool implements blockscanner.BlockScannerFetcher.
func (c *Client) FetchMemPool(height int64) (types.TxIn, error) {
	panic("unimplemented")
}

// FetchTxs implements blockscanner.BlockScannerFetcher.
func (c *Client) FetchTxs(fetchHeight int64, chainHeight int64) (types.TxIn, error) {
	panic("unimplemented")
}

func (c *Client) chainSyncRollForwardHandler(
	ctx chainsync.CallbackContext,
	blockType uint,
	blockData interface{},
	tip chainsync.Tip,
) error {
	var block ledger.Block
	switch v := blockData.(type) {
	case ledger.Block:
		block = v
	case ledger.BlockHeader:
		blockSlot := v.SlotNumber()
		blockHash, _ := hex.DecodeString(v.Hash())
		fmt.Printf("block header, fetching block (%d, %x)\n", blockSlot, blockHash)
		var err error
		block, err = c.node.BlockFetch().Client.GetBlock(ouroboroscommon.NewPoint(blockSlot, blockHash))
		if err != nil {
			return err
		}
	}
	// Display block info
	switch blockType {
	case ledger.BlockTypeByronEbb:
		byronEbbBlock := block.(*ledger.ByronEpochBoundaryBlock)
		fmt.Printf(
			"era = Byron (EBB), epoch = %d, slot = %d, id = %s\n",
			byronEbbBlock.BlockHeader.ConsensusData.Epoch,
			byronEbbBlock.SlotNumber(),
			byronEbbBlock.Hash(),
		)
	case ledger.BlockTypeByronMain:
		byronBlock := block.(*ledger.ByronMainBlock)
		fmt.Printf(
			"era = Byron, epoch = %d, slot = %d, id = %s\n",
			byronBlock.BlockHeader.ConsensusData.SlotId.Epoch,
			byronBlock.SlotNumber(),
			byronBlock.Hash(),
		)
	default:
		if block == nil {
			return fmt.Errorf("block is nil")
		}
		fmt.Printf(
			"era = %s, slot = %d, block_no = %d, hash = %s, txs=%d\n",
			block.Era().Name,
			block.SlotNumber(),
			block.BlockNumber(),
			block.Hash(),
			len(block.Transactions()),
		)
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
