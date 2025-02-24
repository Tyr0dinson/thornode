package ton

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"golang.org/x/crypto/ed25519"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xssnick/tonutils-go/address"
	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
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
	V3R2CodeHex = "B5EE9C724101010100710000DEFF0020DD2082014C97BA218201339CBAB19F71B0ED44D0D31FD31F31D70BFFE304E0A4F2608308D71820D31FD31FD31FF82313BBF263ED44D0D31FD31FD3FFD15132BAF2A15144BAF2A204F901541055F910F2A3F8009320D74A96D307D402FB00E8D101A4C8CB1FCB1FCBFFC9ED5410BD6DAD"
	TxFee       = 5_000_000 // 0.005 TON
)

type TonClient struct {
	logger              zerolog.Logger
	config              tcconfig.BifrostChainConfiguration
	chainId             string
	blockScanner        *blockscanner.BlockScanner
	storage             *blockscanner.BlockScannerStorage
	signerCacheManager  *signercache.CacheManager
	tssKeyManager       *tss.KeySign
	tonScanner          *TonBlockScanner
	api                 ton.APIClientWrapped
	metrics             tcmetrics.Metrics
	bridge              thorclient.ThorchainBridge
	globalSolvencyQueue chan types.Solvency
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
	walletCode          *cell.Cell
}

// NewTonClient creates a new instance of a TON chain client
func NewTonClient(
	_ *thorclient.Keys,
	config tcconfig.BifrostChainConfiguration,
	server *tctss.TssServer,
	bridge thorclient.ThorchainBridge,
	metrics *tcmetrics.Metrics,
) (*TonClient, error) {
	var err error

	logger := log.With().Str("module", config.ChainID.String()).Logger()

	client := TonClient{
		logger:   logger,
		chainId:  config.ChainID.String(),
		config:   config,
		bridge:   bridge,
		wg:       &sync.WaitGroup{},
		stopchan: make(chan struct{}),
	}

	ctx := context.Background()
	pool := liteclient.NewConnectionPool()

	switch config.RPCHost {
	case "":
		return nil, fmt.Errorf("no client config set")
	case "mainnet", "testnet":
		url := "https://ton.org/testnet-global.config.json"

		if config.RPCHost == "mainnet" {
			url = "https://ton.org/global-config.json"
		}

		logger.Info().Str("url", url).Msg("get global config")

		cfg, err := liteclient.GetConfigFromUrl(ctx, url)
		if err != nil {
			logger.Err(err).Msg("fail to get network config")
			return nil, err
		}

		// connect to mainnet lite servers
		err = pool.AddConnectionsFromConfig(ctx, cfg)
		if err != nil {
			logger.Err(err).Msg("fail to add connections to pool")
			return nil, err
		}
	default:
		if config.Password == "" {
			err = fmt.Errorf("key not set")
			client.logger.Err(err).Msg("")
			return nil, err
		}
		err := pool.AddConnection(ctx, config.RPCHost, config.Password)
		if err != nil {
			logger.Err(err).Msg("fail to add client connection")
			return nil, err
		}
	}

	client.tssKeyManager, err = tss.NewKeySign(server, bridge)
	if err != nil {
		logger.Err(err).Msg("failed to create tss signer")
		return nil, err
	}

	var path string // if not set later, will in memory storage
	if len(client.config.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf(
			"%s/%s", config.BlockScanner.DBPath, config.BlockScanner.ChainID,
		)
	}

	client.storage, err = blockscanner.NewBlockScannerStorage(
		path,
		client.config.ScannerLevelDB,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create scan storage")
		return nil, err
	}

	// api client with full proof checks
	policy := ton.ProofCheckPolicyFast
	client.api = ton.NewAPIClient(pool, policy).WithRetry()
	// client.api.SetTrustedBlockFromConfig(liteClientConfig)

	client.tonScanner, err = NewTonBlockScanner(
		config.BlockScanner,
		client.bridge,
		client.api,
	)
	if err != nil {
		return nil, err
	}

	client.blockScanner, err = blockscanner.NewBlockScanner(
		client.config.BlockScanner,
		client.storage,
		metrics,
		client.bridge,
		client.tonScanner,
	)
	if err != nil {
		logger.Err(err).Msg("failed to create block scanner")
		return nil, err
	}

	client.signerCacheManager, err = signercache.NewSignerCacheManager(
		client.storage.GetInternalDb(),
	)
	if err != nil {
		logger.Err(err).Msg("failed to create signer cache manager")
		return nil, err
	}

	// store code
	data, err := hex.DecodeString(V3R2CodeHex)
	if err != nil {
		logger.Err(err).Msg("failed to decode V3R2CodeHex")
		return nil, err
	}

	client.walletCode, err = cell.FromBOC(data)
	if err != nil {
		logger.Err(err).Msg("failed to decode BOC")
		return nil, err
	}

	return &client, nil
}

// Start TON chain client
func (c *TonClient) Start(
	globalTxsQueue chan types.TxIn,
	_ chan types.ErrataBlock,
	globalSolvencyQueue chan types.Solvency,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.blockScanner.Start(globalTxsQueue)
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

// GetAddress returns the address for the given public key.
func (c *TonClient) GetAddress(pubKey common.PubKey) string {
	address, err := pubKey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Msg("failed to get pool address")
		return ""
	}

	return address.String()
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

	account, err := c.api.GetAccount(context.Background(), info, tonAddress)
	if err != nil {
		c.logger.Err(err).Msg("failed to get account")
		return common.Account{}, err
	}

	if account.State == nil {
		msg := "account not initialized"
		c.logger.Warn().Msg(msg)
		return common.Account{}, fmt.Errorf(msg)
	}

	asset, err := common.NewAsset("TON.TON")
	if err != nil {
		c.logger.Err(err).Msg("failed to create asset")
		return common.Account{}, err
	}

	amount := toThor(account.State.Balance.Nano())

	return common.Account{
		Coins: []common.Coin{{
			Asset:  asset,
			Amount: amount,
			// Decimals: int64(account.State.Balance.Decimals()),
		}},
	}, nil

}

// GetBlockScannerHeight returns block scanner height for chain
func (c *TonClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
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
	memo, err := tcmemo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Msgf("fail to parse memo: %s", txIn.Memo)
		return
	}

	validTypes := map[tcmemo.TxType]struct{}{
		tcmemo.TxMigrate:  {},
		tcmemo.TxRagnarok: {},
	}

	_, ok := validTypes[memo.GetType()]
	if !ok {
		return
	}

	err = c.signerCacheManager.SetSigned(
		txIn.CacheHash(c.GetChain(), common.BlankTxID.String()),
		txIn.CacheVault(c.GetChain()),
		txIn.Tx,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to update signer cache")
	}
}

// SignTx returns the signed transaction.
func (c *TonClient) SignTx(
	txOutItem types.TxOutItem,
	_ int64,
) ([]byte, []byte, *types.TxInItem, error) {
	c.logger.Info().Msg(fmt.Sprintf("%v", txOutItem))

	str := fmt.Sprintf("%s|%s|%s|%s", c.GetChain(), txOutItem.ToAddress, txOutItem.Coins, txOutItem.Memo)
	cacheHash := fmt.Sprintf("%X", sha256.Sum256([]byte(str)))

	if c.signerCacheManager.HasSigned(cacheHash) {
		c.logger.Info().
			Interface("tx", txOutItem).
			Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

	signed, err := c.createAndSignTx(txOutItem)
	if err != nil {
		return nil, nil, nil, err
	}

	return signed, nil, nil, nil
}

// BroadcastTx sends the transaction to TON chain
func (c *TonClient) BroadcastTx(
	txOut types.TxOutItem,
	txBytes []byte,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	var stateInit *tlb.StateInit

	body, err := cell.FromBOC(txBytes)
	if err != nil {
		c.logger.Err(err).Msg("fail to parse txBytes")
		return "", err
	}

	vaultPubKey, err := c.getEddsaVaultKey(txOut.VaultPubKey)
	if err != nil {
		c.logger.Err(err).Msg("failed to get eddsa vault pub key")
		return "", err
	}

	fromAddress, err := address.ParseAddr(c.GetAddress(vaultPubKey))
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
		pubKey, err := cosmos.GetPubKeyFromBech32(
			cosmos.Bech32PubKeyTypeAccPub,
			vaultPubKey.String(),
		)
		if err != nil {
			c.logger.Err(err).Msg("failed to get pub key")
			return "", err
		}

		stateInit = &tlb.StateInit{
			Data: cell.BeginCell().
				MustStoreUInt(0, 32). // seqno
				MustStoreUInt(uint64(wallet.DefaultSubwallet), 32). // sub wallet
				MustStoreSlice(pubKey.Bytes(), 256).
				EndCell(),
			Code: c.walletCode,
		}
	}

	msg := &tlb.ExternalMessage{
		DstAddr:   fromAddress,
		StateInit: stateInit,
		Body:      body,
	}

	tx, _, _, err := c.api.SendExternalMessageWaitTransaction(ctx, msg)

	if err != nil {
		if strings.HasSuffix(err.Error(), ": duplicate message") {
			c.logger.Err(err).Msg("message already sent")
			return "", err
		} else {
			c.logger.Err(err).Msg("failed to send external message")
			return "", err
		}
	}

	txHash := base64.StdEncoding.EncodeToString(tx.Hash)

	err = c.signerCacheManager.SetSigned(
		txOut.CacheHash(),
		txOut.CacheVault(c.GetChain()),
		txHash,
	)
	if err != nil {
		c.logger.Err(err).Msg("failed to set signer cache")
	}

	return txHash, nil
}

func (c *TonClient) ShouldReportSolvency(height int64) bool {
	return height%10 == 0
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

	// TODO: implement gas fee
	fee := toThor(big.NewInt(TxFee))

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
			PubKey: vaults[i].PubKey,
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
	tx types.TxOutItem,
) ([]byte, error) {
	coin := tx.Coins.GetCoin(common.TONAsset)

	if coin.IsEmpty() {
		err := fmt.Errorf("no TON coins found")
		c.logger.Err(err).Interface("coins", tx.Coins).Msg("")
		return nil, err
	}

	comment, err := wallet.CreateCommentCell(tx.Memo)
	if err != nil {
		c.logger.Err(err).Msg("failed to create comment")
		return nil, err
	}

	vaultPubKey, err := c.getEddsaVaultKey(tx.VaultPubKey)
	if err != nil {
		c.logger.Err(err).Msg("failed to get eddsa vault key")
		return nil, err
	}

	fromAddress, err := address.ParseAddr(c.GetAddress(vaultPubKey))
	if err != nil {
		c.logger.Err(err).Msg("failed to parse vault address")
		return nil, err
	}

	toAddress, err := address.ParseAddr(string(tx.ToAddress))
	if err != nil {
		c.logger.Err(err).Msg("failed to parse destination address")
		return nil, err
	}

	// 1e8 -> 1e9
	amount := tlb.FromNanoTON(fromThor(coin.Amount))
	message := wallet.SimpleMessage(toAddress, amount, comment)
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

	// TODO: Check and maybe use Thorchain migration height instead
	period := time.Second * 30
	timeout := time.Now().
		Truncate(period).
		Add(period * -1).
		Add(time.Minute * 3).UTC()

	c.logger.Info().Msgf("Timeout: %s", timeout.Format(time.RFC3339))

	// TODO: Replace MustStoreX with err checks
	payload := cell.BeginCell().
		MustStoreUInt(uint64(wallet.DefaultSubwallet), 32).
		MustStoreUInt(uint64(timeout.Unix()), 32).
		MustStoreUInt(seqno.Uint64(), 32) // seq

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

	signature, err := c.sign(hash, vaultPubKey)
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

	return body.EndCell().ToBOC(), nil
}

func (c *TonClient) sign(data []byte, pubkey common.PubKey) ([]byte, error) {
	// TODO: add localKeyManager

	signature, _, err := c.tssKeyManager.RemoteSign(data, pubkey.String())
	if err != nil {
		return nil, err
	}

	// TODO: use r, s for better readability
	slices.Reverse(signature)
	signature = append(signature[32:], signature[:32]...)

	cryptoPubKey, err := cosmos.GetPubKeyFromBech32(
		cosmos.Bech32PubKeyTypeAccPub,
		pubkey.String(),
	)
	if err != nil {
		return nil, err
	}

	ok := ed25519.Verify(cryptoPubKey.Bytes(), data, signature)
	if !ok {
		c.logger.Err(err).Msg("failed to verify signature")
	}

	return signature, nil
}

func (c *TonClient) getEddsaVaultKey(pubKey common.PubKey) (common.PubKey, error) {
	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		return common.EmptyPubKey, err
	}

	for _, vault := range vaults {
		if vault.PubKey.Equals(pubKey) {
			return vault.PubKeyEddsa, nil
		}
	}

	return common.EmptyPubKey, nil
}
