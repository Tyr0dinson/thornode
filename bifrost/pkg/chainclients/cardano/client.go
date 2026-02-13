package cardano

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/keepalive"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	leveldbutil "github.com/syndtr/goleveldb/leveldb/util"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

var (
	_ shared.ChainClient               = &Client{}
	_ blockscanner.BlockScannerFetcher = &Client{}
	_ runners.SolvencyCheckProvider    = &Client{}
	// epoch 542 started on Feb 28, 2025 3:44:51 PM and ends at Mar 5, 2025 3:44:51 PM
	epoch542StartBlock = pcommon.Tip{
		Point: pcommon.Point{
			Slot: 149563179,
			// hash: 0xdad58ad00057569d7e32f2939ec7fee828971823b7235a748d925868c5836c30
			Hash: []byte{218, 213, 138, 208, 0, 87, 86, 157, 126, 50, 242, 147, 158, 199, 254, 232, 40, 151, 24, 35, 183, 35, 90, 116, 141, 146, 88, 104, 197, 131, 108, 48},
		},
		BlockNumber: 11561640,
	}
)

const (
	networkFeeUpdatePeriod = 100
	networkMagicMainnet    = 764824073
	networkMagicTestnet    = 42
)

type Client struct {
	logger              zerolog.Logger
	networkError        chan error
	config              config.BifrostChainConfiguration
	n2n                 *ouroboros.Connection
	n2c                 *ouroboros.Connection
	mtx                 sync.Mutex
	thorchainBridge     thorclient.ThorchainBridge
	storage             *blockscanner.BlockScannerStorage
	scanner             *blockscanner.BlockScanner
	tssKeyManager       tss.ThorchainKeyManager
	asgardAddresses     []common.Address
	lastAsgard          time.Time
	globalSolvencyQueue chan types.Solvency
	globalNetworkQueue  chan common.NetworkFee
	networkMagic        uint32
	networkFee          uint64
	localAddress        string

	startHeight uint64

	protocol *utxocardano.PParams
	localKey ed25519.PrivateKey

	txSizes     []uint64
	txSizesLock sync.Mutex

	signLock   sync.Mutex
	vaultLocks map[string]*sync.Mutex
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

	if common.CurrentChainNetwork == common.MockNet {
		c.networkMagic = networkMagicTestnet
	} else {
		c.networkMagic = networkMagicMainnet
	}

	_, err := tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}
	privKey, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get local testing key: %w", err)
	}
	c.localKey = privKey.Key

	address, err := AddressFromBytes(privKey.PubKey().Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}
	c.localAddress = address.String()

	var path string // if not set later, will in memory storage
	if len(cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", cfg.BlockScanner.DBPath, cfg.BlockScanner.ChainID)
	}
	c.storage, err = blockscanner.NewBlockScannerStorage(path, cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

	c.tssKeyManager, err = tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}

	// create ouroboros connection
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
	c.n2n, err = ouroboros.NewConnection(
		ouroboros.WithPeerSharing(true),
		ouroboros.WithNetworkMagic(c.networkMagic),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithErrorChan(c.networkError),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithKeepAliveConfig(keepalive.NewConfig(
			keepalive.WithPeriod(10*time.Second),
		)),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollBackwardFunc(c.chainSyncRollBackwardHandler),
			chainsync.WithRollForwardFunc(c.chainSyncRollForwardHandler),
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create ouroboros n2n connection: %w", err)
	}

	err = c.n2n.Dial("tcp", c.config.RPCHost)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ouroboros n2n connection: %w", err)
	}

	// create n2c connection
	c.n2c, err = ouroboros.NewConnection(
		ouroboros.WithErrorChan(c.networkError),
		ouroboros.WithLogger(networkLogger),
		ouroboros.WithNetworkMagic(c.networkMagic),
		ouroboros.WithKeepAlive(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2c connection: %w", err)
	}

	err = c.n2c.Dial("tcp", c.config.APIHost)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ouroboros n2c connection: %w", err)
	}

	c.startHeight, _, _ = c.getLastSeenBlock()
	if c.networkMagic == networkMagicMainnet {
		if cfg.BlockScanner.StartBlockHeight > 0 {
			slot, _, _ := c.getBlockId(uint64(cfg.BlockScanner.StartBlockHeight))
			if slot == 0 {
				if cfg.BlockScanner.StartBlockHeight < int64(epoch542StartBlock.BlockNumber) {
					return nil, fmt.Errorf("start block height %d is before hardcoded kepoch 542 start block %d",
						cfg.BlockScanner.StartBlockHeight, epoch542StartBlock.BlockNumber)
				}
				// assume we're missing data and start from an arbitrary epoch.. this could take awhile.
				err := c.setBlockId(epoch542StartBlock.BlockNumber, epoch542StartBlock.Point.Slot, epoch542StartBlock.Point.Hash)
				if err != nil {
					return nil, fmt.Errorf("fail to set epoch block id: %w", err)
				}
				c.startHeight = epoch542StartBlock.BlockNumber
			} else {
				c.startHeight = uint64(cfg.BlockScanner.StartBlockHeight)
			}
		} else if c.startHeight == 0 {
			// also start from epoch 542 if a clean start
			err := c.setBlockId(epoch542StartBlock.BlockNumber, epoch542StartBlock.Point.Slot, epoch542StartBlock.Point.Hash)
			if err != nil {
				return nil, fmt.Errorf("fail to set epoch block id: %w", err)
			}
			c.startHeight = epoch542StartBlock.BlockNumber
		}
	}

	c.logger.Info().Msgf("start height: %d", c.startHeight)
	c.logger.Info().Msgf("magic: %d", c.networkMagic)

	// create block scanner
	// scanner, err := NewScanner(cfg.BlockScanner, thorchainBridge, c.n2c, c.n2n)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create cardano block scanner: %w", err)
	// }

	c.logger.Info().Msgf("creating blockscanner")

	c.scanner, err = blockscanner.NewBlockScanner(
		cfg.BlockScanner,
		c.storage,
		metrics,
		thorchainBridge,
		// scanner,
		c,
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create block scanner: %w", err)
	}

	c.logger.Info().Msgf("ADA client created")

	return c, nil
}

func (c *Client) BroadcastTx(_ types.TxOutItem, txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}

	client := c.client()
	era, err := client.GetCurrentEra()
	c.release()
	if err != nil {
		return "", fmt.Errorf("failed to get current era: %w", err)
	}

	err = c.n2c.LocalTxSubmission().Client.SubmitTx(uint16(era), txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	c.txSizesLock.Lock()
	defer c.txSizesLock.Unlock()
	c.txSizes = append(c.txSizes, uint64(len(txBytes)))
	if len(c.txSizes) > 100 {
		c.txSizes = c.txSizes[1:]
	}

	return tx.Hash().String(), nil
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	return true
}

func (c *Client) GetAccount(pubkey common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pubkey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).
			Str("pub_key", pubkey.String()).
			Msg("failed to get address")
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) getUtxosByAddress(address string, height *big.Int) (*localstatequery.UTxOsResult, error) {
	addr, err := ledger.NewAddress(address)
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	var point pcommon.Point

	if height != nil {
		var slot uint64
		var hash []byte

		slot, hash, err := c.getBlockId(height.Uint64())
		if err != nil {
			c.logger.Warn().Err(err).
				Uint64("height", height.Uint64()).
				Msg("failed to get block id for height, falling back to tip")
		}

		point = pcommon.NewPoint(slot, hash)
	}

	if point.Hash == nil {
		var tip *chainsync.Tip
		tip, err = c.n2n.ChainSync().Client.GetCurrentTip()
		if err != nil {
			c.logger.Err(err).Msg("failed to get current tip")
			return nil, err
		}

		point = tip.Point
	}

	client := c.client()
	err = client.Acquire(&point)
	if err != nil {
		c.release()
		return nil, fmt.Errorf("failed to acquire point: %w", err)
	}

	result, err := client.GetUTxOByAddress([]ledger.Address{addr})
	c.release()
	if err != nil {
		return nil, fmt.Errorf("failed to get UTxO by address")
	}

	return result, nil
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	account := common.Account{}

	result, err := c.getUtxosByAddress(address, height)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return account, err
	}

	total := uint64(0)
	for _, output := range result.Results {
		total += output.Amount().Uint64()
	}

	account.Coins = common.Coins{
		common.NewCoin(common.ADAAsset, sdkmath.NewUint(total)),
	}

	return account, nil
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

// RollbackBlockScanner rolls back the block scanner to the last observed block
func (c *Client) RollbackBlockScanner() error {
	return c.scanner.RollbackToLastObserved()
}

func (c *Client) GetChain() common.Chain {
	return c.config.ChainID
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.config
}

// TODO: check
func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	//  n2c call may be slow, may also query leveldb if chain-sync is current
	client := c.client()
	tip, err := client.GetChainBlockNo()
	c.release()
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
}

func (c *Client) SignTx(txOutItem types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	coin := txOutItem.Coins.GetCoin(common.ADAAsset)

	if coin.IsEmpty() {
		err := fmt.Errorf("no ADA coins found")
		c.logger.Err(err).Interface("coins", txOutItem.Coins).Msg("")
		return nil, nil, nil, err
	}

	fromAddress, err := ledger.NewAddress(c.GetAddress(txOutItem.VaultPubKeyEddsa))
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
		return nil, nil, nil, err
	}

	toAddress, err := ledger.NewAddress(txOutItem.ToAddress.String())
	if err != nil {
		c.logger.Err(err).Msg("failed to get address")
	}

	c.logger.Info().
		Str("from", fromAddress.String()).
		Str("to", toAddress.String()).
		Msg("sign tx")

	result, err := c.getUtxosByAddress(fromAddress.String(), nil)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return nil, nil, nil, err
	}

	var inputs []shelley.ShelleyTransactionInput

	amount := coin.Amount.Uint64() / 100 // 1e8 => 1e6
	target := amount + c.networkFee
	total := uint64(0)

	for id, output := range result.Results {
		total += output.Amount().Uint64()

		inputs = append(inputs, shelley.NewShelleyTransactionInput(
			id.Hash.String(),
			id.Idx,
		))

		if total >= target {
			break
		}
	}

	if total < target {
		err = fmt.Errorf("insufficient funds")
		c.logger.Err(err).
			Uint64("have", amount).
			Uint64("need", target).
			Msg("failed to get utxos")
		return nil, nil, nil, err
	}

	outputs := []babbage.BabbageTransactionOutput{
		{
			OutputAddress: toAddress,
			OutputAmount: mary.MaryTransactionOutputValue{
				Amount: amount,
			},
		},
	}

	change := total - target

	if change > common.ADAChain.DustThreshold().Uint64() {
		outputs = append(outputs, babbage.BabbageTransactionOutput{
			OutputAddress: fromAddress,
			OutputAmount: mary.MaryTransactionOutputValue{
				Amount: change,
			},
		})
	}

	tx := &conway.ConwayTransaction{}
	tx.Body.TxInputs = conway.NewConwayTransactionInputSet(inputs)
	tx.Body.TxOutputs = outputs
	tx.Body.TxFee = c.networkFee
	tx.Body.Ttl = 50_000_000 // TODO: find reasonable value
	tx.TxIsValid = true

	encoded, err := cbor.Encode(&tx.Body)
	if err != nil {
		c.logger.Err(err).Msg("failed to encode transaction")
		return nil, nil, nil, err
	}

	hash := lcommon.Blake2b256Hash(encoded)

	signature, err := c.sign(hash[:], txOutItem)
	if err != nil {
		c.logger.Err(err).Msg("failed to sign transaction")
		return nil, nil, nil, err
	}

	var pubKey cryptotypes.PubKey
	pubKey, err = cosmos.GetPubKeyFromBech32(
		cosmos.Bech32PubKeyTypeAccPub,
		txOutItem.VaultPubKeyEddsa.String(),
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to get public key")
		return nil, nil, nil, err
	}

	// verify
	ok := ed25519.Verify(pubKey.Bytes(), hash[:], signature)
	if !ok {
		return nil, nil, nil, fmt.Errorf("failed to verify signature: %w", err)
	}

	tx.WitnessSet.VkeyWitnesses = cbor.NewSetType(
		[]lcommon.VkeyWitness{
			{
				Vkey:      pubKey.Bytes(),
				Signature: signature,
			},
		},
		false,
	)

	signed, err := cbor.Encode(tx)
	if err != nil {
		c.logger.Err(err).Msg("failed to encode transaction")
		return nil, nil, nil, err
	}

	return signed, nil, nil, nil
}

func (c *Client) sign(
	data []byte,
	txOutItem types.TxOutItem,
) ([]byte, error) {
	var err error
	var signature []byte

	vaultAddress := c.GetAddress(txOutItem.VaultPubKeyEddsa)
	if c.localAddress == vaultAddress {
		signature = ed25519.Sign(c.localKey, data)
	} else {
		signature, _, err = c.tssKeyManager.RemoteSign(
			data,
			common.SigningAlgoEd25519,
			txOutItem.VaultPubKeyEddsa.String(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to sign remote transaction: %w", err)
		}
	}

	return signature, nil
}

func (c *Client) Start(
	globalTxsQueue chan types.TxIn,
	_ chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.logger.Info().Msg("Start ADA client")
	var point pcommon.Point
	if c.startHeight == 0 {
		point = pcommon.NewPointOrigin()
	} else {
		slot, hash, err := c.getBlockId(c.startHeight)
		if err != nil {
			c.logger.Err(err).Msgf("client start failed, unable to find block id for height=%d", c.startHeight)
			return
		}
		point = pcommon.NewPoint(slot, hash)
	}
	c.logger.Info().Msgf("starting ChainSync: %x, %d", point.Hash, point.Slot)
	err := c.n2n.ChainSync().Client.Sync([]pcommon.Point{point})
	if err != nil {
		c.logger.Err(err).Msg("failed to start chain-sync client")
		return
	}

	c.globalSolvencyQueue = globalSolvencyQueue
	c.globalNetworkQueue = globalNetworkFeeQueue
	c.scanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.logger.Info().Msg("started cardano client")
}

func (c *Client) Stop() {
	if err := c.n2n.ChainSync().Client.Stop(); err != nil {
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
	block, err := c.n2n.BlockFetch().Client.GetBlock(pcommon.NewPoint(slot, hash))
	if err != nil {
		return types.TxIn{}, err
	}

	var txIns []*types.TxInItem
	for _, tx := range block.Transactions() {
		c.logger.Info().Str("hash", tx.Hash().String()).Msg("processing transaction")
		if !tx.IsValid() {
			continue
		}

		// try to get sender from witness
		// if we make certain assumptions about tx origins we can skip n2c protocols and utxo queries
		if len(tx.Witnesses().Vkey()) != 1 {
			continue
		}

		fromAddress, err := AddressFromBytes(tx.Witnesses().Vkey()[0].Vkey)
		if err != nil {
			c.logger.Err(err).Msg("fail to get sender address")
			continue
		}

		// isSenderAsgard := c.isAsgardAddress(senderAddr.String())

		// expectation is one output address per filtered tx
		// see: https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/utxo/client_internal.go#L724
		// which collapses multiple VOUTs into a single address based on filter criteria
		if len(tx.Outputs()) == 0 {
			continue
		}
		output := tx.Outputs()[0]

		toAddress := output.Address()

		fmt.Println(fromAddress.String(), "=>", toAddress.String())
		// if !isSenderAsgard && !c.isAsgardAddress(addr.String()) {
		// 	// skip txs that don't involve asgard addresses
		// 	continue
		// }
		if fromAddress.String() == toAddress.String() {
			// skip txs that are sent to the sender, i.e. for utxo change
			continue
		}

		amount := output.Amount().Uint64() * 100

		memo := ExtractMemo(tx.Metadata(), 0)
		fmt.Println("memo:", memo)

		txIns = append(txIns, &types.TxInItem{
			Tx:          tx.Hash().String(),
			BlockHeight: fetchHeight,
			Memo:        memo,
			Sender:      fromAddress.String(),
			To:          toAddress.String(),
			Coins:       common.Coins{common.NewCoin(common.ADAAsset, sdkmath.NewUint(amount))},
			Gas:         common.Gas{common.NewCoin(common.ADAAsset, sdkmath.NewUintFromBigInt(tx.Fee()))},
		})
	}

	if err = c.updateNetworkFee(fetchHeight); err != nil {
		return types.TxIn{}, err
	}

	return types.TxIn{
		Chain:    c.config.ChainID,
		TxArray:  txIns,
		Filtered: false,
		MemPool:  false,
	}, nil
}

func (c *Client) getMeanTxSize() uint64 {
	c.txSizesLock.Lock()
	defer c.txSizesLock.Unlock()
	meanSize := uint64(568)
	if len(c.txSizes) > 0 {
		meanSize = 0
		for _, size := range c.txSizes {
			meanSize += size
		}
		meanSize /= uint64(len(c.txSizes))
	}
	return meanSize
}

func (c *Client) updateNetworkFee(height int64) error {
	if height != 0 && height%networkFeeUpdatePeriod != 0 {
		return nil
	}

	// fetch network protocol parameters, needed for tx signing
	client := c.client()
	protocolParams, err := client.GetCurrentProtocolParams()
	c.release()
	if err != nil {
		return fmt.Errorf("fail to get protocol parameters: %w", err)
	}
	c.protocol, err = protocolParams.Utxorpc()
	if err != nil {
		return fmt.Errorf("fail to get parameters: %w", err)
	}

	minFeeConstant := c.protocol.MinFeeConstant.GetInt()
	minFeeCoefficient := c.protocol.MinFeeCoefficient.GetInt()

	resolution := uint64(c.config.BlockScanner.GasPriceResolution)

	feeRate := uint64(minFeeConstant+minFeeCoefficient) * c.getMeanTxSize()

	roundedFeeRate := ((feeRate + (resolution / 2)) / resolution) * resolution

	// TODO: check if min fee rate is needed

	if roundedFeeRate == 0 {
		return fmt.Errorf("rounded fee rate is zero")
	}

	if c.networkFee == roundedFeeRate {
		return nil
	}

	c.networkFee = roundedFeeRate

	c.globalNetworkQueue <- common.NetworkFee{
		Chain:           common.ADAChain,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: roundedFeeRate,
	}

	c.logger.Info().
		Uint64("fee", roundedFeeRate).
		Int64("height", height).
		Msg("sent network fee to THORChain")

	return nil
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
		err := c.setBlockId(h.BlockNumber(), h.SlotNumber(), h.Hash().Bytes())
		c.logger.Info().
			Uint64("block_number", h.BlockNumber()).
			Uint64("slot_number", h.SlotNumber()).
			Str("hash", h.Hash().String()).
			Msg("roll forward")
		if err != nil {
			c.logger.Err(err).Msg("fail to set block id")
		}
	}
	return nil
}

func (c *Client) chainSyncRollBackwardHandler(
	ctx chainsync.CallbackContext,
	point pcommon.Point,
	tip chainsync.Tip,
) error {
	c.logger.Info().Msgf("roll backward: point = (%d, %x), tip = (%d, %x)",
		point.Slot, point.Hash, tip.Point.Slot, tip.Point.Hash,
	)
	return nil
}

func (c *Client) ShouldReportSolvency(height int64) bool {
	return height%c.config.SolvencyBlocks == 0
}

func (c *Client) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}
	if !c.IsBlockScannerHealthy() && height == c.scanner.PreviousHeight()+1 {
		return nil
	}
	asgardVaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgard vaults: %w", err)
	}
	_, fee := c.GetNetworkFee()
	gasFee := sdkmath.NewUint(fee)
	reportMsgs := make([]types.Solvency, 0, len(asgardVaults))
	solventMsgs := make([]types.Solvency, 0, len(asgardVaults))
	for i := range asgardVaults {
		acct, err := c.GetAccount(asgardVaults[i].PubKey, big.NewInt(height))
		if err != nil {
			return fmt.Errorf("fail to get account: %w", err)
		}
		msg := types.Solvency{
			Height: height,
			Chain:  c.config.ChainID,
			PubKey: asgardVaults[i].PubKey,
			Coins:  acct.Coins,
		}
		if runners.IsVaultSolvent(acct, asgardVaults[i], gasFee) {
			solventMsgs = append(solventMsgs, msg)
		} else {
			reportMsgs = append(reportMsgs, msg)
		}
	}
	// only report solvency when the block scanner is unhealthy and all vaults are solvent
	// otherwise report insolvency
	solvent := false
	if !c.IsBlockScannerHealthy() && len(solventMsgs) == len(asgardVaults) {
		reportMsgs = solventMsgs
		solvent = true
	}

	for i := range reportMsgs {
		c.logger.Info().
			Stringer("asgard", reportMsgs[i].PubKey).
			Interface("coins", reportMsgs[i].Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")

		// send solvency to thorchain via global queue consumed by the observer
		select {
		case c.globalSolvencyQueue <- reportMsgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msgf("fail to send solvency info to THORChain, timeout")
		}
	}
	return nil

}

func (c *Client) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, c.networkFee
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

func (c *Client) getAsgardAddress() ([]common.Address, error) {
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && c.asgardAddresses != nil {
		return c.asgardAddresses, nil
	}
	newAddresses, err := utxo.GetAsgardAddress(c.config.ChainID, c.thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}
	if len(newAddresses) > 0 { // ensure we don't overwrite with empty list
		c.asgardAddresses = newAddresses
	}
	c.lastAsgard = time.Now()
	return c.asgardAddresses, nil
}

func (c *Client) isAsgardAddress(addressToCheck string) bool {
	asgards, err := c.getAsgardAddress()
	if err != nil {
		c.logger.Err(err).Msg("fail to get asgard addresses")
		return false
	}
	for _, addr := range asgards {
		if strings.EqualFold(addr.String(), addressToCheck) {
			return true
		}
	}
	return false
}

func (c *Client) release() {
	err := c.n2c.LocalStateQuery().Client.Release()
	if err != nil {
		c.logger.Err(err).Msg("failed to release client")
	}
	c.mtx.Unlock()
}

func (c *Client) client() *localstatequery.Client {
	c.mtx.Lock()
	return c.n2c.LocalStateQuery().Client
}
