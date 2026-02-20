package cardano

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/keepalive"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	utxocardano "github.com/utxorpc/go-codegen/utxorpc/v1alpha/cardano"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
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

// estimated tx size with
// 5 inputs, 2 outputs (1 base + 1 enterprise), 150 char memo
// 1.5 factor covers, more inputs or longer memos
// precise tx fee will be calculated on actual tx creation
const estimatedTxSize = 620

const txTimeoutBlocks = uint64(20)

const (
	networkFeeUpdatePeriod = 100
	networkMagicMainnet    = 764824073
	networkMagicTestnet    = 42
)

type Client struct {
	logger              zerolog.Logger
	networkError        chan error
	config              config.BifrostChainConfiguration
	mtx                 sync.Mutex
	n2c                 *ouroboros.Connection
	n2n                 *ouroboros.Connection
	n2nPoint            pcommon.Point
	ttlSlots            uint64
	minFeeA             uint64
	minFeeB             uint64
	networkFee          uint64
	thorchainBridge     thorclient.ThorchainBridge
	signerCacheManager  *signercache.CacheManager
	storage             *blockscanner.BlockScannerStorageCardano
	scanner             *blockscanner.BlockScanner
	tssKeyManager       tss.ThorchainKeyManager
	asgardAddresses     []common.Address
	lastAsgard          time.Time
	globalSolvencyQueue chan types.Solvency
	globalNetworkQueue  chan common.NetworkFee
	networkMagic        uint32
	localAddress        string

	startHeight uint64

	protocol *utxocardano.PParams
	localKey ed25519.PrivateKey

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
	c.storage, err = blockscanner.NewBlockScannerStorageCardano(path, cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

	c.tssKeyManager, err = tss.NewKeySign(server, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(
		c.storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache: %w", err)
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
		ouroboros.WithKeepAlive(true),
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
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ouroboros n2c connection: %w", err)
	}

	err = c.n2c.Dial("tcp", c.config.APIHost)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ouroboros n2c connection: %w", err)
	}

	if c.config.BlockScanner.StartBlockHeight > 0 {
		slot := c.config.BlockScanner.Cardano.StartBlockSlot
		if slot == 0 {
			return nil, fmt.Errorf("start block slot is zero")
		}

		hash := c.config.BlockScanner.Cardano.StartBlockHash
		if hash == "" {
			return nil, fmt.Errorf("start block hash is undefined")
		}

		var bz []byte
		bz, err = hex.DecodeString(hash)
		if err != nil {
			return nil, fmt.Errorf("failed to decode start block hash: %w", err)
		}

		c.n2nPoint = pcommon.NewPoint(slot, bz)
	} else {
		client := c.client()
		var tip *pcommon.Point
		tip, err = client.GetChainPoint()
		c.release()
		if err != nil {
			return nil, fmt.Errorf("fail to get current tip: %w", err)
		}
		c.n2nPoint = *tip
	}

	// get slot coefficient
	client := c.client()
	var genesisConfig *localstatequery.GenesisConfigResult
	genesisConfig, err = client.GetGenesisConfig()
	c.release()
	if err != nil {
		return nil, fmt.Errorf("fail to get genesis config: %w", err)
	}

	num, ok := genesisConfig.ActiveSlotsCoeff[0].(uint64)
	if !ok {
		return nil, fmt.Errorf("fail to get active slot coeff")
	}
	den, ok := genesisConfig.ActiveSlotsCoeff[1].(uint64)
	if !ok {
		return nil, fmt.Errorf("fail to get active slot coeff")
	}

	c.ttlSlots = den / num * txTimeoutBlocks

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

func (c *Client) BroadcastTx(txOutItem types.TxOutItem, txBytes []byte) (string, error) {
	tx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction: %w", err)
	}

	err = c.n2c.LocalTxSubmission().Client.SubmitTx(
		ledger.TxTypeConway, txBytes,
	)
	if err != nil {
		return "", fmt.Errorf("failed to submit transaction bytes=%x: %w", txBytes, err)
	}

	txHash := tx.Hash().String()

	c.logger.Info().
		Str("hash", txHash).
		Uint64("amount", tx.Outputs()[0].Amount().Uint64()).
		Uint64("change", tx.Outputs()[1].Amount().Uint64()).
		Uint64("fee", tx.Fee().Uint64()).
		Msg("BroadcastTx")

	err = c.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(c.GetChain()),
		txHash,
	)
	if err != nil {
		c.logger.Err(err).
			Interface("tx_out_item", txOutItem).
			Msg("failed to mark tx out item as signed")
	}

	return txHash, nil
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

func (c *Client) getUtxosByAddress(
	address string,
	height *big.Int,
) (map[localstatequery.UtxoId]ledger.BabbageTransactionOutput, error) {
	addr, err := ledger.NewAddress(address)
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	var point pcommon.Point

	var slot uint64
	var hash []byte

	if height != nil {

		_, slot, hash, err = c.storage.GetBlockId(height.Uint64())
		if err != nil {
			c.logger.Warn().Err(err).
				Uint64("height", height.Uint64()).
				Msg("failed to get block id for height, falling back to tip")
		}

		point = pcommon.NewPoint(slot, hash)
	}

	if point.Hash == nil {
		_, slot, hash, err = c.storage.GetLastBlockId()
		if err != nil {
			return nil, err
		}
		point = pcommon.NewPoint(slot, hash)
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

	utxos := map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{}
	for id, output := range result.Results {
		var found bool
		found, err = c.storage.HasSpentUtxo(id.Hash.Bytes())
		if err != nil {
			c.logger.Warn().Err(err).Msg("failed to check for spent utxo")
			continue
		}

		if found {
			continue
		}

		utxos[id] = output
	}

	return utxos, nil
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	account := common.Account{}

	utxos, err := c.getUtxosByAddress(address, height)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return account, err
	}

	total := uint64(0)
	for _, output := range utxos {
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
	client := c.client()
	defer c.release()
	return client.GetChainBlockNo()
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(
		types.InboundCacheKey(vault, c.GetChain().String()),
	)
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(
		types.BroadcastCacheKey(vault, c.GetChain().String()),
	)
	return lastObserved, lastBroadCasted, err
}

func (c *Client) IsBlockScannerHealthy() bool {
	return c.scanner.IsHealthy()
}

func (c *Client) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {
	m, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
		return
	}
	if !m.IsOutbound() {
		return
	}
	if m.GetTxID().IsEmpty() {
		return
	}
	if err = c.signerCacheManager.SetSigned(txIn.CacheHash(c.GetChain(), m.GetTxID().String()), txIn.CacheVault(c.GetChain()), txIn.Tx); err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}

func (c *Client) SignTx(txOutItem types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	if c.signerCacheManager.HasSigned(txOutItem.CacheHash()) {
		c.logger.Info().
			Interface("tx", txOutItem).
			Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

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

	utxos, err := c.getUtxosByAddress(fromAddress.String(), nil)
	if err != nil {
		c.logger.Err(err).Msg("failed to get utxos by address")
		return nil, nil, nil, err
	}

	currentHeight, err := c.GetHeight()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get current height")
	}

	tx, err := CreateConwayTx(
		fromAddress,
		toAddress,
		coin.Amount.Uint64()/100, // 1e8 => 1e6
		c.minFeeA,
		c.minFeeB,
		txOutItem.Memo,
		utxos,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to create conway tx")
		return nil, nil, nil, err
	}

	c.logger.Info().
		Uint64("min_fee_a", c.minFeeA).
		Uint64("min_fee_b", c.minFeeB).
		Uint64("fee", tx.Body.TxFee).
		Msg("created tx")

	_, slot, _, _ := c.storage.GetLastBlockId()

	tx.Body.Ttl = slot + c.ttlSlots

	encoded, err := cbor.Encode(tx.Body)
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

	txBytes, err := cbor.Encode(tx)
	if err != nil {
		c.logger.Err(err).Msg("failed to encode transaction")
		return nil, nil, nil, err
	}

	var spentUtxos [][]byte
	for _, input := range tx.Inputs() {
		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, input.Index())
		spentUtxos = append(spentUtxos, append(input.Id().Bytes(), index...))
	}
	err = c.storage.SetSpentUtxos(spentUtxos, currentHeight)
	if err != nil {
		c.logger.Err(err).Msg("failed to set spent utxos")
		return nil, nil, nil, err
	}

	txIn := types.NewTxInItem(
		currentHeight,
		tx.Hash().String(),
		txOutItem.Memo,
		fromAddress.String(),
		txOutItem.ToAddress.String(),
		txOutItem.Coins,
		[]common.Coin{common.NewCoin(
			common.ADAAsset,
			cosmos.NewUint(tx.Body.TxFee*100),
		)},
		txOutItem.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	return txBytes, nil, txIn, nil
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
	c.logger.Info().
		Uint64("slot", c.n2nPoint.Slot).
		Hex("hash", c.n2nPoint.Hash).
		Msg("starting ChainSync")

	err := c.n2n.ChainSync().Client.Sync([]pcommon.Point{c.n2nPoint})
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
	_, slot, hash, err := c.storage.GetBlockId(uint64(fetchHeight))
	if err != nil {
		c.logger.Err(err).Msgf("fail to find block id for height=%d", fetchHeight)
		return types.TxIn{}, err
	}
	block, err := c.n2n.BlockFetch().Client.GetBlock(pcommon.NewPoint(slot, hash))
	if err != nil {
		c.logger.Err(err).Msg("fail to fetch block")
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

		memo := ExtractMemo(tx.Metadata(), 0)

		amount := output.Amount().Uint64() * 100
		coins := common.Coins{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(amount),
			Decimals: 6,
		}}

		fee := tx.Fee().Uint64() * 100
		gas := common.Gas{{
			Asset:    common.ADAAsset,
			Amount:   sdkmath.NewUint(fee),
			Decimals: 6,
		}}

		txIns = append(txIns, &types.TxInItem{
			Tx:          tx.Hash().String(),
			BlockHeight: fetchHeight,
			Memo:        memo,
			Sender:      fromAddress.String(),
			To:          toAddress.String(),
			Coins:       coins,
			Gas:         gas,
		})
	}

	if err = c.updateNetworkFee(fetchHeight); err != nil {
		return types.TxIn{}, err
	}

	err = c.storage.PruneBlockIds(uint64(fetchHeight))
	if err != nil {
		c.logger.Err(err).Msg("fail to prune block ids")
	}

	err = c.storage.PruneSpentUtxos(uint64(fetchHeight) - txTimeoutBlocks)
	if err != nil {
		c.logger.Err(err).Msg("fail to prune spent utxos")
	}

	return types.TxIn{
		Chain:    c.config.ChainID,
		TxArray:  txIns,
		Filtered: false,
		MemPool:  false,
	}, nil
}

func (c *Client) updateNetworkFee(height int64) error {
	if height <= 0 {
		return nil
	}

	if height%networkFeeUpdatePeriod != 0 && c.minFeeA > 0 && c.minFeeB > 0 {
		return nil
	}

	// fetch network protocol parameters, needed for tx signing
	client := c.client()
	protocolParams, err := client.GetCurrentProtocolParams()
	c.release()
	if err != nil {
		return fmt.Errorf("failed to get protocol parameters: %w", err)
	}

	params, ok := protocolParams.(*conway.ConwayProtocolParameters)
	if !ok {
		return fmt.Errorf("failed to get protocol parameters: %w, err")
	}

	minFeeA := uint64(params.MinFeeA)
	minFeeB := uint64(params.MinFeeB)

	if minFeeA == c.minFeeA && minFeeB == c.minFeeB {
		return nil
	}

	c.minFeeA = minFeeA
	c.minFeeB = minFeeB

	feeRate := minFeeA*estimatedTxSize + minFeeB + 1

	resolution := uint64(c.config.BlockScanner.GasPriceResolution)
	roundedFeeRate := ((feeRate + (resolution / 2)) / resolution) * resolution

	if roundedFeeRate == 0 {
		return fmt.Errorf("rounded fee rate is zero")
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
		err := c.storage.SetBlockId(h.BlockNumber(), h.SlotNumber(), h.Hash().Bytes())
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
