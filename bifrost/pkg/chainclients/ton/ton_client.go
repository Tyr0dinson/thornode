package ton

import (
	"context"
	"encoding/base64"
	"fmt"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"math/big"
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

	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
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
			return nil, client.error(err, "fail to get network config")
		}

		// connect to mainnet lite servers
		err = pool.AddConnectionsFromConfig(ctx, cfg)
		if err != nil {
			return nil, client.error(err, "fail to add connections to pool")
		}
	default:
		if config.Password == "" {
			return nil, client.error(nil, "key not set")
		}
		err := pool.AddConnection(ctx, config.RPCHost, config.Password)
		if err != nil {
			logger.Err(err).Msg("fail to add client connection")
			return nil, err
		}
	}

	client.tssKeyManager, err = tss.NewKeySign(server, bridge)
	if err != nil {
		return nil, client.error(err, "failed to create tss signer")
	}

	// client.localKeyManager, err = NewLocalKeyManager(thorKeys)
	// if err != nil {
	// 	return nil, client.error(err, "failed to create local key manager")
	// }

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
		return nil, client.error(err, "failed to create scan storage")
	}

	// api client with full proof checks
	policy := ton.ProofCheckPolicyFast
	client.api = ton.NewAPIClient(pool, policy).WithRetry()
	// client.api.SetTrustedBlockFromConfig(liteClientConfig)

	client.tonScanner, err = NewTonBlockScanner(
		config.BlockScanner,
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
		return nil, client.error(err, "failed to create block scanner")
	}

	client.signerCacheManager, err = signercache.NewSignerCacheManager(
		client.storage.GetInternalDb(),
	)
	if err != nil {
		return nil, client.error(err, "failed to create signer cache manager")
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
		_ = c.error(err, "failed to get pool address")
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
		return common.Account{}, c.error(err, "failed to get master chain info")
	}

	tonAddress, err := tonaddress.ParseAddr(address)
	if err != nil {
		return common.Account{}, c.error(err, "failed to parse address")
	}

	account, err := c.api.GetAccount(context.Background(), info, tonAddress)
	if err != nil {
		return common.Account{}, c.error(err, "failed to get account")
	}

	asset, err := common.NewAsset("TON.TON")
	if err != nil {
		return common.Account{}, c.error(err, "failed to create asset")
	}

	return common.Account{
		Coins: []common.Coin{{
			Asset:    asset,
			Amount:   cosmos.NewUintFromBigInt(account.State.Balance.Nano()),
			Decimals: int64(account.State.Balance.Decimals()),
		}},
	}, nil

}

// GetBlockScannerHeight returns block scanner height for chain
func (c *TonClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// GetLatestTxForVault returns last observed and broadcasted tx for a particular vault and chain
func (c *TonClient) GetLatestTxForVault(vault string) (string, string, error) {
	return "", "", nil
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *TonClient) GetConfirmationCount(txIn types.TxIn) int64 {
	return 0
}

// ConfirmationCountReady TON chain has almost instant finality,
// so doesn't need to wait for confirmation
func (c *TonClient) ConfirmationCountReady(txIn types.TxIn) bool {
	return true
}

// OnObservedTxIn is called when a new observed tx is received.
func (c *TonClient) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {
	// TODO: remove
	m, err := memo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Msgf("fail to parse memo: %s", txIn.Memo)
		return
	}
	c.logger.Info().
		Str("hash", txIn.Tx).
		Str("type", m.GetType().String()).
		Uint64("amount", m.GetAmount().Uint64()).
		Str("asset", m.GetAsset().String()).
		Msg("found tx")
}

// SignTx returns the signed transaction.
func (c *TonClient) SignTx(
	tx types.TxOutItem,
	height int64,
) ([]byte, []byte, *types.TxInItem, error) {
	c.logger.Info().Msg(fmt.Sprintf("%v", tx))

	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		c.logger.Info().
			Interface("tx", tx).
			Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

	c.bridge.GetPubKeys()

	// TODO: implement double spend checks

	msg, err := c.processOutboundMessage(tx)
	if err != nil {
		c.logger.Err(err).Msg("failed to process outbound tx")
		return nil, nil, nil, err
	}

	// TODO: handle checkpoints

	return msg, nil, nil, nil
}

// BroadcastTx sends the transaction to TON chain
func (c *TonClient) BroadcastTx(
	txOut types.TxOutItem,
	txBytes []byte,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	body, err := cell.FromBOC(txBytes)
	if err != nil {
		c.logger.Err(err).Msg("fail to parse txBytes")
		return "", err
	}

	addr := c.GetAddress(txOut.VaultPubKey)
	src, err := address.ParseAddr(addr)
	if err != nil {
		c.logger.Err(err).
			Str("address", addr).
			Msg("fail to parse vault address")
		return "", err
	}

	var stateInit *tlb.StateInit

	msg := &tlb.ExternalMessage{
		DstAddr:   src,
		StateInit: stateInit,
		Body:      body,
	}

	tx, _, _, err := c.api.SendExternalMessageWaitTransaction(ctx, msg)
	hash := base64.StdEncoding.EncodeToString(tx.Hash)
	if err == nil {
		err = c.signerCacheManager.SetSigned(
			txOut.CacheHash(),
			txOut.CacheVault(c.GetChain()),
			hash,
		)
		if err != nil {
			c.logger.Err(err).Msg("fail to set signer cache")
		}
	}

	return hash, nil
}

func (c *TonClient) ShouldReportSolvency(height int64) bool {
	// TODO: Implement fee observaton / calculation
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
	fee := cosmos.NewUint(1)

	solventMsgs := make([]types.Solvency, 0, len(vaults))
	insolventMsgs := make([]types.Solvency, 0, len(vaults))

	fmt.Println("check")

	for i, vault := range vaults {
		fmt.Println(vault.PubKey)
		acc, err := c.GetAccount(vault.PubKey, big.NewInt(height))
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

func (c *TonClient) processOutboundMessage(
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
		return nil, c.error(err, "failed to create comment")
	}

	fromAddress, err := address.ParseAddr(c.GetAddress(tx.VaultPubKey))
	if err != nil {
		return nil, c.error(err, "failed to parse vault address")
	}

	toAddress, err := address.ParseAddr(string(tx.ToAddress))
	if err != nil {
		return nil, c.error(err, "failed to parse destination address")
	}

	message := wallet.SimpleMessage(
		toAddress, tlb.FromNanoTON(coin.Amount.BigInt()), comment,
	)
	message.InternalMessage.Bounce = false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	// we need fresh block info to run get methods
	block, err := c.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, c.error(err, "failed to get block")
	}

	result, err := c.api.WaitForBlock(block.SeqNo).RunGetMethod(ctx, block, fromAddress, "seqno")
	if err != nil {
		return nil, c.error(err, "failed to query wallet")
	}

	seqno, err := result.Int(0)
	if err != nil {
		return nil, c.error(err, "failed to parse seqno")
	}

	payload := cell.BeginCell().
		MustStoreUInt(uint64(wallet.DefaultSubwallet), 32).
		MustStoreUInt(uint64(time.Now().Add(time.Duration(30)*time.Second).UTC().Unix()), 32).
		MustStoreUInt(seqno.Uint64(), 32) // seq

	tonCell, err := tlb.ToCell(message.InternalMessage)
	if err != nil {
		return nil, c.error(err, "failed to convert internal message to tonCell")
	}

	err = payload.StoreUInt(uint64(message.Mode), 8)
	if err != nil {
		return nil, c.error(err, "failed to store message mode")
	}

	err = payload.StoreRef(tonCell)
	if err != nil {
		return nil, c.error(err, "failed to store tonCell")
	}

	hash := payload.EndCell().Hash()

	signature, err := c.sign(hash, tx.VaultPubKey)
	if err != nil {
		c.logger.Err(err).Msg("failed to sign message")
		return nil, err
	}

	builder := cell.BeginCell()
	err = builder.StoreSlice(signature, 32)
	if err != nil {
		return nil, c.error(err, "failed to store signature")
	}

	err = builder.StoreBuilder(payload)
	if err != nil {
		return nil, c.error(err, "failed to store payload")
	}

	data := builder.EndCell().ToBOC()

	return data, nil
}

func (c *TonClient) sign(data []byte, pubkey common.PubKey) ([]byte, error) {
	// TODO: add localKeyManager
	sig, _, err := c.tssKeyManager.RemoteSign(data, pubkey.String())
	if err != nil {
		return nil, err
	}

	// TODO: add verification

	return sig, nil
}

func (c *TonClient) error(err error, msg string) error {
	if err != nil {
		c.logger.Err(err).Msg(msg)
		return fmt.Errorf("%s: %w", msg, err)
	}
	c.logger.Error().Msg(msg)
	return fmt.Errorf("%s", msg)
}
