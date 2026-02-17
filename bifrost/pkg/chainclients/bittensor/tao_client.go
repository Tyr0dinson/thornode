package bittensor

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/blake2b"

	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	taorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/rpc"
	taotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// mortalEraPeriod is the number of blocks a mortal era transaction is valid for.
// 64 blocks ≈ 12.8 minutes at 12s block time.
const mortalEraPeriod = 64

// TAOClient is a client for the Bittensor (TAO) blockchain.
type TAOClient struct {
	logger              zerolog.Logger
	cfg                 config.BifrostChainConfiguration
	localPubKey         string // SS58 encoded local Ed25519 public key
	k                   *cosmoscryptoed25519.PrivKey
	taoScanner          *TAOScanner
	bridge              thorclient.ThorchainBridge
	poolMgr             thorclient.PoolManager
	tssKeyManager       tss.ThorchainKeyManager
	storage             *blockscanner.BlockScannerStorage
	blockScanner        *blockscanner.BlockScanner
	signerCacheManager  *signercache.CacheManager
	rpcClient           *taorpc.Client
	globalSolvencyQueue chan stypes.Solvency
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
	signMu              sync.Mutex               // protects nonce reservation in SignTx
	nonces              map[string]nonceMetadata // vault address → nonce state (guarded by signMu)
}

// nonceMetadata holds nonce and block height for a vault.
type nonceMetadata struct {
	Nonce       uint32 `json:"Nonce"`
	BlockHeight int64  `json:"BlockHeight"`
}

// NewTAOClient creates a new instance of TAOClient.
func NewTAOClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
) (*TAOClient, error) {
	if thorKeys == nil {
		return nil, errors.New("thor keys empty")
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

	tssKm, err := tss.NewKeySign(server, bridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create tss signer: %w", err)
	}

	priv, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get Ed25519 private key: %w", err)
	}

	pub, ok := priv.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return nil, errors.New("fail to cast pubkey to ed25519")
	}

	// Format public key as SS58 address for local signing comparison
	localAddr, err := ss58.Encode(ss58.PrefixTAO, pub.Bytes())
	if err != nil {
		return nil, fmt.Errorf("fail to encode local pubkey as SS58: %w", err)
	}

	rpcClient := taorpc.NewClient(cfg.RPCHost, cfg.BlockScanner.HTTPRequestTimeout)

	c := &TAOClient{
		logger:        log.With().Str("module", "tao").Stringer("chain", cfg.ChainID).Logger(),
		cfg:           cfg,
		localPubKey:   localAddr,
		k:             priv,
		bridge:        bridge,
		poolMgr:       poolMgr,
		tssKeyManager: tssKm,
		rpcClient:     rpcClient,
		wg:            &sync.WaitGroup{},
		stopchan:      make(chan struct{}),
		nonces:        make(map[string]nonceMetadata),
	}

	// Initialize storage
	var path string
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	c.storage, err = blockscanner.NewBlockScannerStorage(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scan storage: %w", err)
	}

	c.taoScanner, err = NewTAOScanner(
		c.cfg.BlockScanner,
		c.ReportSolvency,
		rpcClient,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create tao scanner: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, c.storage, m, bridge, c.taoScanner)
	if err != nil {
		return c, fmt.Errorf("fail to create block scanner: %w", err)
	}

	signerCacheManager, err := signercache.NewSignerCacheManager(c.storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager: %w", err)
	}
	c.signerCacheManager = signerCacheManager

	return c, nil
}

// Start starts the TAO chain client.
func (c *TAOClient) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.taoScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.tssKeyManager.Start()
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.bridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
}

// Stop stops the TAO chain client.
func (c *TAOClient) Stop() {
	c.tssKeyManager.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// IsBlockScannerHealthy returns true if the block scanner is healthy.
func (c *TAOClient) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// GetConfig returns the chain configuration.
func (c *TAOClient) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

// GetChain returns the chain.
func (c *TAOClient) GetChain() common.Chain {
	return common.TAOChain
}

// GetHeight returns the current block height.
func (c *TAOClient) GetHeight() (int64, error) {
	return c.taoScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height.
func (c *TAOClient) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block scanner.
func (c *TAOClient) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

// GetLatestTxForVault returns last observed and broadcasted tx for a vault.
func (c *TAOClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadCasted, err
}

// GetAddress returns the address for the given public key.
func (c *TAOClient) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// GetAccount returns the balance of the provided pubkey.
func (c *TAOClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pk.GetAddress(common.TAOChain)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}
	return c.GetAccountByAddress(addr.String(), height)
}

// GetAccountByAddress returns account information.
func (c *TAOClient) GetAccountByAddress(addr string, height *big.Int) (common.Account, error) {
	balance, err := c.rpcClient.GetBalance(addr, "")
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get balance: %w", err)
	}

	amount := convertRaoToThorchain(balance)

	nonce, err := c.rpcClient.GetNonce(addr)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get nonce: %w", err)
	}

	taoCoin := common.NewCoin(common.TAOAsset, amount)
	taoCoin.Decimals = 9
	coins := common.Coins{taoCoin}

	return common.Account{
		Sequence: int64(nonce),
		Coins:    coins,
	}, nil
}

// --------------------------------- solvency ---------------------------------

// ShouldReportSolvency returns true if the given height is a solvency report height.
func (c *TAOClient) ShouldReportSolvency(height int64) bool {
	return c.cfg.SolvencyBlocks > 0 && height%c.cfg.SolvencyBlocks == 0
}

// ReportSolvency reports solvency once per configured solvency blocks.
// Follows the same insolvent-first pattern as XRP/SOL/ETH: reports only insolvent
// vaults during normal operation, or all-solvent when the scanner is halted.
func (c *TAOClient) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}

	// When the block scanner is not healthy, only report from the auto-unhalt
	// SolvencyCheckRunner (which passes chainHeight). FetchTxs passes
	// PreviousHeight+1, so skip those to avoid noise during halts.
	if !c.IsBlockScannerHealthy() && height == c.blockScanner.PreviousHeight()+1 {
		return nil
	}

	asgardVaults, err := c.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}

	// 1x estimated gas cost breathing room, converted to THORChain (1e8) units.
	fee := c.taoScanner.averageFee()
	if fee == 0 {
		fee = defaultFeeRao
	}
	currentGasFee := c.cfg.ChainID.NativeGasToThorchain(cosmos.NewUint(fee))

	// Separate insolvent from solvent vaults.
	msgs := make([]stypes.Solvency, 0, len(asgardVaults))
	solventMsgs := make([]stypes.Solvency, 0, len(asgardVaults))
	checkedVaults := 0
	for i := range asgardVaults {
		if asgardVaults[i].PubKeyEddsa.IsEmpty() {
			c.logger.Error().Stringer("asgard", asgardVaults[i].PubKey).Msg("asgard vault EdDSA pubkey is empty, skipping solvency check")
			continue
		}
		checkedVaults++

		acct, err := c.GetAccount(asgardVaults[i].PubKeyEddsa, nil)
		if err != nil {
			c.logger.Err(err).Stringer("asgard", asgardVaults[i].PubKey).Msg("fail to get account balance for solvency")
			continue
		}

		msg := stypes.Solvency{
			Height: height,
			Chain:  c.cfg.ChainID,
			PubKey: asgardVaults[i].PubKey,
			Coins:  acct.Coins,
		}

		if runners.IsVaultSolvent(acct, asgardVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}
		msgs = append(msgs, msg)
	}

	// Only report all-solvent when the scanner is unhealthy (solvency-halted)
	// and every checked vault is solvent. Compare against checkedVaults (not
	// len(asgardVaults)) because vaults without EdDSA keys can't hold TAO
	// and should not block auto-unhalting.
	solvent := false
	if !c.IsBlockScannerHealthy() && checkedVaults > 0 && len(solventMsgs) == checkedVaults {
		msgs = solventMsgs
		solvent = true
	}

	for i := range msgs {
		c.logger.Info().
			Stringer("asgard", msgs[i].PubKey).
			Interface("coins", msgs[i].Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")

		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("fail to send solvency info to thorchain, timeout")
		}
	}

	return nil
}

// --------------------------------- sign ---------------------------------

// SignTx returns the signed transaction.
func (c *TAOClient) SignTx(tx stypes.TxOutItem, thorchainHeight int64) (signedTx, checkpoint []byte, _ *stypes.TxInItem, err error) {
	defer func() {
		if err != nil {
			var keysignError tss.KeysignError
			if errors.As(err, &keysignError) {
				if len(keysignError.Blame.BlameNodes) == 0 {
					c.logger.Err(err).Msg("TSS doesn't know which node to blame")
					return
				}
				var txID common.TxID
				txID, err = c.bridge.PostKeysignFailure(keysignError.Blame, thorchainHeight, tx.Memo, tx.Coins, tx.VaultPubKeyEddsa)
				if err != nil {
					c.logger.Err(err).Msg("fail to post keysign failure to THORChain")
					return
				}
				c.logger.Info().Str("tx_id", txID.String()).Msg("post keysign failure to thorchain")
			}
		}
	}()

	if tx.Chain != common.TAOChain {
		return nil, nil, nil, errors.New("tx chain is not TAO")
	}
	if tx.ToAddress.IsEmpty() {
		return nil, nil, nil, errors.New("to address is empty")
	}
	if tx.VaultPubKeyEddsa.IsEmpty() {
		return nil, nil, nil, errors.New("vault Ed25519 public key is empty")
	}

	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		c.logger.Info().Interface("tx", tx).Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
	}

	// Get memo for parsing
	memoForParsing := tx.GetMemo()
	var memoObj mem.Memo
	if memoForParsing == "" {
		memoObj = mem.NewOutboundMemo(tx.InHash)
	} else {
		memoObj, err = mem.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to parse memo(%s): %w", memoForParsing, err)
		}
		if memoObj.IsInbound() {
			return nil, nil, nil, errors.New("inbound memo should not be used for outbound tx")
		}
	}

	if len(tx.Coins) != 1 {
		return nil, nil, nil, fmt.Errorf("only support one coin per transaction, received: %d", len(tx.Coins))
	}

	// Get vault address
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(common.TAOChain)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get vault address: %w", err)
	}

	// Read block hash + height atomically — used for both nonce management and
	// mortal era construction (era phase must match the block hash).
	latestBlockHash, currentHeight := c.taoScanner.getLatestBlock()

	// Reserve nonce under lock to prevent concurrent SignTx calls from getting the same nonce
	reservedNonce, checkpointBytes, err := c.reserveNonce(tx, vaultAddr.String(), currentHeight)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build the extrinsic (uses consistent block hash/height for mortal era)
	rawTx, err := c.createAndSignExtrinsic(tx, vaultAddr.String(), reservedNonce, latestBlockHash, currentHeight)
	if err != nil {
		// If the scanner hasn't observed any blocks, the nonce was reserved at height 0
		// without on-chain verification. Don't return the checkpoint — on retry (once the
		// scanner is running) the nonce will be freshly fetched from the chain. Returning
		// a stale checkpoint here would cause the retry to use a potentially wrong nonce.
		if latestBlockHash == "" {
			return nil, nil, nil, fmt.Errorf("fail to create and sign extrinsic: %w", err)
		}
		return nil, checkpointBytes, nil, fmt.Errorf("fail to create and sign extrinsic: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic) — matches scanner
	txHash := taotypes.ExtrinsicHash(rawTx)

	// Create instant observation TxInItem
	if tx.GasRate < 0 {
		return nil, nil, nil, fmt.Errorf("negative gas rate: %d", tx.GasRate)
	}
	gasRate := c.cfg.ChainID.NativeGasToThorchain(cosmos.NewUint(uint64(tx.GasRate)))
	if gasRate.IsZero() {
		gasRate = cosmos.OneUint()
	}
	gasCoin := common.NewCoin(common.TAOAsset, gasRate)
	gasCoin.Decimals = 9

	txIn := stypes.NewTxInItem(
		currentHeight,
		txHash,
		tx.Memo,
		vaultAddr.String(),
		tx.ToAddress.String(),
		tx.Coins,
		common.Gas([]common.Coin{gasCoin}),
		tx.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	// Return checkpoint so the signer can retry with the same nonce if broadcast
	// fails. This is important because TAO extrinsics use mortal era (64 blocks)
	// and the signer pipeline stores SignedTx for broadcast retry without re-signing.
	// Returning the checkpoint ensures re-signing with a fresh mortal era on retry.
	return rawTx, checkpointBytes, txIn, nil
}

// createAndSignExtrinsic builds and signs a Substrate extrinsic.
// latestBlockHash and latestBlockHeight must come from a single atomic read
// (via getLatestBlock) to ensure the mortal era phase matches the block hash.
func (c *TAOClient) createAndSignExtrinsic(tx stypes.TxOutItem, vaultAddr string, nonce uint32, latestBlockHash string, latestBlockHeight int64) ([]byte, error) {
	// Decode destination address
	_, destPubkey, err := ss58.Decode(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("fail to decode destination address: %w", err)
	}

	// Convert amount to rao (1e8 * 10 = 1e9) with overflow check
	amount, err := convertToRao(tx.Coins[0].Amount)
	if err != nil {
		return nil, fmt.Errorf("fail to convert amount to rao: %w", err)
	}

	// Build call data
	transferCall := taotypes.BalanceTransferKeepAlive(
		c.taoScanner.balancesPalletIndex,
		c.taoScanner.transferCallIndex,
		destPubkey,
		amount,
	)

	var callData []byte
	if tx.Memo != "" {
		// Batch transfer + remark for memo
		remarkCall := taotypes.SystemRemark(
			c.taoScanner.systemPalletIndex,
			c.taoScanner.remarkCallIndex,
			[]byte(tx.Memo),
		)
		callData = taotypes.BatchAll(
			c.taoScanner.utilityPalletIndex,
			c.taoScanner.batchAllCallIndex,
			[][]byte{transferCall, remarkCall},
		)
	} else {
		callData = transferCall
	}

	// Get signing parameters
	genesisHashBytes, err := taotypes.HexDecode(c.taoScanner.getGenesisHash())
	if err != nil {
		return nil, fmt.Errorf("fail to decode genesis hash: %w", err)
	}

	// Use the caller-provided block hash + height for mortal era.
	// Both come from a single atomic read, preventing era/hash mismatch
	// that would cause the Substrate runtime to reject the transaction.
	blockHash := latestBlockHash
	eraBlockHeight := latestBlockHeight
	if blockHash == "" {
		return nil, fmt.Errorf("scanner has not observed any blocks, cannot construct mortal era")
	}
	blockHashBytes, err := taotypes.HexDecode(blockHash)
	if err != nil {
		return nil, fmt.Errorf("fail to decode block hash: %w", err)
	}

	// Use mortal era so the transaction expires (prevents replay)
	era := taotypes.MortalEra(mortalEraPeriod, uint64(eraBlockHeight))

	// Read spec + tx version atomically (both change together on runtime upgrades)
	specVersion, txVersion := c.taoScanner.getRuntimeVersion()

	// Construct signing payload
	payload := taotypes.SigningPayload(
		callData,
		era,
		uint64(nonce),
		0, // tip = 0
		taotypes.MetadataModeDisabled,
		specVersion,
		txVersion,
		genesisHashBytes,
		blockHashBytes,
	)

	// Sign the payload
	// If payload > 256 bytes, hash it first (Substrate convention)
	signingData := payload
	if len(payload) > 256 {
		hash := blake2b.Sum256(payload)
		signingData = hash[:]
	}

	var sig []byte

	// Get vault public key bytes for the signer field
	_, vaultPubkeyBytes, err := ss58.Decode(vaultAddr)
	if err != nil {
		return nil, fmt.Errorf("fail to decode vault address: %w", err)
	}

	if vaultAddr == c.localPubKey {
		// Local signing
		sig, err = c.k.Sign(signingData)
		if err != nil {
			return nil, fmt.Errorf("fail to sign locally: %w", err)
		}
	} else {
		// TSS signing
		sig, _, err = c.tssKeyManager.RemoteSign(signingData, common.SigningAlgoEd25519, tx.VaultPubKeyEddsa.String())
		if err != nil {
			return nil, fmt.Errorf("fail to sign via TSS: %w", err)
		}
	}

	// Verify signature
	if !ed25519.Verify(vaultPubkeyBytes, signingData, sig) {
		return nil, errors.New("signature verification failed")
	}

	// Assemble the signed extrinsic
	ext := &taotypes.Extrinsic{
		Signer:        vaultPubkeyBytes,
		SignatureType: taotypes.SignatureTypeEd25519,
		Signature:     sig,
		Era:           era,
		Nonce:         uint64(nonce),
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      callData,
	}

	return taotypes.EncodeSignedExtrinsic(ext), nil
}

// --------------------------------- broadcast ---------------------------------

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *TAOClient) BroadcastTx(txOutItem stypes.TxOutItem, rawTx []byte) (string, error) {
	// Compute hash locally for consistency with the scanner's extrinsicHash.
	txHash := taotypes.ExtrinsicHash(rawTx)

	hexTx := taotypes.HexEncode(rawTx)
	nodeHash, err := c.rpcClient.SubmitExtrinsic(hexTx)
	if err != nil {
		errMsg := err.Error()
		switch {
		// Tx already in the pool — another bifrost instance submitted first.
		// Same pattern as XRP (tefPAST_SEQ) and Cosmos (CosmosSuccessCodes).
		case strings.Contains(errMsg, "AlreadyImported") || strings.Contains(errMsg, "already imported"):
			c.logger.Info().Str("txid", txHash).Msg("extrinsic already in pool, treating as success")

		// Stale nonce — the on-chain nonce is ahead of this tx's nonce, meaning
		// a tx with this nonce already confirmed. Treat as success.
		case strings.Contains(errMsg, "Stale"):
			c.logger.Info().Str("txid", txHash).Msg("extrinsic has stale nonce (already confirmed), treating as success")

		default:
			return "", fmt.Errorf("fail to broadcast tx: %w", err)
		}
	}

	if nodeHash != "" && nodeHash != txHash {
		c.logger.Warn().Str("local", txHash).Str("node", nodeHash).Msg("extrinsic hash mismatch between local and node")
	}
	c.logger.Info().Str("memo", txOutItem.Memo).Str("txid", txHash).Msg("broadcast tx")

	if err := c.signerCacheManager.SetSigned(txOutItem.CacheHash(), txOutItem.CacheVault(c.GetChain()), txHash); err != nil {
		c.logger.Err(err).Interface("txOutItem", txOutItem).Msg("fail to mark tx out item as signed")
	}

	return txHash, nil
}

// --------------------------------- observe ---------------------------------

// OnObservedTxIn is called when a new observed tx is received.
func (c *TAOClient) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	m, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
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

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *TAOClient) GetConfirmationCount(txIn stypes.TxIn) int64 {
	// Bittensor has finality once block is finalized
	return 0
}

// ConfirmationCountReady returns true if the confirmation count is ready.
func (c *TAOClient) ConfirmationCountReady(txIn stypes.TxIn) bool {
	return true
}

// --------------------------------- helpers ---------------------------------

// reserveNonce atomically reads, reserves, and pre-increments a nonce for the
// given vault. Returns the reserved nonce and its JSON checkpoint.
func (c *TAOClient) reserveNonce(tx stypes.TxOutItem, vaultAddr string, currentHeight int64) (uint32, []byte, error) {
	// If this is a retry (checkpoint present), use the checkpoint nonce directly
	// without pre-incrementing — the nonce was already incremented on the first attempt.
	if tx.Checkpoint != nil {
		var meta nonceMetadata
		if err := json.Unmarshal(tx.Checkpoint, &meta); err != nil {
			return 0, nil, fmt.Errorf("fail to unmarshal checkpoint: %w", err)
		}
		c.logger.Debug().
			Str("vault", vaultAddr).
			Uint32("nonce", meta.Nonce).
			Int64("height", currentHeight).
			Bool("checkpoint", true).
			Msg("nonce reserved")
		return meta.Nonce, tx.Checkpoint, nil
	}

	// Fetch on-chain nonce BEFORE acquiring the lock to avoid holding
	// the mutex over network I/O.
	var onChainNonce uint32
	var fetchedOnChain bool
	cachedMeta := c.getCachedNonce(vaultAddr)
	if currentHeight > cachedMeta.BlockHeight {
		c.logger.Debug().Str("vault", vaultAddr).Int64("height", currentHeight).Msg("fetching on-chain nonce")
		nonce, err := c.rpcClient.GetNonce(vaultAddr)
		if err != nil {
			return 0, nil, fmt.Errorf("fail to get nonce: %w", err)
		}
		onChainNonce = nonce
		fetchedOnChain = true
	}

	c.signMu.Lock()
	defer c.signMu.Unlock()

	meta := c.nonces[vaultAddr]
	if fetchedOnChain && meta.Nonce <= onChainNonce {
		if meta.Nonce < onChainNonce {
			c.logger.Info().
				Str("vault", vaultAddr).
				Uint32("old", meta.Nonce).
				Uint32("new", onChainNonce).
				Msg("on-chain nonce advanced")
		}
		meta = nonceMetadata{
			Nonce:       onChainNonce,
			BlockHeight: currentHeight,
		}
	} else if fetchedOnChain && meta.Nonce > onChainNonce && currentHeight >= meta.BlockHeight+mortalEraPeriod {
		// If cached nonce is ahead of on-chain and mortalEraPeriod blocks have passed,
		// any pending transactions must have expired. Reset to on-chain nonce to recover
		// from broadcast failures that left the cache stuck ahead.
		c.logger.Warn().
			Str("vault", vaultAddr).
			Uint32("cached", meta.Nonce).
			Uint32("on_chain", onChainNonce).
			Int64("stale_height", meta.BlockHeight).
			Int64("current_height", currentHeight).
			Msg("nonce cache stale after mortal era expiry, resetting to on-chain")
		meta = nonceMetadata{
			Nonce:       onChainNonce,
			BlockHeight: currentHeight,
		}
	}

	reservedNonce := meta.Nonce
	checkpoint, err := json.Marshal(meta)
	if err != nil {
		return 0, nil, fmt.Errorf("fail to marshal checkpoint: %w", err)
	}

	// Pre-increment so the next concurrent call gets a different nonce
	meta.Nonce++
	c.nonces[vaultAddr] = meta

	c.logger.Debug().
		Str("vault", vaultAddr).
		Uint32("nonce", reservedNonce).
		Int64("height", currentHeight).
		Bool("checkpoint", false).
		Msg("nonce reserved")

	return reservedNonce, checkpoint, nil
}

// getCachedNonce returns the cached nonce metadata for a vault address.
func (c *TAOClient) getCachedNonce(vaultAddr string) nonceMetadata {
	c.signMu.Lock()
	defer c.signMu.Unlock()
	return c.nonces[vaultAddr]
}

// convertToRao converts THORChain amount (1e8) to rao (1e9) with overflow check.
func convertToRao(amount cosmos.Uint) (uint64, error) {
	bi := amount.BigInt()
	if !bi.IsUint64() {
		return 0, fmt.Errorf("amount overflow converting to rao: %s", amount)
	}
	v := bi.Uint64()
	result := v * 10
	if result/10 != v {
		return 0, fmt.Errorf("amount overflow converting to rao: %s", amount)
	}
	return result, nil
}
