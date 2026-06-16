package ton

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/math"
	"github.com/xssnick/tonutils-go/liteclient"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	tcmetrics "gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tctss "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	tcconfig "gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	tcmemo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

const (
	ConfirmationBlocks int64 = 1
	TransactionTimeout       = 5 * time.Minute
)

type TonClient struct {
	logger              zerolog.Logger
	config              tcconfig.BifrostChainConfiguration
	localPubKey         string
	k                   *cosmoscryptoed25519.PrivKey
	blockScanner        *blockscanner.BlockScanner
	storage             *blockscanner.BlockScannerStorage
	signerCacheManager  *signercache.CacheManager
	tssKeyManager       *tss.KeySign
	tonScanner          *TonBlockScanner
	api                 ton.APIClientWrapped
	ctx                 context.Context
	bridge              thorclient.ThorchainBridge
	globalSolvencyQueue chan types.Solvency
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
}

// NewTonClient creates a new instance of a TON chain client
func NewTonClient(
	thorKeys *thorclient.Keys,
	config tcconfig.BifrostChainConfiguration,
	server *tctss.TssServer,
	bridge thorclient.ThorchainBridge,
	metrics *tcmetrics.Metrics,
) (*TonClient, error) {
	var err error

	logger := log.With().Str("module", config.ChainID.String()).Logger()

	c := TonClient{
		logger:   logger,
		config:   config,
		bridge:   bridge,
		ctx:      context.Background(),
		wg:       &sync.WaitGroup{},
		stopchan: make(chan struct{}),
	}

	if c.config.BlockScanner.PrefetchBlocks != 0 {
		return nil, fmt.Errorf("prefetch blocks not supported")
	}

	pool := liteclient.NewConnectionPool()

	switch c.config.RPCHost {
	case "":
		return nil, fmt.Errorf("no client config set")
	case "mock":
		c.api, err = getMockApi()
		if err != nil {
			return nil, err
		}
	case "mainnet", "testnet":
		url := "https://ton.org/testnet-global.config.json"

		if c.config.RPCHost == "mainnet" {
			url = "https://ton.org/global-config.json"
		}

		c.logger.Info().Str("url", url).Msg("get global config")

		var cfg *liteclient.GlobalConfig
		cfg, err = liteclient.GetConfigFromUrl(c.ctx, url)
		if err != nil {
			c.logger.Err(err).Msg("fail to get network config")
			return nil, err
		}

		// connect to mainnet lite servers
		err = pool.AddConnectionsFromConfig(c.ctx, cfg)
		if err != nil {
			c.logger.Err(err).Msg("fail to add connections to pool")
			return nil, err
		}
	default:
		if c.config.Password == "" {
			err = fmt.Errorf("liteserver key not set")
			c.logger.Err(err).Msg("")
			return nil, err
		}
		err = pool.AddConnection(c.ctx, c.config.RPCHost, c.config.Password)
		if err != nil {
			c.logger.Err(err).Msg("fail to add client connection")
			return nil, err
		}
	}

	if c.config.RPCHost != "mock" {
		// api client with full proof checks
		policy := ton.ProofCheckPolicyFast
		c.api = ton.NewAPIClient(pool, policy)
	}

	c.tssKeyManager, err = tss.NewKeySign(server, bridge)
	if err != nil {
		logger.Err(err).Msg("failed to create tss signer")
		return nil, err
	}

	priv, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}
	c.k = priv

	pub, ok := priv.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return nil, fmt.Errorf("failed to cast pubkey to ed25519")
	}
	tonWallet, err := wallet.AddressFromPubKey(pub.Bytes(), wallet.V3R2, wallet.DefaultSubwallet)
	if err != nil {
		return nil, fmt.Errorf("failed to get wallet address: %w", err)
	}

	tonWallet.SetBounce(false)
	tonWallet.SetTestnetOnly(common.IsMocknet)
	c.localPubKey = tonWallet.String()

	var path string // if not set later, will in memory storage
	if len(c.config.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf(
			"%s/%s", config.BlockScanner.DBPath, config.BlockScanner.ChainID,
		)
	}

	c.storage, err = blockscanner.NewBlockScannerStorage(
		path,
		c.config.ScannerLevelDB,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create scan storage")
		return nil, err
	}

	// only relevant for mocknet, test image starts with height < 0
	var info *ton.BlockIDExt
	info, err = c.api.GetMasterchainInfo(c.ctx)
	if err != nil || info.SeqNo < 1 {
		return nil, fmt.Errorf("failed to get master chain info: %w", err)
	}

	c.tonScanner, err = NewTonBlockScanner(
		config.BlockScanner,
		c.bridge,
		c.api,
	)
	if err != nil {
		return nil, err
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(
		c.config.BlockScanner,
		c.storage,
		metrics,
		c.bridge,
		c.tonScanner,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create block scanner")
		return nil, err
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(
		c.storage.GetInternalDb(),
	)
	if err != nil {
		logger.Err(err).Msg("failed to create signer cache manager")
		return nil, err
	}

	return &c, nil
}

func (c *TonClient) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(common.TONChain)
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// Start TON chain client
func (c *TonClient) Start(
	globalTxsQueue chan types.TxIn,
	_ chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.tonScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.tssKeyManager.Start()

	c.wg.Add(1)
	go runners.SolvencyCheckRunner(
		c.GetChain(),
		c,
		c.bridge,
		c.stopchan,
		c.wg,
		time.Second,
	)
}

// Stop TON chain client
func (c *TonClient) Stop() {
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

func (c *TonClient) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// GetChain returns the chain.
func (c *TonClient) GetChain() common.Chain {
	return c.config.ChainID
}

// GetConfig returns the chain client configuration
func (c *TonClient) GetConfig() tcconfig.BifrostChainConfiguration {
	return c.config
}

// GetHeight returns the current height of the chain.
func (c *TonClient) GetHeight() (int64, error) {
	return c.tonScanner.GetHeight()
}

// GetAccount returns the account for the given public key.
func (c *TonClient) GetAccount(
	pubKey common.PubKey,
	height *big.Int,
) (common.Account, error) {
	address, err := pubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).
			Str("pubkey", pubKey.String()).
			Msg("failed to get pool address")
		return common.Account{}, err
	}
	return c.GetAccountByAddress(address.String(), height)
}

// GetAccountByAddress returns the account for the given address.
func (c *TonClient) GetAccountByAddress(
	address string,
	_ *big.Int,
) (common.Account, error) {
	info, err := c.api.GetMasterchainInfo(context.Background())
	if err != nil {
		c.logger.Err(err).Msg("failed to get master chain info")
		return common.Account{}, err
	}

	tonAddress, err := tonaddress.ParseAddr(address)
	if err != nil {
		c.logger.Err(err).Msg("failed to parse address")
		return common.Account{}, err
	}

	asset, err := common.NewAsset("TON.GRAM")
	if err != nil {
		c.logger.Err(err).Msg("failed to create asset")
		return common.Account{}, err
	}

	account, err := c.api.GetAccount(context.Background(), info, tonAddress)
	if err != nil {
		c.logger.Err(err).Msg("failed to get account")
		return common.Account{}, err
	}

	if account.State == nil {
		c.logger.Warn().Msg("account not initialized")
		return common.Account{
			Coins: []common.Coin{{
				Asset:  asset,
				Amount: math.NewUint(0),
				// Decimals: int64(account.State.Balance.Decimals()),
			}},
		}, nil
	}

	decimals := common.TONChain.GetGasAssetDecimal()

	amount, err := common.ConvertDecimals(
		account.State.Balance.Nano(),
		int(decimals),
		common.THORChainDecimals,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to convert gas amount to decimals")
		return common.Account{}, err
	}

	return common.Account{
		Coins: []common.Coin{{
			Asset:    asset,
			Amount:   math.NewUintFromBigInt(amount),
			Decimals: decimals,
		}},
	}, nil
}

// GetBlockScannerHeight returns block scanner height for chain
func (c *TonClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block scanner to the last observed block
func (c *TonClient) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

// GetLatestTxForVault returns last observed and broadcasted tx for a particular vault and chain
func (c *TonClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(
		types.InboundCacheKey(vault, c.GetChain().String()),
	)
	if err != nil {
		return "", "", err
	}

	lastBroadcasted, err := c.signerCacheManager.GetLatestRecordedTx(
		types.BroadcastCacheKey(vault, c.GetChain().String()),
	)

	return lastObserved, lastBroadcasted, err
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *TonClient) GetConfirmationCount(_ types.TxIn) int64 {
	return 0
}

// ConfirmationCountReady TON chain has almost instant finality,
// so doesn't need to wait for confirmation
func (c *TonClient) ConfirmationCountReady(_ types.TxIn) bool {
	return true
}

// OnObservedTxIn is called when a new observed tx is received.
func (c *TonClient) OnObservedTxIn(txIn types.TxInItem, _ int64) {
	m, err := tcmemo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Msgf("fail to parse memo: %s", txIn.Memo)
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

// SignTx returns the signed transaction.
func (c *TonClient) SignTx(
	txOutItem types.TxOutItem,
	thorchainHeight int64,
) ([]byte, []byte, *types.TxInItem, error) {
	var err error
	defer func() {
		if err != nil {
			var keysignError tss.KeysignError
			if errors.As(err, &keysignError) {
				if len(keysignError.Blame.BlameNodes) == 0 {
					c.logger.Err(err).Msg("TSS doesn't know which node to blame")
					return
				}

				// key sign error forward the keysign blame to thorchain
				var txID common.TxID
				txID, err = c.bridge.PostKeysignFailure(keysignError.Blame, thorchainHeight, txOutItem.Memo, txOutItem.Coins, txOutItem.VaultPubKeyEddsa)
				if err != nil {
					c.logger.Err(err).Msg("fail to post keysign failure to THORChain")
					return
				}
				c.logger.Info().Str("tx_id", txID.String()).Msgf("post keysign failure to thorchain")
			}
			c.logger.Err(err).Msg("failed to sign tx")
			return
		}
	}()
	if c.signerCacheManager.HasSigned(txOutItem.CacheHash()) {
		c.logger.Info().
			Interface("tx", txOutItem).
			Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

	signed, err := c.createAndSignTx(txOutItem)
	if err != nil {
		return nil, nil, nil, err
	}

	currentHeight, err := c.tonScanner.GetHeight()
	if err != nil {
		return nil, nil, nil, err
	}

	txIn := types.NewTxInItem(
		currentHeight,
		"",
		txOutItem.Memo,
		c.GetAddress(txOutItem.VaultPubKeyEddsa),
		txOutItem.ToAddress.String(),
		txOutItem.Coins,
		append(common.Gas{}, txOutItem.MaxGas.ToCoins()...),
		txOutItem.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	return signed, nil, txIn, nil
}

// BroadcastTx sends the transaction to TON chain
func (c *TonClient) BroadcastTx(
	txOut types.TxOutItem,
	txBytes []byte,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), broadcastTimeout)
	defer cancel()

	var stateInit *tlb.StateInit

	body, err := cell.FromBOC(txBytes)
	if err != nil {
		c.logger.Err(err).Msg("fail to parse txBytes")
		return "", err
	}

	vaultAddr, err := txOut.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return "", fmt.Errorf("fail to get vault address: %w", err)
	}

	fromAddress, err := tonaddress.ParseAddr(vaultAddr.String())
	if err != nil {
		c.logger.Err(err).
			Str("address", fromAddress.String()).
			Msg("failed to parse vault address")
		return "", err
	}

	initialized := true

	// check if source wallet contract is already deployed
	block, err := c.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		c.logger.Err(err).Msg("failed to get master chain info")
		return "", err
	}

	_, err = c.api.WaitForBlock(block.SeqNo).RunGetMethod(
		ctx, block, fromAddress, "seqno",
	)
	if err != nil {
		execErr, ok := err.(ton.ContractExecError)
		if ok && execErr.Code == ton.ErrCodeContractNotInitialized {
			c.logger.Info().Msg("contract not initialized")
			initialized = false
		} else {
			c.logger.Err(err).Msg("failed to run method")
			return "", err
		}
	}

	if !initialized {
		var pubKey cryptotypes.PubKey
		pubKey, err = cosmos.GetPubKeyFromBech32(
			cosmos.Bech32PubKeyTypeAccPub,
			txOut.VaultPubKeyEddsa.String(),
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to get pub key")
			return "", err
		}

		stateInit, err = wallet.GetStateInit(
			ed25519.PublicKey(pubKey.Bytes()),
			wallet.V3R2,
			wallet.DefaultSubwallet,
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to get state init")
			return "", err
		}
	}

	msg := &tlb.ExternalMessage{
		DstAddr:   fromAddress,
		StateInit: stateInit,
		Body:      body,
	}

	tx, _, _, errSend := c.api.SendExternalMessageWaitTransaction(ctx, msg)
	if errSend != nil {
		errSend = fmt.Errorf("failed to broadcast transaction: %w", errSend)

		if strings.Contains(errSend.Error(), "inbound external message rejected by transaction") {
			// try to find the hash for this transaction
			var slice *cell.Slice
			slice, err = body.BeginParse().LoadRef()
			if err != nil {
				c.logger.Err(err).Msg("failed to load ref")
				return "", errSend
			}

			var internal tlb.InternalMessage
			err = tlb.LoadFromCell(&internal, slice)
			if err != nil {
				c.logger.Err(err).Msg("failed to parse internal message")
				return "", errSend
			}

			tx, err = c.api.FindLastTransactionByOutMsgHash(ctx, fromAddress, internal.Body.Hash())
			if err == nil && tx != nil {
				errSend = nil
			}
		}

		if errSend != nil {
			return "", errSend
		}
	}

	if tx == nil {
		return "", fmt.Errorf("failed to broadcast transaction")
	}

	hash := hex.EncodeToString(tx.Hash)

	if err = c.signerCacheManager.SetSigned(txOut.CacheHash(), txOut.CacheVault(c.GetChain()), hash); err != nil {
		c.logger.Err(err).Msg("fail to set signer cache")
	}

	return hash, nil
}

func (c *TonClient) ShouldReportSolvency(height int64) bool {
	return height%20 == 19
}

func (c *TonClient) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}

	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	// (FetchTxs passes PreviousHeight + 1 from scanBlocks, while SolvencyCheckRunner passes chainHeight)
	if !c.IsBlockScannerHealthy() && height == c.blockScanner.PreviousHeight()+1 {
		return nil
	}

	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards,err: %w", err)
	}

	fee := c.config.ChainID.NativeGasToThorchain(
		math.NewUint(c.tonScanner.feeRate.Load()),
	)

	solventMsgs := make([]types.Solvency, 0, len(vaults))
	insolventMsgs := make([]types.Solvency, 0, len(vaults))

	for i, vault := range vaults {
		acc, err := c.GetAccount(vault.PubKeyEddsa, big.NewInt(height))
		if err != nil {
			c.logger.Err(err).Msgf("fail to get account balance")
			continue
		}

		msg := types.Solvency{
			Height: height,
			Chain:  c.config.ChainID,
			PubKey: vaults[i].PubKeyEddsa,
			Coins:  acc.Coins,
		}

		if runners.IsVaultSolvent(acc, vaults[i], fee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}

		insolventMsgs = append(insolventMsgs, msg)
	}

	msgs := insolventMsgs
	solvent := false

	if !c.IsBlockScannerHealthy() && len(solventMsgs) == len(vaults) {
		msgs = solventMsgs
		solvent = true
	}

	for _, msg := range msgs {
		c.logger.Info().
			Str("asgard", msg.PubKey.String()).
			Interface("coins", msg.Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")
		// send solvency to thorchain via global queue consumed by the observer
		select {
		case c.globalSolvencyQueue <- msg:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msgf("fail to send solvency info to THORChain, timeout")
		}
	}

	return nil
}

func (c *TonClient) createAndSignTx(
	txOutItem types.TxOutItem,
) ([]byte, error) {
	coin := txOutItem.Coins.GetCoin(common.GRAMAsset)

	if coin.IsEmpty() {
		err := fmt.Errorf("no TON coins found")
		c.logger.Err(err).Interface("coins", txOutItem.Coins).Msg("")
		return nil, err
	}

	comment, err := wallet.CreateCommentCell(txOutItem.Memo)
	if err != nil {
		c.logger.Err(err).Msg("failed to create comment")
		return nil, err
	}

	vaultAddr, err := txOutItem.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return nil, fmt.Errorf("fail to get vault address: %w", err)
	}

	fromAddress, err := tonaddress.ParseAddr(vaultAddr.String())
	if err != nil {
		c.logger.Err(err).
			Str("address", fromAddress.String()).
			Msg("failed to parse vault address")
		return nil, err
	}

	toAddress, err := tonaddress.ParseAddr(string(txOutItem.ToAddress))
	if err != nil {
		c.logger.Err(err).Msg("failed to parse destination address")
		return nil, err
	}

	amount, err := common.ConvertDecimals(
		coin.Amount.BigInt(),
		common.THORChainDecimals,
		int(common.TONChain.GetGasAssetDecimal()),
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to convert amount to decimals")
		return nil, err
	}

	message := wallet.SimpleMessage(toAddress, tlb.FromNanoTON(amount), comment)
	message.InternalMessage.Bounce = false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	seqno := big.NewInt(0)

	// we need fresh block info to run get methods
	block, err := c.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		c.logger.Err(err).Msg("failed to get current master chain info")
		return nil, err
	}

	result, err := c.api.WaitForBlock(block.SeqNo).RunGetMethod(
		ctx, block, fromAddress, "seqno",
	)
	if err == nil {
		seqno, err = result.Int(0)
		if err != nil {
			c.logger.Err(err).Msg("failed to parse seqno")
			return nil, err
		}
	} else {
		execErr, ok := err.(ton.ContractExecError)
		if ok && execErr.Code == ton.ErrCodeContractNotInitialized {
			c.logger.Info().Msg("wallet not yet initialized")
		} else {
			c.logger.Err(err).Msg("failed to get seqno")
			return nil, err
		}
	}

	// Use THORChain block timestamp for determinism
	timestamp, err := c.bridge.GetBlockTimestamp(txOutItem.Height)
	if err != nil {
		c.logger.Err(err).Msg("failed to get THORChain block timestamp")
		return nil, err
	}

	timeout := timestamp.UTC().Add(TransactionTimeout)

	payload := cell.BeginCell()

	err = payload.StoreUInt(uint64(wallet.DefaultSubwallet), 32)
	if err != nil {
		c.logger.Err(err).Msg("failed to store subwallet")
		return nil, err
	}

	err = payload.StoreUInt(uint64(timeout.Unix()), 32)
	if err != nil {
		c.logger.Err(err).Msg("failed to store timeout")
		return nil, err
	}

	err = payload.StoreUInt(seqno.Uint64(), 32)
	if err != nil {
		c.logger.Err(err).Msg("failed to store seqno")
		return nil, err
	}

	internalMessage, err := tlb.ToCell(message.InternalMessage)
	if err != nil {
		c.logger.Err(err).Msg("failed to convert internal message to cell")
		return nil, err
	}

	err = payload.StoreUInt(uint64(message.Mode), 8)
	if err != nil {
		c.logger.Err(err).Msg("failed to store message mode")
		return nil, err
	}

	err = payload.StoreRef(internalMessage)
	if err != nil {
		c.logger.Err(err).Msg("failed to store internal message")
		return nil, err
	}

	hash := payload.EndCell().Hash()

	signature, err := c.sign(txOutItem, hash)
	if err != nil {
		c.logger.Err(err).Msg("failed to sign message")
		return nil, err
	}

	body := cell.BeginCell()
	err = body.StoreSlice(signature, 512)
	if err != nil {
		c.logger.Err(err).Msg("failed to store signature")
		return nil, err
	}

	err = body.StoreBuilder(payload)
	if err != nil {
		c.logger.Err(err).Msg("failed to store payload")
		return nil, err
	}

	c.logger.Debug().
		Str("vault", txOutItem.VaultPubKeyEddsa.String()).
		Str("from", fromAddress.String()).
		Str("to", txOutItem.ToAddress.String()).
		Str("amount", coin.Amount.String()).
		Str("memo", txOutItem.Memo).
		Uint64("seqno", seqno.Uint64()).
		Time("timeout", timeout).
		Str("signature", hex.EncodeToString(signature)).
		Msg("Signed tx")

	return body.EndCell().ToBOC(), nil
}

func (c *TonClient) sign(txOut types.TxOutItem, message []byte) ([]byte, error) {
	parsedPub, err := txOut.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return nil, fmt.Errorf("failed to get address from pubkey: %w", err)
	}

	var sig []byte
	if parsedPub.String() == c.localPubKey {
		sig, err = c.k.Sign(message)
		if err != nil {
			return nil, fmt.Errorf("failed to sign local transaction: %w", err)
		}
	} else {
		sig, _, err = c.tssKeyManager.RemoteSign(message, common.SigningAlgoEd25519, txOut.VaultPubKeyEddsa.String())
		if err != nil {
			return nil, fmt.Errorf("failed to sign remote transaction: %w", err)
		}
	}

	cryptoPubKey, err := cosmos.GetPubKeyFromBech32(
		cosmos.Bech32PubKeyTypeAccPub,
		txOut.VaultPubKeyEddsa.String(),
	)
	if err != nil {
		return nil, err
	}

	ok := ed25519.Verify(cryptoPubKey.Bytes(), message, sig)
	if !ok {
		return nil, fmt.Errorf("failed to verify signature")
	}

	return sig, nil
}
