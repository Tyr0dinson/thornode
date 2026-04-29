package substrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/generic"
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
) (*Client, error) {
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}

	logger := log.With().
		Str("module", "substrate").
		Stringer("chain", config.ChainID).
		Logger()

	c := &Client{
		logger:   logger,
		wg:       &sync.WaitGroup{},
		stopchan: make(chan struct{}),
		nonces:   make(map[string]nonceMetadata),
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

	c.rpcClient = rpc.NewClient(
		config.RPCHost,
		config.BlockScanner.HTTPRequestTimeout,
	)

	c.chainScanner, err = NewScanner(
		config.BlockScanner,
		c.ReportSolvency,
		c.rpcClient,
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
	if !c.ShouldReportSolvency(height) {
		return nil
	}

	// When the block scanner is not healthy, only report from the auto-unhalt
	// SolvencyCheckRunner (which passes chainHeight). FetchTxs passes
	// PreviousHeight+1, so skip those to avoid noise during halts.
	if !c.IsBlockScannerHealthy() && height == c.blockScanner.PreviousHeight()+1 {
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

	if c.HasSigned(tx) {
		c.logger.Info().Interface("tx", tx).Msg("transaction already signed, ignoring...")
		return nil, nil, nil, nil
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

	if len(tx.Coins) != 1 {
		return nil, nil, nil, fmt.Errorf("only support one coin per transaction, received: %d", len(tx.Coins))
	}

	// Substrate clients build a Balances::transfer_keep_alive denominated in
	// the chain's native gas asset. Reject any other asset at the boundary
	// rather than silently treating its amount as the gas-asset amount.
	gasAsset := c.GetChain().GetGasAsset()
	if !tx.Coins[0].Asset.Equals(gasAsset) {
		return nil, nil, nil, fmt.Errorf("substrate clients only support the native gas asset %s, got %s", gasAsset, tx.Coins[0].Asset)
	}

	// Get vault address
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get vault address: %w", err)
	}

	// Read block hash + height atomically — used for both nonce management and
	// mortal era construction (era phase must match the block hash).
	latestBlockHash, currentHeight := c.chainScanner.getLatestBlock()

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
			checkpointBytes = nil
		}
		return nil, checkpointBytes, nil, fmt.Errorf("failed to create and sign extrinsic: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic) — matches scanner
	txHash := stypes.ExtrinsicHash(rawTx)

	// Create instant observation TxInItem
	if tx.GasRate < 0 {
		return nil, nil, nil, fmt.Errorf("negative gas rate: %d", tx.GasRate)
	}

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

	// Return checkpoint so the signer can retry with the same nonce if broadcast
	// fails. Substrate extrinsics use a mortal era (configured per chain via
	// MortalEraPeriod) and the signer pipeline stores SignedTx for broadcast retry.
	// Once the era expires, the checkpoint allows re-signing with the same nonce
	// against a fresh mortal era.
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
) ([]byte, error) {
	// Decode destination address
	_, destPubkey, err := ss58.Decode(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode destination address: %w", err)
	}

	amount := c.GetChain().ThorchainToNativeGas(tx.Coins[0].Amount)

	// Build call data
	transferCall := stypes.BalanceTransferKeepAlive(
		c.chainScanner.params.BalancesPallet,
		c.chainScanner.params.BalancesKeepAliveCall,
		destPubkey,
		amount,
	)

	var callData []byte
	if tx.Memo != "" {
		// Batch transfer + remark for memo
		remarkCall := stypes.SystemRemark(
			c.chainScanner.params.SystemPallet,
			c.chainScanner.params.SystemRemarkCall,
			[]byte(tx.Memo),
		)
		callData = stypes.BatchAll(
			c.chainScanner.params.UtilityPallet,
			c.chainScanner.params.UtilityBatchAllCall,
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
	payload := stypes.SigningPayload(
		callData,
		era,
		uint64(nonce),
		0, // tip = 0
		c.chainScanner.params.CheckMetadataHash,
		stypes.MetadataModeDisabled,
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
		return nil, fmt.Errorf("failed to decode vault address: %w", err)
	}

	sig, err = c.Sign(signingData, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Assemble the signed extrinsic
	ext := &stypes.Extrinsic{
		Signer:            vaultPubkeyBytes,
		SignatureType:     stypes.SignatureTypeEd25519,
		Signature:         sig,
		Era:               era,
		Nonce:             uint64(nonce),
		Tip:               0,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: c.chainScanner.params.CheckMetadataHash,
		CallData:          callData,
	}

	return stypes.EncodeSignedExtrinsic(ext), nil
}

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *Client) BroadcastTx(txOutItem ttypes.TxOutItem, rawTx []byte) (string, error) {
	// Compute hash locally for consistency with the scanner's extrinsicHash.
	txHash := stypes.ExtrinsicHash(rawTx)

	hexTx := stypes.HexEncode(rawTx)
	nodeHash, err := c.rpcClient.SubmitExtrinsic(hexTx)
	if err != nil {
		errMsg := err.Error()
		switch {
		// Tx already in the pool — another bifrost instance submitted first.
		// Same pattern as XRP (tefPAST_SEQ) and Cosmos (CosmosSuccessCodes).
		case strings.Contains(errMsg, "AlreadyImported") || strings.Contains(errMsg, "already imported"):
			c.logger.Info().Str("txid", txHash).Msg("extrinsic already in pool, treating as success")

		case strings.Contains(errMsg, "Stale"):
			// Stale means the on-chain nonce has advanced past this tx's slot.
			// That *usually* means our tx already confirmed — but it can also mean
			// someone else burned the slot (operator error, double-signer, state
			// corruption). Do NOT mark signed here; let the scanner confirm via
			// the normal inbound-observation path. On retry, Checkpoint reuses
			// the same nonce under a fresh mortal era.
			c.logger.Info().Str("txid", txHash).Msg("extrinsic stale - deferring confirmation to scanner")
			// triggers retry; SignedTx cleared in sign.go
			return "", fmt.Errorf("stale nonce: %w", err)
		default:
			return "", fmt.Errorf("failed to broadcast tx: %w", err)
		}
	}

	if nodeHash != "" && nodeHash != txHash {
		c.logger.Warn().Str("local", txHash).Str("node", nodeHash).Msg("extrinsic hash mismatch between local and node")
	}
	c.logger.Info().Str("memo", txOutItem.Memo).Str("txid", txHash).Msg("broadcast tx")

	c.SetSigned(txOutItem, txHash)

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

// --------------------------------- helpers ---------------------------------

// reserveNonce atomically reads, reserves, and pre-increments a nonce for the
// given vault. Returns the reserved nonce and its JSON checkpoint.
func (c *Client) reserveNonce(tx ttypes.TxOutItem, vaultAddr string, currentHeight int64) (uint32, []byte, error) {
	// If this is a retry (checkpoint present), use the checkpoint nonce directly
	// without pre-incrementing — the nonce was already incremented on the first attempt.
	if tx.Checkpoint != nil {
		var meta nonceMetadata
		if err := json.Unmarshal(tx.Checkpoint, &meta); err != nil {
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
	} else if fetchedOnChain && meta.Nonce > onChainNonce && currentHeight >= meta.BlockHeight+period {
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
