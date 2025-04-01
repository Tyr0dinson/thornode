package cardano

import (
	"bytes"
	"crypto/ed25519"
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
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
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
	_ runners.SolvencyCheckProvider    = &Client{}
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

const (
	networkFeeUpdatePeriod = 100
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
	protocol    *utxocardano.PParams

	localTestingKey ed25519.PrivateKey
	txSizes         []uint64
	txSizesLock     sync.Mutex

	signLock   sync.Mutex
	vaultLocks map[string]*sync.Mutex
}

func getNetworkMagic(cfg config.BifrostChainConfiguration) (uint32, error) {
	if cfg.ChainNetwork == "" {
		return 0, errors.New("chain network not set")
	}
	i, err := strconv.ParseUint(cfg.ChainNetwork, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("error parsing chain network as uint, %w", err)
	}
	return uint32(i), nil
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
		vaultLocks:      make(map[string]*sync.Mutex),
	}

	_, err := tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}
	c.localTestingKey, err = thorKeys.GetSingleValidatorEd25519PrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get local testing key: %w", err)
	}
	vaultAddr, err := c.getVaultAddress()
	if err != nil {
		return nil, fmt.Errorf("fail to get vault address: %w", err)
	}
	pubKey := c.localTestingKey.Public().(ed25519.PublicKey)
	pubBech32, err := cardano.NewThorPubKeyBech32(pubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get thor pub key bech32: %w", err)
	}
	fmt.Printf("cardano client has thorKeys:\n privKey: %x\n  pubKey: %x\n vaultAddress: %s\n thorpub: %s\n",
		c.localTestingKey,
		pubKey,
		vaultAddr.String(),
		pubBech32,
	)

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
	networkMagic, err := getNetworkMagic(cfg)
	if err != nil {
		return nil, fmt.Errorf("fail to get network magic: %w", err)
	}
	c.networkError = make(chan error)
	go func() {
		for {
			err := <-c.networkError
			c.logger.Err(err).Msg("ouroboros network error")
			time.Sleep(time.Millisecond * 500)
		}
	}()
	networkLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// create n2n connection
	n2nConn, err := c.dialTCP(c.config.RPCHost)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2n host: %w", err)
	}
	c.nodeConn, err = ouroboros.NewConnection(
		ouroboros.WithPeerSharing(true),
		ouroboros.WithConnection(n2nConn),
		ouroboros.WithNetworkMagic(networkMagic),
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

	// create n2c connection
	n2cConn, err := c.dialTCP(cfg.CardanoN2CAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to dial n2c host: %w", err)
	}
	c.clientConn, err = ouroboros.NewConnection(
		ouroboros.WithConnection(n2cConn),
		ouroboros.WithErrorChan(c.networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetworkMagic(networkMagic),
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

	if err := c.updateNetworkFee(0); err != nil {
		return nil, fmt.Errorf("fail to update network fee: %w", err)
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

func (c *Client) BroadcastTx(_ types.TxOutItem, txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}
	era, err := c.clientConn.LocalStateQuery().Client.GetCurrentEra()
	if err != nil {
		return "", fmt.Errorf("failed to get current era: %w", err)
	}
	err = c.clientConn.LocalTxSubmission().Client.SubmitTx(uint16(era), txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	c.txSizesLock.Lock()
	defer c.txSizesLock.Unlock()
	c.txSizes = append(c.txSizes, uint64(len(txBytes)))
	if len(c.txSizes) > 100 {
		c.txSizes = c.txSizes[1:]
	}

	return tx.Hash(), nil
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	return true
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
	// TODO
	// What to do here?
}

func (c *Client) SignTx(tx types.TxOutItem, height int64) (signedBz, checkpoint []byte, txIn *types.TxInItem, err error) {
	vaultAddr, err := c.getVaultAddress()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get vault address: %w", err)
	}
	txVaultAddr, err := tx.VaultPubKey.GetAddress(c.GetChain())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get vault pubkey address: %w", err)
	}

	c.logger.Info().Str("vault", vaultAddr.String()).Str("tx-vault", txVaultAddr.String()).Msg("sign tx")

	if txVaultAddr.String() != vaultAddr.String() {
		return nil, nil, nil, fmt.Errorf("tx-vault pubkey address %s does not match vault address %s",
			txVaultAddr.String(), vaultAddr.String())
	}

	toAddr, err := cardano.NewAddressFromBech32(tx.ToAddress.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to parse to recipient address: %w", err)
	}
	if len(tx.Coins) != 1 {
		return nil, nil, nil, fmt.Errorf("expected 1 coin, got %d", len(tx.Coins))
	}

	vaultLock := c.getVaultLock(vaultAddr.String())
	vaultLock.Lock()
	defer vaultLock.Unlock()

	txBuilder := NewTxBuilder(c.protocol, []ed25519.PrivateKey{c.localTestingKey})

	// get block
	// blockSlot, blockHash, err := c.getBlockId(uint64(height))
	// if err != nil {
	// 	return nil, nil, nil, fmt.Errorf("fail to get block id for height %d: %w", height, err)
	// }
	// queryClient := c.clientConn.LocalStateQuery().Client
	// if err := queryClient.Acquire(&ouroboroscommon.Point{Slot: blockSlot, Hash: blockHash}); err != nil {
	// 	return nil, nil, nil, fmt.Errorf("fail to acquire point slot=%d hash=%x: %w", blockSlot, blockHash, err)
	// }
	queryClient := c.clientConn.LocalStateQuery().Client
	if err := queryClient.AcquireVolatileTip(); err != nil {
		return nil, nil, nil, fmt.Errorf("fail to acquire tip: %w", err)
	}
	defer func() {
		if err := queryClient.Release(); err != nil {
			c.logger.Err(err).Msg("fail to release point")
		}
	}()

	ledgerAddr, err := ledger.NewAddress(vaultAddr.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to create ledger address: %w", err)
	}
	utxoResponse, err := queryClient.GetUTxOByAddress([]ledger.Address{ledgerAddr})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get utxo by address: %w", err)
	}
	var (
		utxoSum      uint64
		targetAmount = tx.Coins[0].Amount.Uint64()
	)
	for txId, utxo := range utxoResponse.Results {
		utxoSum += utxo.Amount()
		txBuilder.AddInputs(TxInput{
			TxHash: txId.Hash[:],
			Index:  uint16(txId.Idx),
			Amount: utxo.Amount(),
		})
		utxoSum += utxo.Amount()
		if utxoSum >= targetAmount {
			break
		}
	}
	if utxoSum < targetAmount {
		return nil, nil, nil, fmt.Errorf("insufficient funds: need %d, have %d", targetAmount, utxoSum)
	}

	txBuilder.AddOutputs(TxOutput{
		Address: toAddr,
		Amount:  targetAmount,
	})
	if err = txBuilder.SetMemo(tx.Memo); err != nil {
		return nil, nil, nil, fmt.Errorf("fail to set memo: %w", err)
	}
	if err := txBuilder.AddChangeIfNeeded(vaultAddr); err != nil {
		return nil, nil, nil, fmt.Errorf("fail to add change: %w", err)
	}
	signedTx, err := txBuilder.Build()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to build tx: %w", err)
	}
	signedBz, err = signedTx.Bytes()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to marshal tx: %w", err)
	}

	return signedBz, nil, nil, nil
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
		senderAddr, err := c.getAddressFromPubkey(tx.Witnesses().Vkey()[0].Vkey)
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
			addr := txOut.Address()
			if !isSenderAsgard && !c.isAsgardAddress(addr.Bytes()) {
				// skip txs that don't involve asgard addresses
				continue
			}
			if addr.String() == senderAddr.String() {
				continue
			}
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

	if err := c.updateNetworkFee(fetchHeight); err != nil {
		return types.TxIn{}, err
	}

	return types.TxIn{
		Count:    strconv.Itoa(len(txIns)),
		Chain:    c.config.ChainID,
		TxArray:  txIns,
		Filtered: false,
		MemPool:  false,
	}, nil
}

func (c *Client) updateNetworkFee(height int64) error {
	if height != 0 && height%networkFeeUpdatePeriod != 0 {
		return nil
	}
	c.txSizesLock.Lock()
	meanSize := uint64(568)
	if len(c.txSizes) > 0 {
		meanSize = 0
		for _, size := range c.txSizes {
			meanSize += size
		}
		meanSize /= uint64(len(c.txSizes))
	}
	c.txSizesLock.Unlock()

	// fetch network protocol parameters, needed for tx signing
	protocolParams, err := c.clientConn.LocalStateQuery().Client.GetCurrentProtocolParams()
	if err != nil {
		return fmt.Errorf("fail to get protocol parameters: %w", err)
	}
	c.protocol = protocolParams.Utxorpc()
	_, err = c.thorchainBridge.PostNetworkFee(height, common.ADAChain,
		1,
		c.protocol.MinFeeConstant+c.protocol.MinFeeCoefficient*meanSize,
	)
	fmt.Printf("cardano updated network fee: %d\n", c.protocol.MinFeeConstant+c.protocol.MinFeeCoefficient*meanSize)
	return err
}

func (c *Client) getVaultAddress() (cardano.Address, error) {
	return c.getAddressFromPubkey(c.localTestingKey.Public().(ed25519.PublicKey))
}

func (c *Client) getAddressFromPubkey(pub []byte) (cardano.Address, error) {
	networkMagic, err := getNetworkMagic(c.config)
	if err != nil {
		return nil, fmt.Errorf("fail to get network magic: %w", err)
	}
	switch networkMagic {
	case 764824073:
		return cardano.PaymentOnlyMainnetAddressFromPubkey(pub)
	case 42:
		return cardano.PaymentOnlyTestnetAddressFromPubkey(pub)
	default:
	}
	return nil, fmt.Errorf("unsupported network magic: %d", networkMagic)
}

func (c *Client) isAsgardAddress(addr []byte) bool {
	// TODO
	// ref impl: https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/utxo/client_internal.go#L49
	//

	// this a temporary hack until eddsa is live
	vaultAddress, err := c.getVaultAddress()
	if err != nil {
		c.logger.Err(err).Msg("fail to get vault address")
		return false
	}
	return bytes.Equal(addr, vaultAddress)
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

func (c *Client) ShouldReportSolvency(height int64) bool {
	panic("unimplemented")
}

func (c *Client) ReportSolvency(height int64) error {
	panic("unimplemented")
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

func (c *Client) getVaultLock(vaultPubKey string) *sync.Mutex {
	c.signLock.Lock()
	defer c.signLock.Unlock()
	lock, ok := c.vaultLocks[vaultPubKey]
	if !ok {
		lock = &sync.Mutex{}
		c.vaultLocks[vaultPubKey] = lock
	}
	return lock
}
