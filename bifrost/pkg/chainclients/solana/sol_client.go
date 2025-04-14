package solana

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/mr-tron/base58"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// SOLClient is a client for the Solana blockchain
type SOLClient struct {
	logger                  zerolog.Logger
	cfg                     config.BifrostChainConfiguration
	localPubKey             string
	k                       *cosmoscryptoed25519.PrivKey
	solScanner              *SOLScanner
	solGeyser               *SolGeyser
	bridge                  thorclient.ThorchainBridge
	blockScanner            *blockscanner.BlockScanner
	poolMgr                 thorclient.PoolManager
	tssKeyManager           tss.ThorchainKeyManager
	wg                      *sync.WaitGroup
	stopchan                chan struct{}
	globalSolvencyQueue     chan stypes.Solvency
	signerCacheManager      *signercache.CacheManager
	lastSolvencyCheckHeight int64
	rpcClient               *rpc.SolRPC
}

// NewSOLClient creates a new instance of a SOLClient
func NewSOLClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
) (*SOLClient, error) {
	// check required arguments
	if thorKeys == nil {
		return nil, fmt.Errorf("failed to create EVM client, thor keys empty")
	}
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}
	if poolMgr == nil {
		return nil, errors.New("pool manager is nil")
	}

	// // create keys
	tssKm, err := tss.NewKeySign(server, bridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}
	priv, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	pub, ok := priv.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return nil, fmt.Errorf("failed to cast pubkey to ed25519")
	}
	formattedPub := base58.Encode(pub.Bytes())

	// create rpc clients
	rpcClient := rpc.NewSolRPC(cfg.RPCHost, cfg.BlockScanner.HTTPRequestTimeout)

	c := &SOLClient{
		logger:        log.With().Str("module", "sol").Stringer("chain", cfg.ChainID).Logger(),
		cfg:           cfg,
		localPubKey:   formattedPub,
		k:             priv,
		bridge:        bridge,
		poolMgr:       poolMgr,
		tssKeyManager: tssKm,
		wg:            &sync.WaitGroup{},
		stopchan:      make(chan struct{}),
		rpcClient:     rpcClient,
	}

	// initialize storage
	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	storage, err := blockscanner.NewBlockScannerStorage(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return c, fmt.Errorf("fail to create blockscanner storage: %w", err)
	}
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager")
	}
	c.signerCacheManager = signerCacheManager

	// create block scanner
	c.solScanner, err = NewSOLScanner(
		c.cfg.BlockScanner,
		storage,
		bridge,
		m,
		rpcClient,
		pubkeyMgr,
		c.ReportSolvency,
		signerCacheManager,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create evm block scanner: %w", err)
	}

	// create geyser receiver
	c.solGeyser, err = NewSolGeyser(c.cfg.BlockScanner, c.solScanner, cfg.GeyserHost, cfg.GeyserToken)
	if err != nil {
		return c, fmt.Errorf("fail to create sol geyser: %w", err)
	}

	// Set geyser reference in scanner
	c.solScanner.SetGeyser(c.solGeyser)

	// initialize block scanner
	c.blockScanner, err = blockscanner.NewBlockScanner(
		c.cfg.BlockScanner, storage, m, c.bridge, c.solScanner,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create block scanner: %w", err)
	}

	return c, nil
}

// Start starts the chain client with the given queues.
func (c *SOLClient) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	//c.solScanner.globalErrataQueue = globalErrataQueue
	c.globalSolvencyQueue = globalSolvencyQueue
	c.solScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.tssKeyManager.Start()
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.solGeyser.Run()
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.bridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
}

// Stop stops the chain client.
func (c *SOLClient) Stop() {
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	c.solGeyser.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// IsBlockScannerHealthy returns true if the block scanner is healthy.
func (c *SOLClient) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// --------------------------------- config ---------------------------------

// GetConfig returns the chain configuration.
func (c *SOLClient) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

// GetChain returns the chain.
func (c *SOLClient) GetChain() common.Chain {
	return common.SOLChain
}

// --------------------------------- status ---------------------------------

// GetHeight returns the current height of the chain.
func (c *SOLClient) GetHeight() (int64, error) {
	return c.solScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height
func (c *SOLClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

func (c *SOLClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadCasted, err
}

// --------------------------------- addresses ---------------------------------

// GetAddress returns the address for the given public key.
func (c *SOLClient) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// GetAccount returns the account for the given public key.
func (c *SOLClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	// TODO: Implement?
	return common.Account{}, nil
}

// GetAccountByAddress returns the account for the given address.
func (c *SOLClient) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	// TODO: Implement?
	return common.Account{}, nil
}

// --------------------------------- balances ---------------------------------

// GetBalance returns the balance of the provided address.
func (c *SOLClient) GetBalance(addr, token string, height *big.Int) (*big.Int, error) {
	// TODO: Implement
	return big.NewInt(0), nil
}

// GetBalances returns the balances of the provided address.
func (c *SOLClient) GetBalances(addr string, height *big.Int) (common.Coins, error) {
	// TODO: Implement
	return common.Coins{}, nil
}

// --------------------------------- solvency ---------------------------------

// ReportSolvency reports solvency once per configured solvency blocks.
func (c *SOLClient) ReportSolvency(height int64) error {
	// TODO: Implement
	if !c.ShouldReportSolvency(height) {
		return nil
	}
	c.lastSolvencyCheckHeight = height
	return nil
}

// ShouldReportSolvency returns true if the given height is a solvency report height.
func (c *SOLClient) ShouldReportSolvency(height int64) bool {
	return height%c.cfg.SolvencyBlocks == 0
}

// --------------------------------- sign ---------------------------------

// SignTx returns the signed transaction.
func (c *SOLClient) SignTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if tx.Chain != common.SOLChain {
		return nil, nil, nil, fmt.Errorf("tx chain is not solana")
	}
	if tx.ToAddress.IsEmpty() {
		return nil, nil, nil, fmt.Errorf("to address is empty")
	}
	if tx.VaultPubKeyEddsa.IsEmpty() {
		return nil, nil, nil, fmt.Errorf("vault public key is empty")
	}

	if len(tx.Memo) == 0 {
		return nil, nil, nil, fmt.Errorf("can't sign tx when it doesn't have memo")
	}

	solTx, err := c.CreateTx(tx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to create tx: %w", err)
	}

	checkPointBytes, err := solTx.Message.Serialize()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to serialize checkpoint bytes: %w", err)
	}

	signedTx, err := c.signTransaction(solTx, tx)
	if err != nil {
		return checkPointBytes, nil, nil, fmt.Errorf("fail to sign tx: %w", err)
	}

	prettyPrint(signedTx)

	signedBytes, err := signedTx.Serialize()
	if err != nil {
		return checkPointBytes, nil, nil, fmt.Errorf("fail to serialize signed tx: %w", err)
	}

	fmt.Println("SIGNED TX", string(signedBytes))

	return signedBytes, nil, nil, nil
}

// SignTransaction takes a rpc.Transaction and signs with the pubkey
func (c *SOLClient) signTransaction(solTx types.Transaction, txOut stypes.TxOutItem) (types.Transaction, error) {
	message, err := solTx.Message.Serialize()
	if err != nil {
		return solTx, fmt.Errorf("failed to serialize transaction: %w", err)
	}

	var sig []byte

	parsedPub, err := common.PubKey(txOut.VaultPubKeyEddsa).GetAddress(c.GetChain())
	if err != nil {
		return types.Transaction{}, fmt.Errorf("failed to get address from pubkey: %w", err)
	}

	if parsedPub.String() == c.localPubKey {
		sig, err = c.k.Sign(message)
		if err != nil {
			return types.Transaction{}, fmt.Errorf("failed to sign local transaction: %w", err)
		}
	} else {
		sig, _, err = c.tssKeyManager.RemoteSign(message, txOut.VaultPubKeyEddsa.String())
		if err != nil {
			return types.Transaction{}, fmt.Errorf("failed to sign remote transaction: %w", err)
		}
	}

	vaultAccount := solTx.Message.Accounts[0]
	if !ed25519.Verify(vaultAccount.Bytes(), message, sig) {
		return types.Transaction{}, fmt.Errorf("signature verification failed")
	}

	solTx.AddSignature(sig)
	return solTx, nil
}

// CreateTx builds a solana transfer function from a TxOutItem using the transaction
// specification (https://solana.com/docs/core/transactions#manual-sol-transfer)
func (c *SOLClient) CreateTx(tx stypes.TxOutItem) (types.Transaction, error) {
	// Validation
	memo, err := mem.ParseMemo(common.LatestVersion, tx.Memo)
	if err != nil {
		return types.Transaction{}, fmt.Errorf("fail to parse memo(%s):%w", tx.Memo, err)
	}
	if memo.IsInbound() {
		return types.Transaction{}, fmt.Errorf("inbound memo should not be used for outbound tx")
	}
	if len(tx.Coins) != 1 {
		return types.Transaction{}, fmt.Errorf("only support one coin per transaction")
	}

	solTx := types.Transaction{}

	// Create account pubkeys
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.Transaction{}, fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create vault public key: %w", err)
	}
	toPubkey, err := types.PublicKeyFromString(tx.ToAddress.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create to public key: %w", err)
	}

	accountKeys := []types.PublicKey{vaultPubKey, toPubkey, types.SystemProgramID, types.MemoProgramID}

	// Memo instruction
	// Program index is 3 (referencing accountKeys) for the memo program
	// Account index is 0 for the vault/signer
	memoInstruction := types.NewCompiledInstruction(3, []int{0}, []byte(tx.Memo))

	solValue := convertToLamports(tx.Coins[0].Amount)
	if solValue.Int64() == 0 {
		return solTx, fmt.Errorf("sol value is zero")
	}
	// Transfer instruction
	// Program index is 2 (referencing accountKeys) for the system program
	// Account index is 0 for the vault/signer
	// Account index is 1 for the recipient
	transferInstruction := types.NewCompiledTransferInstruction(2, 0, 1, solValue.Uint64())

	header := types.MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 2,
	}

	recentBlockhash := c.solScanner.GetLatestBlockHash()

	message := types.NewMessage(header, accountKeys, recentBlockhash, []types.CompiledInstruction{memoInstruction, transferInstruction})
	return types.NewTransaction(message), nil
}

// --------------------------------- sign ---------------------------------

func convertToLamports(amount cosmos.Uint) *big.Int {
	// thorchain amounts in 1e8
	// multiply by 10 to get 1e9 (lamports)
	return new(big.Int).Mul(amount.BigInt(), big.NewInt(10))
}

// --------------------------------- broadcast ---------------------------------

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *SOLClient) BroadcastTx(txOutItem stypes.TxOutItem, rawTx []byte) (string, error) {
	base64Tx := base64.StdEncoding.EncodeToString(rawTx)
	txSig, err := c.rpcClient.BroadcastTx(base64Tx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}
	c.logger.Info().Str("memo", txOutItem.Memo).Str("txid", txSig).Msg("broadcast tx")

	// update the signer cache
	if err := c.signerCacheManager.SetSigned(txOutItem.CacheHash(), txOutItem.CacheVault(c.GetChain()), txSig); err != nil {
		c.logger.Err(err).Interface("txOutItem", txOutItem).Msg("fail to mark tx out item as signed")
	}

	// TODO: update signed block height
	// blockHeight, err := c.bridge.GetBlockHeight()
	// if err != nil {
	// 	c.logger.Err(err).Msg("fail to get current THORChain block height")
	// 	// at this point , the tx already broadcast successfully , don't return an error
	// 	// otherwise will cause the same tx to retry
	// } else if err = c.AddSignedTxItem(txID, blockHeight, txOutItem.VaultPubKey.String(), &txOutItem); err != nil {
	// 	c.logger.Err(err).Str("hash", txID).Msg("fail to add signed tx item")
	// }

	return txSig, nil
}

// --------------------------------- observe ---------------------------------

// OnObservedTxIn is called when a new observed tx is received.
func (c *SOLClient) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	// TODO: Implement
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *SOLClient) GetConfirmationCount(txIn stypes.TxIn) int64 {
	// TODO: Implement
	return 0
}

// ConfirmationCountReady returns true if the confirmation count is ready.
func (c *SOLClient) ConfirmationCountReady(txIn stypes.TxIn) bool {
	// TODO: Implement
	return true
}
