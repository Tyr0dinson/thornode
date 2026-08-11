package substrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/generic"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"golang.org/x/crypto/blake2b"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	tcmetrics "gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	ttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	tcconfig "gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

const (
	defaultMaxPendingNonces = 3
	migrationTxCacheBlocks  = 100
)

// Client is a generic client for Substrate blockchains.
type Client struct {
	*generic.Client
	logger              zerolog.Logger
	storage             *blockscanner.BlockScannerStorage
	blockScanner        *blockscanner.BlockScanner
	chainScanner        *Scanner
	rpcClient           *rpc.Client
	globalSolvencyQueue chan ttypes.Solvency
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
	signMu              sync.Mutex               // protects nonce reservation in SignTx
	nonces              map[string]nonceMetadata // vault address → nonce state (guarded by signMu)
	migrationTxCache    *migrationTxCache
	stateStore          *stateStore
}

// nonceMetadata holds nonce and block height for a vault.
type nonceMetadata struct {
	Nonce       uint32 `json:"Nonce"`
	BlockHeight int64  `json:"BlockHeight"`
}

// NewClient creates a new instance of Client.
func NewClient(
	thorKeys *thorclient.Keys,
	config tcconfig.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	metrics *tcmetrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
) (*Client, error) {
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}

	// default
	if config.MaxPendingNonces == 0 {
		config.MaxPendingNonces = defaultMaxPendingNonces
	}

	logger := log.With().
		Str("module", "substrate").
		Stringer("chain", config.ChainID).
		Logger()

	c := &Client{
		logger:           logger,
		wg:               &sync.WaitGroup{},
		stopchan:         make(chan struct{}),
		nonces:           make(map[string]nonceMetadata),
		migrationTxCache: newMigrationTxCache(),
	}

	var err error

	var path string
	if len(config.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", config.BlockScanner.DBPath, config.BlockScanner.ChainID)
	}
	c.storage, err = blockscanner.NewBlockScannerStorage(path, config.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("failed to create scan storage: %w", err)
	}

	c.Client, err = generic.NewClient(
		logger, thorKeys, config, server, bridge, c.storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	c.stateStore, err = newStateStore(c.Client.StateStore())
	if err != nil {
		return nil, fmt.Errorf("failed to create substrate state store: %w", err)
	}

	c.rpcClient = rpc.NewClient(
		config.RPCHost,
		config.BlockScanner.HTTPRequestTimeout,
	)

	c.chainScanner, err = NewScanner(
		config.BlockScanner,
		c.reportSolvencyFromScanner,
		c.rpcClient,
		pubkeyMgr,
		c.handleFinalizedFailure,
	)
	if err != nil {
		return c, fmt.Errorf("failed to create chain scanner: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(
		config.BlockScanner,
		c.storage,
		metrics,
		bridge,
		c.chainScanner,
	)
	if err != nil {
		return c, fmt.Errorf("failed to create block scanner: %w", err)
	}

	return c, nil
}

// Start starts the Substrate chain client.
func (c *Client) Start(
	globalTxsQueue chan ttypes.TxIn,
	_ chan ttypes.ErrataBlock,
	globalSolvencyQueue chan ttypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.chainScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.Client.Start()
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.GetBridge(), c.stopchan, c.wg, constants.ThorchainBlockTime)
}

// Stop stops the Substrate chain client.
func (c *Client) Stop() {
	c.Client.Stop()
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// IsBlockScannerHealthy returns true if the block scanner is healthy.
func (c *Client) IsBlockScannerHealthy() bool {
	return c.blockScanner.IsHealthy()
}

// GetHeight returns the current block height.
func (c *Client) GetHeight() (int64, error) {
	return c.chainScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height.
func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

// RollbackBlockScanner rolls back the block scanner.
func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

// GetAddress returns the address for the given public key.
func (c *Client) GetAddress(pubkey common.PubKey) string {
	addr, err := pubkey.GetAddress(c.GetChain())
	if err != nil {
		c.logger.Err(err).Str("pub_key", pubkey.String()).Msg("failed to get pool address")
		return ""
	}
	return addr.String()
}

// GetAccount returns the balance of the provided pubkey.
func (c *Client) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pk.GetAddress(c.GetChain())
	if err != nil {
		return common.Account{}, fmt.Errorf("failed to get address from pubkey(%s): %w", pk, err)
	}
	return c.GetAccountByAddress(addr.String(), height)
}

// GetAccountByAddress returns account information.
func (c *Client) GetAccountByAddress(addr string, height *big.Int) (common.Account, error) {
	balance, err := c.rpcClient.GetBalance(addr, "", c.chainScanner.params.BalanceSize)
	if err != nil {
		return common.Account{}, fmt.Errorf("failed to get balance: %w", err)
	}

	nonce, err := c.rpcClient.GetNonce(addr)
	if err != nil {
		return common.Account{}, fmt.Errorf("failed to get nonce: %w", err)
	}

	coin := c.GetChain().NativeGasToThorCoin(balance)
	coins := common.Coins{coin}

	return common.Account{
		Sequence: int64(nonce),
		Coins:    coins,
	}, nil
}

// ReportSolvency reports solvency once per configured solvency blocks.
// Follows the same insolvent-first pattern as XRP/SOL/ETH: reports only insolvent
// vaults during normal operation, or all-solvent when the scanner is halted.
func (c *Client) ReportSolvency(height int64) error {
	return c.reportSolvency(height)
}

func (c *Client) reportSolvencyFromScanner(height int64) error {
	if !c.IsBlockScannerHealthy() {
		return nil
	}

	return c.reportSolvency(height)
}

func (c *Client) reportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}

	asgardVaults, err := c.GetBridge().GetAsgards()
	if err != nil {
		return fmt.Errorf("failed to get asgards: %w", err)
	}

	// 1x estimated gas cost breathing room, converted to THORChain (1e8) units.
	_, fee := c.chainScanner.GetNetworkFee()
	currentGasFee := c.GetChain().NativeGasToThorchain(cosmos.NewUint(fee))

	// Separate insolvent from solvent vaults.
	msgs := make([]ttypes.Solvency, 0, len(asgardVaults))
	solventMsgs := make([]ttypes.Solvency, 0, len(asgardVaults))
	checkedVaults := 0
	for i := range asgardVaults {
		if asgardVaults[i].PubKeyEddsa.IsEmpty() {
			c.logger.Error().Stringer("asgard", asgardVaults[i].PubKey).Msg("asgard vault EdDSA pubkey is empty, skipping solvency check")
			continue
		}
		checkedVaults++

		acct, err := c.GetAccount(asgardVaults[i].PubKeyEddsa, nil)
		if err != nil {
			c.logger.Err(err).Stringer("asgard", asgardVaults[i].PubKey).Msg("failed to get account balance for solvency")
			continue
		}

		msg := ttypes.Solvency{
			Height: height,
			Chain:  c.GetChain(),
			PubKey: asgardVaults[i].PubKey,
			Coins:  acct.Coins,
		}

		if runners.IsVaultSolvent(c.GetChain(), acct, asgardVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}
		msgs = append(msgs, msg)
	}

	// Only report all-solvent when the scanner is unhealthy (solvency-halted)
	// and every checked vault is solvent. Compare against checkedVaults (not
	// len(asgardVaults)) because vaults without EdDSA keys can't hold assets
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
			c.logger.Info().Msg("failed to send solvency info to thorchain, timeout")
		}
	}

	return nil
}

// SignTx returns the signed transaction.
func (c *Client) SignTx(tx ttypes.TxOutItem, thorchainHeight int64) (signedTx, checkpoint []byte, _ *ttypes.TxInItem, err error) {
	if tx.Chain != c.GetChain() {
		return nil, nil, nil, errors.New("tx chain not supported")
	}
	if tx.ToAddress.IsEmpty() {
		return nil, nil, nil, errors.New("to address is empty")
	}
	if tx.VaultPubKeyEddsa.IsEmpty() {
		return nil, nil, nil, errors.New("vault Ed25519 public key is empty")
	}
	if len(tx.Coins) != 1 {
		return nil, nil, nil, fmt.Errorf("only support one coin per transaction, received: %d", len(tx.Coins))
	}
	if tx.GasRate < 0 {
		return nil, nil, nil, fmt.Errorf("negative gas rate: %d", tx.GasRate)
	}
	gasAsset := c.GetChain().GetGasAsset()
	if !tx.Coins[0].Asset.Equals(gasAsset) {
		return nil, nil, nil, fmt.Errorf("substrate clients only support the native gas asset %s, got %s", gasAsset, tx.Coins[0].Asset)
	}

	// Get memo for parsing
	memoForParsing := tx.GetMemo()
	var memoObj mem.Memo
	if memoForParsing != "" {
		memoObj, err = mem.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse memo(%s): %w", memoForParsing, err)
		}
		if memoObj.IsInbound() {
			return nil, nil, nil, errors.New("inbound memo should not be used for outbound tx")
		}
	}

	// Get vault address
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get vault address: %w", err)
	}

	// Read block hash + height atomically — used for both nonce management and
	// mortal era construction (era phase must match the block hash).
	latestBlockHash, currentHeight := c.chainScanner.getLatestBlock()
	if c.HasSigned(tx) {
		var txHash string
		txHash, err = c.GetSignedTransactionHash(tx)
		if err != nil {
			c.logger.Err(err).
				Interface("tx", tx).
				Msg("failed to get signed transaction hash, keeping signer cache entry")
			return nil, nil, nil, nil
		}
		var canRetry bool
		canRetry, err = c.canRetrySignedTx(txHash, vaultAddr.String(), latestBlockHash, currentHeight)
		if err != nil {
			c.logger.Err(err).
				Interface("tx", tx).
				Msg("failed to verify signed transaction expiry, keeping signer cache entry")
			return nil, nil, nil, nil
		}
		if !canRetry {
			c.logger.Info().
				Interface("tx", tx).
				Msg("transaction already signed, ignoring...")
			return nil, nil, nil, nil
		}
		err = c.releaseExpiredSignedTx(txHash)
		if err != nil {
			c.logger.Err(err).Interface("tx", tx).
				Msg("failed to release expired signed transaction")
			return nil, nil, nil, nil
		}
		c.logger.Info().Str("txid", txHash).Int64("height", currentHeight).
			Msg("released expired transaction for re-signing")
	}

	// Reserve nonce under lock to prevent concurrent SignTx calls from getting the same nonce
	reservedNonce, checkpointBytes, err := c.reserveNonce(tx, vaultAddr.String(), currentHeight)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build the extrinsic (uses consistent block hash/height for mortal era)
	rawTx, err := c.createAndSignExtrinsic(
		tx,
		vaultAddr.String(),
		reservedNonce,
		latestBlockHash,
		currentHeight,
		thorchainHeight,
	)
	if err != nil {
		// If the scanner hasn't observed any blocks, the nonce was reserved at height 0
		// without on-chain verification. Don't return the checkpoint — on retry (once the
		// scanner is running) the nonce will be freshly fetched from the chain. Returning
		// a stale checkpoint here would cause the retry to use a potentially wrong nonce.
		if latestBlockHash == "" {
			checkpointBytes = nil
		}
		return nil, checkpointBytes, nil, fmt.Errorf("failed to create and sign extrinsic: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic) — matches scanner
	txHash := stypes.ExtrinsicHash(rawTx)

	// Create instant observation TxInItem
	gasCoin := c.GetChain().NativeGasToThorCoinCeil(cosmos.NewUint(uint64(tx.GasRate)))
	if gasCoin.Amount.IsZero() {
		gasCoin.Amount = cosmos.OneUint()
	}

	txIn := ttypes.NewTxInItem(
		currentHeight,
		txHash,
		tx.Memo,
		vaultAddr.String(),
		tx.ToAddress.String(),
		tx.Coins,
		[]common.Coin{gasCoin},
		tx.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	// Return the checkpoint with the signed extrinsic. Failed broadcasts are
	// retried by re-broadcasting the stored SignedTx rather than re-signing, so the
	// mortal era is preserved and an unlandable tx expires on its own. The checkpoint
	// is only used to reuse this nonce if a keysign-failure retry must re-sign.
	return rawTx, checkpointBytes, txIn, nil
}

// createAndSignExtrinsic builds and signs a Substrate extrinsic.
// latestBlockHash and latestBlockHeight must come from a single atomic read
// (via getLatestBlock) to ensure the mortal era phase matches the block hash.
func (c *Client) createAndSignExtrinsic(
	tx ttypes.TxOutItem,
	vaultAddr string,
	nonce uint32,
	latestBlockHash string,
	latestBlockHeight int64,
	thorchainHeight int64,
) ([]byte, error) {
	// Decode destination address
	_, destPubkey, err := ss58.Decode(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode destination address: %w", err)
	}

	if bytes.Equal(destPubkey, make([]byte, 32)) {
		return nil, fmt.Errorf("refusing to send to null address: %s", tx.ToAddress)
	}

	amount := c.GetChain().ThorchainToNativeGas(tx.Coins[0].Amount)

	// Build call data
	transferCall := stypes.BalanceTransferKeepAlive(
		c.chainScanner.params.Index(BalancesPallet),
		c.chainScanner.params.Index(BalancesTransferKeepAliveCall),
		destPubkey,
		amount,
	)

	var callData []byte
	if tx.Memo != "" {
		// Batch transfer + remark for memo
		remarkCall := stypes.SystemRemark(
			c.chainScanner.params.Index(SystemPallet),
			c.chainScanner.params.Index(SystemRemarkCall),
			[]byte(tx.Memo),
		)
		callData = stypes.BatchAll(
			c.chainScanner.params.Index(UtilityPallet),
			c.chainScanner.params.Index(UtilityBatchAllCall),
			[][]byte{transferCall, remarkCall},
		)
	} else {
		callData = transferCall
	}

	// Get signing parameters
	genesisHashBytes, err := stypes.HexDecode(c.chainScanner.getGenesisHash())
	if err != nil {
		return nil, fmt.Errorf("failed to decode genesis hash: %w", err)
	}

	// Use the caller-provided block hash + height for mortal era.
	// Both come from a single atomic read, preventing era/hash mismatch
	// that would cause the Substrate runtime to reject the transaction.
	blockHash := latestBlockHash
	eraBlockHeight := uint64(latestBlockHeight)
	if blockHash == "" {
		return nil, fmt.Errorf("scanner has not observed any blocks, cannot construct mortal era")
	}
	blockHashBytes, err := stypes.HexDecode(blockHash)
	if err != nil {
		return nil, fmt.Errorf("failed to decode block hash: %w", err)
	}

	// Use mortal era so the transaction expires (prevents replay)
	period := uint64(c.chainScanner.params.MortalEraPeriod)
	era := stypes.MortalEra(period, eraBlockHeight)

	// Read spec + tx version atomically (both change together on runtime upgrades)
	specVersion, txVersion := c.chainScanner.getRuntimeVersion()

	// Construct signing payload
	var payload []byte
	payload, err = stypes.SigningPayload(
		callData,
		era,
		uint64(nonce),
		0, // tip = 0
		c.chainScanner.params.CheckMetadataHash,
		stypes.MetadataModeDisabled,
		c.chainScanner.params.ChargeAssetTxPayment,
		specVersion,
		txVersion,
		genesisHashBytes,
		blockHashBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to construct payload: %w", err)
	}

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
		return nil, fmt.Errorf("failed to decode vault address: %w", err)
	}

	sig, err = c.Sign(signingData, tx, thorchainHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Assemble the signed extrinsic
	ext := &stypes.Extrinsic{
		Signer:               vaultPubkeyBytes,
		SignatureType:        stypes.SignatureTypeEd25519,
		Signature:            sig,
		Era:                  era,
		Nonce:                uint64(nonce),
		Tip:                  0,
		MetadataMode:         stypes.MetadataModeDisabled,
		CheckMetadataHash:    c.chainScanner.params.CheckMetadataHash,
		ChargeAssetTxPayment: c.chainScanner.params.ChargeAssetTxPayment,
		CallData:             callData,
	}

	return stypes.EncodeSignedExtrinsic(ext), nil
}

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *Client) BroadcastTx(txOutItem ttypes.TxOutItem, rawTx []byte) (string, error) {
	// Compute hash locally for consistency with the scanner's extrinsicHash.
	txHash := stypes.ExtrinsicHash(rawTx)
	extrinsic, err := stypes.ParseSignedExtrinsic(rawTx, c.chainScanner.params.CheckMetadataHash, c.chainScanner.params.ChargeAssetTxPayment)
	if err != nil {
		return "", fmt.Errorf("failed to parse extrinsic before broadcast: %w", err)
	}
	if extrinsic.Nonce > math.MaxUint32 {
		return "", fmt.Errorf("extrinsic nonce exceeds uint32: %d", extrinsic.Nonce)
	}
	vaultAddr := c.GetAddress(txOutItem.GetVaultPubKey())
	_, height := c.chainScanner.getLatestBlock()
	record := signedExtrinsicRecord{
		TxHash:        txHash,
		VaultAddress:  vaultAddr,
		Nonce:         uint32(extrinsic.Nonce),
		RecheckHeight: height + int64(c.chainScanner.params.MortalEraPeriod),
	}

	err = c.stateStore.PutExtrinsic(record)
	if err != nil {
		return "", fmt.Errorf("failed to persist extrinsic recovery state: %w", err)
	}

	nodeHash, err := c.rpcClient.SubmitExtrinsic(stypes.HexEncode(rawTx))
	if err != nil {
		switch {
		// Tx already in the pool — another bifrost instance submitted first.
		// Same pattern as XRP (tefPAST_SEQ) and Cosmos (CosmosSuccessCodes).
		case rpc.IsAlreadyImportedErr(err):
			c.logger.Info().Str("txid", txHash).Msg("extrinsic already in pool, treating as success")
		case rpc.IsStaleNonceErr(err):
			// Stale means the nonce slot was burned. Usually that's our prior
			// submission already confirming, but it can also be an external burn.
			// The migration cache tells us which: a hit means the scanner saw the
			// tx land — treat as success; a miss means abandon, since re-broad-
			// casting risks a double-spend.
			cacheKey := migrationTxCacheKey(vaultAddr, extrinsic.Nonce)
			if !c.migrationTxCache.Has(cacheKey) {
				c.logger.Err(err).Str("txid", txHash).Msg("stale nonce, no scanner observation")

				err = c.stateStore.DeleteExtrinsic(txHash)
				if err != nil {
					c.logger.Err(err).Str("txid", txHash).Msg("failed to delete abandoned extrinsic recovery state")
				}
				return "", types.ErrAbandonTxOut
			}
			c.logger.Info().Str("txid", txHash).Msg("stale nonce but already observed by scanner")
		default:
			return "", fmt.Errorf("failed to broadcast tx: %w", err)
		}
	}

	if nodeHash != "" && nodeHash != txHash {
		c.logger.Warn().Str("local", txHash).Str("node", nodeHash).Msg("extrinsic hash mismatch between local and node")
	}
	c.logger.Info().Str("memo", txOutItem.Memo).Str("txid", txHash).Msg("broadcast tx")

	err = c.SetSigned(txOutItem, txHash)
	if err != nil {
		return "", fmt.Errorf("failed to record broadcast transaction in signer cache: %w", err)
	}

	return txHash, nil
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *Client) GetConfirmationCount(txIn ttypes.TxIn) int64 {
	// Substrate chains have deterministic finality once a block is finalized
	return 0
}

// ConfirmationCountReady returns true if the confirmation count is ready.
func (c *Client) ConfirmationCountReady(txIn ttypes.TxIn) bool {
	return true
}

// OnObservedTxIn extends the generic outbound-memo dedup with substrate-
// specific tracking of vault-as-sender internal-memo (Migrate / Consolidate)
// extrinsics. The substrate migration cache is consulted from BroadcastTx's
// Stale handler to distinguish "our prior submission already confirmed" from
// "abandon" for internal flows whose memos carry no OUT:txid anchor.
func (c *Client) OnObservedTxIn(txIn ttypes.TxInItem, height int64) {
	// use generic client for signer cache
	if !c.isFailedOutboundObservation(txIn) {
		c.Client.OnObservedTxIn(txIn, height)
	}

	// substrate specific migration cache
	ok, _ := c.chainScanner.pubkeyMgr.IsValidPoolAddress(txIn.Sender, c.GetChain())
	if !ok {
		return
	}
	memo, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil || !memo.IsInternal() {
		return
	}

	nonce, found := c.chainScanner.nonceCache.Get(txIn.Tx)
	if !found {
		c.logger.Debug().
			Str("tx", txIn.Tx).
			Msg("no nonce found in cache")
		return
	}
	cacheKey := migrationTxCacheKey(txIn.Sender, nonce)
	c.migrationTxCache.Add(cacheKey, uint64(height))
	c.migrationTxCache.Purge(height - migrationTxCacheBlocks)
}

// handleFinalizedFailure removes signer and recovery state for a finalized failed extrinsic.
func (c *Client) handleFinalizedFailure(txHash string) error {
	err := c.RemoveSigned(txHash)
	if err != nil {
		return fmt.Errorf("failed to remove finalized failed transaction from signer cache: %w", err)
	}
	err = c.stateStore.DeleteExtrinsic(txHash)
	if err != nil {
		return fmt.Errorf("failed to remove finalized failed transaction recovery state: %w", err)
	}
	return nil
}

// canRetrySignedTx reports whether an expired signed transaction can be retried from finalized account state.
func (c *Client) canRetrySignedTx(
	txHash string,
	vaultAddr, finalizedHash string,
	finalizedHeight int64,
) (bool, error) {
	if txHash == "" {
		return false, fmt.Errorf("signed transaction hash is missing")
	}
	record, found, err := c.stateStore.GetExtrinsic(txHash)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("signed extrinsic recovery record is missing")
	}
	if record.VaultAddress != vaultAddr {
		return false, fmt.Errorf("signed extrinsic vault mismatch")
	}
	if finalizedHeight < record.RecheckHeight {
		return false, nil
	}
	if finalizedHash == "" {
		return false, fmt.Errorf("finalized scanner block hash is empty")
	}
	nonce, _, err := c.rpcClient.GetAccountInfo(
		vaultAddr,
		finalizedHash,
		c.chainScanner.params.BalanceSize,
	)
	if err != nil {
		return false, fmt.Errorf("failed to get finalized account nonce: %w", err)
	}
	if nonce > record.Nonce {
		return false, nil
	}
	return true, nil
}

// releaseExpiredSignedTx removes signer state and then best-effort recovery state after an expiry check.
func (c *Client) releaseExpiredSignedTx(txHash string) error {
	err := c.RemoveSigned(txHash)
	if err != nil {
		return fmt.Errorf("failed to remove expired transaction from signer cache: %w", err)
	}
	err = c.stateStore.DeleteExtrinsic(txHash)
	if err != nil {
		c.logger.Error().Err(err).Str("txid", txHash).
			Msg("failed to delete expired extrinsic recovery state")
	}
	return nil
}

// isFailedOutboundObservation identifies the synthetic self-referencing observation emitted for a failed outbound.
func (c *Client) isFailedOutboundObservation(txIn ttypes.TxInItem) bool {
	if len(txIn.Coins) != 1 || !txIn.Coins[0].Asset.Equals(c.GetGasAsset()) ||
		!txIn.Coins[0].Amount.Equal(cosmos.OneUint()) {
		return false
	}
	memoObj, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil || !memoObj.IsOutbound() {
		return false
	}
	txID, err := common.NewTxID(txIn.Tx)
	return err == nil && memoObj.GetTxID().Equals(txID)
}

// --------------------------------- helpers ---------------------------------

// reserveNonce atomically reads, reserves, and pre-increments a nonce for the
// given vault. Returns the reserved nonce and its JSON checkpoint.
func (c *Client) reserveNonce(tx ttypes.TxOutItem, vaultAddr string, currentHeight int64) (uint32, []byte, error) {
	// If this is a retry (checkpoint present), use the checkpoint nonce directly
	// without pre-incrementing — the nonce was already incremented on the first attempt.
	if tx.Checkpoint != nil {
		var meta nonceMetadata
		err := json.Unmarshal(tx.Checkpoint, &meta)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to unmarshal checkpoint: %w", err)
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
			return 0, nil, fmt.Errorf("failed to get nonce: %w", err)
		}
		onChainNonce = nonce
		fetchedOnChain = true
	}

	c.signMu.Lock()
	defer c.signMu.Unlock()

	period := int64(c.chainScanner.params.MortalEraPeriod)

	meta := c.nonces[vaultAddr]
	if fetchedOnChain {
		if meta.Nonce <= onChainNonce {
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
		} else {
			if currentHeight >= meta.BlockHeight+period {
				// If the cached nonce is ahead of on-chain and MortalEraPeriod blocks have
				// passed, any pending transaction has expired without landing. Reset to the
				// on-chain nonce to recover from a stuck outbound that left the cache ahead.
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
			} else if uint64(meta.Nonce-onChainNonce) > c.GetConfig().MaxPendingNonces {
				// Cap on pending nonces: when the cache has drifted too far ahead of
				// on-chain, refuse to sign rather than queue more transactions that
				// will all expire via mortal era. An unset max_pending_nonces (0)
				// means "no pending allowed".
				c.logger.Warn().
					Str("vault", vaultAddr).
					Uint32("cached", meta.Nonce).
					Uint32("on_chain", onChainNonce).
					Msg("pending nonce too far in future")
				return 0, nil, fmt.Errorf("pending nonce too far in future")
			}
		}
	}

	reservedNonce := meta.Nonce
	checkpoint, err := json.Marshal(meta)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to marshal checkpoint: %w", err)
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
func (c *Client) getCachedNonce(vaultAddr string) nonceMetadata {
	c.signMu.Lock()
	defer c.signMu.Unlock()
	return c.nonces[vaultAddr]
}
