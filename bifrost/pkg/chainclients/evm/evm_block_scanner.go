package evm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"sync"
	stdatomic "sync/atomic"

	_ "embed"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ethclient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	. "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/aggregators"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

////////////////////////////////////////////////////////////////////////////////////////
// EVMScanner
////////////////////////////////////////////////////////////////////////////////////////

type EVMScanner struct {
	cfg             config.BifrostBlockScannerConfiguration
	logger          zerolog.Logger
	db              blockscanner.ScannerStorage
	m               *metrics.Metrics
	errCounter      *prometheus.CounterVec
	gasPriceChanged bool
	// Guards gasCache while FetchTxs prefetch goroutines append samples and refresh the atomic gasPrice snapshot.
	gasCacheMu sync.Mutex
	// Atomic gasPrice snapshot read by signer/unstuck/network-fee goroutines.
	gasPrice stdatomic.Pointer[big.Int]
	// Serializes FetchTxs/prefetch network-fee compare and lastReportedGasPrice update.
	networkFeeMu          sync.Mutex
	lastReportedGasPrice  stdatomic.Uint64
	ethClient             *ethclient.Client
	ethRpc                *evm.EthRPC
	blockMetaAccessor     evm.BlockMetaAccessor
	globalErrataQueue     chan<- stypes.ErrataBlock
	globalNetworkFeeQueue chan<- common.NetworkFee
	bridge                thorclient.ThorchainBridge
	pubkeyMgr             pubkeymanager.PubKeyValidator
	eipSigner             etypes.Signer
	currentBlockHeight    stdatomic.Int64
	downloadFailures      evm.DownloadFailures
	watchOffset           int
	confirmationPolicy    func(stypes.TxIn) (int64, error)
	scanMu                sync.Mutex
	occurrenceJournalMu   sync.Mutex
	journal               *evm.OccurrenceJournal
	canonicalViewMu       sync.Mutex
	canonicalView         *evm.CanonicalValidator
	gasCache              []*big.Int
	solvencyReporter      SolvencyReporter
	whitelistTokens       []tokenlist.ERC20Token
	whitelistContracts    []common.Address
	signerCacheManager    *signercache.CacheManager
	tokenManager          *evm.TokenManager

	vaultABI *abi.ABI
	erc20ABI *abi.ABI
}

var _ blockscanner.BlockScannerRecoveryService = (*EVMScanner)(nil)

// NewEVMScanner create a new instance of EVMScanner.
func NewEVMScanner(cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorage,
	chainID *big.Int,
	ethClient *ethclient.Client,
	ethRpc *evm.EthRPC,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter SolvencyReporter,
	signerCacheManager *signercache.CacheManager,
) (*EVMScanner, error) {
	// check required arguments
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics manager is nil")
	}
	if ethClient == nil {
		return nil, errors.New("ETH RPC client is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}

	// set storage prefixes
	prefixBlockMeta := fmt.Sprintf("%s-blockmeta-", strings.ToLower(cfg.ChainID.String()))
	prefixSignedMeta := fmt.Sprintf("%s-signedtx-", strings.ToLower(cfg.ChainID.String()))
	prefixTokenMeta := fmt.Sprintf("%s-tokenmeta-", strings.ToLower(cfg.ChainID.String()))

	// create block meta accessor
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor(
		prefixBlockMeta, prefixSignedMeta, storage.GetInternalDb(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create block meta accessor: %w", err)
	}

	// load ABIs
	vaultABI, erc20ABI, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		return nil, fmt.Errorf("failed to create contract abi: %w", err)
	}

	// load token list
	allTokens := tokenlist.GetEVMTokenList(cfg.ChainID).Tokens
	var whitelistTokens []tokenlist.ERC20Token
	for _, addr := range cfg.WhitelistTokens {
		// find matching token in token list
		found := false
		for _, tok := range allTokens {
			if strings.EqualFold(addr, tok.Address) {
				whitelistTokens = append(whitelistTokens, tok)
				found = true
				break
			}
		}

		// all whitelisted tokens must be in the chain token list
		if !found {
			return nil, fmt.Errorf("whitelist token %s not found in token list", addr)
		}
	}

	// create token manager - storage is scoped to chain so assets should not collide
	tokenManager, err := evm.NewTokenManager(
		storage.GetInternalDb(),
		prefixTokenMeta,
		cfg.ChainID.GetGasAsset(),
		defaultDecimals,
		cfg.HTTPRequestTimeout,
		whitelistTokens,
		ethClient,
		routerContractABI,
		erc20ContractABI,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token helper: %w", err)
	}

	// store the token metadata for the chain gas asset
	err = tokenManager.SaveTokenMeta(
		cfg.ChainID.GetGasAsset().Symbol.String(), evm.NativeTokenAddr, defaultDecimals,
	)
	if err != nil {
		return nil, err
	}

	// load whitelist contracts for the chain
	whitelistContracts := []common.Address{}
	for _, agg := range aggregators.DexAggregators() {
		if agg.Chain.Equals(cfg.ChainID) {
			whitelistContracts = append(whitelistContracts, common.Address(agg.Address))
		}
	}

	scanner := &EVMScanner{
		cfg:                cfg,
		logger:             log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:         m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		ethRpc:             ethRpc,
		ethClient:          ethClient,
		db:                 storage,
		m:                  m,
		gasPriceChanged:    false,
		blockMetaAccessor:  blockMetaAccessor,
		bridge:             bridge,
		vaultABI:           vaultABI,
		erc20ABI:           erc20ABI,
		eipSigner:          etypes.NewPragueSigner(chainID),
		pubkeyMgr:          pubkeyMgr,
		gasCache:           make([]*big.Int, 0),
		solvencyReporter:   solvencyReporter,
		whitelistTokens:    whitelistTokens,
		whitelistContracts: whitelistContracts,
		signerCacheManager: signerCacheManager,
		tokenManager:       tokenManager,
	}
	scanner.gasPrice.Store(big.NewInt(0))
	return scanner, nil
}

// --------------------------------- exported ---------------------------------

// GetGasPrice returns the current gas price.
func (e *EVMScanner) GetGasPrice() *big.Int {
	if e.cfg.FixedGasRate > 0 {
		return big.NewInt(e.cfg.FixedGasRate)
	}
	// Return a copy because callers mutate gas prices while this atomic snapshot is shared.
	gasPrice := e.gasPrice.Load()
	if gasPrice == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(gasPrice)
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (e *EVMScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return e.cfg.MaxGasLimit, e.lastReportedGasPrice.Load()
}

// GetNonce returns the nonce (including pending) for the given address.
func (e *EVMScanner) GetNonce(addr string) (uint64, error) {
	return e.ethRpc.GetNonce(addr)
}

// GetNonceFinalized returns the nonce for the given address.
func (e *EVMScanner) GetNonceFinalized(addr string) (uint64, error) {
	return e.ethRpc.GetNonceFinalized(addr)
}

// FetchMemPool returns all transactions in the mempool.
func (e *EVMScanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// GetTokens returns all token meta data.
func (e *EVMScanner) GetTokens() ([]*evmtypes.TokenMeta, error) {
	return e.tokenManager.GetTokens()
}

// downloadScannerBlock fetches a block and its receipts outside scanMu and
// opens its canonical proof.
func (e *EVMScanner) downloadScannerBlock(height int64) (*etypes.Block, []*etypes.Receipt, evm.ScannerEvidence, error) {
	block, evidence, err := e.canonicalObservationView().FetchScannerBlock(height, e.ethRpc.GetBlock)
	if err != nil {
		return nil, nil, evidence, err
	}
	receipts, err := e.fetchScannerReceipts(block)
	if err != nil {
		return nil, nil, evidence, fmt.Errorf("fetch block receipts: %w", err)
	}
	evidence, err = e.canonicalObservationView().PrepareScannerBlock(evidence)
	if err != nil {
		return nil, nil, evidence, fmt.Errorf("prepare block proof: %w", err)
	}
	return block, receipts, evidence, nil
}

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (e *EVMScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	// log height every 100 blocks
	if height%100 == 0 {
		e.logger.Info().Int64("height", height).Msg("fetching txs for height")
	}

	// process all transactions in the block
	// A durable earlier replay must not wait for this block's RPCs. The
	// journal serializes this read; keep the later cleanup/generation/replay
	// checks too, since recovery work can arrive while downloads are in flight.
	journal, journalErr := e.occurrenceJournal()
	if journalErr != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, journalErr)
	}
	// A reorg or recovery request that overtakes the download (a generation
	// change) is not an RPC failure: download the height once more at once
	// instead of paying the scanner's unavailable-block backoff. A replaced
	// download (an orphan) is not retried: the RPC may still serve it, and each
	// detection invalidates the generation again.
	var block *etypes.Block
	var receipts []*etypes.Receipt
	var evidence evm.ScannerEvidence
	for attempt := 0; ; attempt++ {
		if next, ok, nextErr := journal.NextRescanWithin(height, e.cfg.FloorBlockHeight); nextErr != nil {
			return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, nextErr)
		} else if ok && next < height {
			return stypes.TxIn{}, btypes.RewindRequiredError{NextHeight: next}
		}
		var err error
		block, receipts, evidence, err = e.downloadScannerBlock(height)
		if err == nil {
			e.scanMu.Lock()
			if e.canonicalObservationView().Current(evidence.Generation) {
				break
			}
			e.scanMu.Unlock()
			err = evm.ErrStaleScannerGeneration
		}
		if attempt > 0 || !errors.Is(err, evm.ErrStaleScannerGeneration) {
			// A second stale generation is a reorg race, not an RPC failure.
			if !errors.Is(err, evm.ErrStaleScannerGeneration) && e.downloadFailures.First(height) {
				e.logger.Warn().Err(err).Int64("height", height).Msg("failed to download block and receipts")
			}
			return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, err)
		}
	}
	defer e.scanMu.Unlock()
	if cleanupErr := evm.DrainOccurrenceCleanup(journal, e.canonicalObservationView(), e.globalErrataQueue, e.cfg.HTTPRequestTimeout); cleanupErr != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, cleanupErr)
	}

	if next, ok, nextErr := journal.NextRescanWithin(height, e.cfg.FloorBlockHeight); nextErr != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, nextErr)
	} else if ok && next < height {
		return stypes.TxIn{}, btypes.RewindRequiredError{NextHeight: next}
	}

	txIn, err := e.processBlock(block, receipts)
	if err != nil {
		e.logger.Error().Err(err).Int64("height", height).Msg("failed to search tx in block")
		return stypes.TxIn{}, fmt.Errorf("%w: fail to process block %d: %w", btypes.ErrUnavailableBlock, height, err)
	}

	var validationErr error
	if e.canonicalObservationView().Current(evidence.Generation) {
		evidence, validationErr = e.canonicalObservationView().RevalidateScannerBlock(evidence)
	} else {
		// Reorg processing under scanMu invalidated the opening view. Prove the
		// replacement in full before publishing its metadata or delivery.
		evidence, validationErr = e.canonicalObservationView().ValidateScannerBlock(height, block.Hash().Hex())
	}
	if validationErr != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %v", btypes.ErrUnavailableBlock, validationErr)
	}

	// Keep the acknowledgement even if local relevance filtering removes
	// every item: Observer's first-status gate still owns startup progress.
	hadExtractedItems := len(txIn.TxArray) > 0
	// if reorgs are possible on this chain store block meta for handling
	if e.cfg.MaxReorgRescanBlocks > 0 {
		if txIn, err = journal.FilterUnattributed(txIn); err != nil {
			return stypes.TxIn{}, fmt.Errorf("%w: filter observation attribution: %w", btypes.ErrUnavailableBlock, err)
		}
		blockMeta := evmtypes.NewBlockMeta(block.Header(), txIn)
		if err = evm.RetainTransactionIdentity(blockMeta, block, e.eipSigner); err != nil {
			return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, err)
		}

		if e.pubkeyMgr != nil {
			evm.RetainInboundDirection(blockMeta, txIn, func(address string) bool {
				ok, _ := e.pubkeyMgr.IsValidPoolAddress(address, e.cfg.ChainID)
				return ok
			})
		}
		if err = journal.SaveMeta(blockMeta); err != nil {
			return stypes.TxIn{}, fmt.Errorf("%w: persist block metadata: %w", btypes.ErrUnavailableBlock, err)
		}
		pruneHeight := height - e.cfg.MaxReorgRescanBlocks
		if pruneHeight > 0 {
			defer func() {
				if err = journal.PruneBlockMeta(pruneHeight); err != nil {
					e.logger.Err(err).Int64("height", height).Msg("fail to prune block meta")
				}
			}()
		}
	}

	extractedCount := len(txIn.TxArray)
	txIn, err = journal.HoldFailedReplays(txIn)
	if err != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: retain failed replay: %w", btypes.ErrUnavailableBlock, err)
	}
	if txIn, err = journal.PrepareRecoveryHandoff(txIn); err != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %v", btypes.ErrUnavailableBlock, err)
	}
	// The finalizer retains withheld attribution waits while Observer only sees
	// deliverable items. Mixed batches therefore advance without false handoffs.
	txIn.TrackObservationHolds()
	finalizationTxIn := txIn
	txIn.TxArray = make([]*stypes.TxInItem, 0, len(finalizationTxIn.TxArray))
	for _, item := range finalizationTxIn.TxArray {
		if !item.EVMAttributionPending {
			txIn.TxArray = append(txIn.TxArray, item)
		}
	}

	if waiting := len(finalizationTxIn.TxArray) - len(txIn.TxArray); waiting > 0 {
		e.logger.Debug().Int64("height", height).Int("waiting_observations", waiting).Msg("recovery delivery waits for authenticated vault attribution")
	}
	if heldCount := extractedCount - len(finalizationTxIn.TxArray); heldCount > 0 {
		e.logger.Warn().Int64("height", height).Int("held_observations", heldCount).Msg("retained conflicting EVM recovery observation; accounting review required")
	}

	e.updateCurrentBlockHeight(height)

	rescanHeight, rescanPending, rescanErr := journal.NextRescanWithin(height, e.cfg.FloorBlockHeight)
	if rescanErr != nil {
		return stypes.TxIn{}, fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, rescanErr)
	}
	if hadExtractedItems || (rescanPending && rescanHeight == height) {
		txIn.Persisted = make(chan error, 1)
	}
	validateFinalizer := func() error {
		_, finalizeErr := e.canonicalObservationView().RevalidateScannerBlock(evidence)
		return finalizeErr
	}

	// Stored and skipped completion remain distinct transitions, but both must
	// reject a stale generation or a changed canonical block on every page.
	txIn.FinalizePersistence = func() error {
		if finalizeErr := validateFinalizer(); finalizeErr != nil {
			return finalizeErr
		}
		return journal.CompleteRescan(height, block.Hash().Hex(), finalizationTxIn, e.canonicalObservationView(), evidence.Generation)
	}
	txIn.FinalizeSkipped = func() error {
		if finalizeErr := validateFinalizer(); finalizeErr != nil {
			return finalizeErr
		}
		return journal.SkipRescan(height, block.Hash().Hex(), finalizationTxIn, e.canonicalObservationView(), evidence.Generation)
	}

	// skip reporting network fee and solvency if block more than flexibility blocks from tip
	if chainHeight-height > e.cfg.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	// report network fee and solvency
	e.reportNetworkFee(height)
	if e.solvencyReporter != nil {
		if err = e.solvencyReporter(height); err != nil {
			e.logger.Err(err).Msg("failed to report Solvency info to THORNode")
		}
	}

	return txIn, nil
}

func (e *EVMScanner) getCurrentBlockHeight() int64 {
	return e.currentBlockHeight.Load()
}

// updateCurrentBlockHeight records the highest scanner height seen without
// allowing concurrent prefetches to move it backwards.
func (e *EVMScanner) updateCurrentBlockHeight(height int64) {
	for {
		currentHeight := e.currentBlockHeight.Load()
		if height <= currentHeight {
			return
		}
		if e.currentBlockHeight.CompareAndSwap(currentHeight, height) {
			return
		}
	}
}

// --------------------------------- extraction ---------------------------------

func (e *EVMScanner) processBlock(block *etypes.Block, prefetched ...[]*etypes.Receipt) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		TxArray:  nil,
		Filtered: false,
		MemPool:  false,
	}

	// Validate ancestry before skipping empty blocks so transaction count
	// cannot suppress reorg cleanup or replacement-block recovery.
	if e.cfg.MaxReorgRescanBlocks > 0 {
		err := e.processReorg(block.Header())
		if err != nil {
			e.logger.Error().Err(err).Msgf("fail to process reorg for block %d", block.NumberU64())
			return txIn, err
		}
	}

	// skip empty blocks
	if block.Transactions().Len() == 0 {
		return txIn, nil
	}

	// collect all relevant transactions from the block and their gas prices
	txInBlock, gasPrices, err := e.getTxIn(block, prefetched...)
	if err != nil {
		return txIn, err
	}
	if len(txInBlock.TxArray) > 0 {
		txIn.TxArray = append(txIn.TxArray, txInBlock.TxArray...)
	}

	// update gas price from collected receipt effective gas prices
	e.updateGasPrice(gasPrices)

	return txIn, nil
}

func (e *EVMScanner) getTxInOptimized(method string, block *etypes.Block, prefetched ...[]*etypes.Receipt) (stypes.TxIn, []*big.Int, error) {
	// Use custom method name as it varies between implementation
	// It should be akin to: "fetch all transaction receipts within a block", e.g. getBlockReceipts
	// This has shown to be more efficient than getTransactionReceipt in a batch call
	txInbound := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		Filtered: false,
		MemPool:  false,
	}

	// tx lookup for compatibility with shared evm functions
	txByHash := make(map[string]*etypes.Transaction)
	for _, tx := range block.Transactions() {
		if tx == nil {
			continue
		}

		// skip blob transactions
		if tx.Type() == etypes.BlobTxType {
			continue
		}

		// best effort remove the tx from the signed txs (ok if it does not exist)
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
		}

		txByHash[tx.Hash().String()] = tx
	}

	var receipts []*etypes.Receipt
	var err error
	if len(prefetched) > 0 {
		receipts = prefetched[0]
	} else {
		receipts, err = e.fetchOptimizedReceipts(method, block)
		if err != nil {
			return stypes.TxIn{}, nil, err
		}
	}

	// at least one worker: an unbuffered semaphore would block the first
	// receipt forever, and a negative capacity panics
	sem := make(chan struct{}, max(e.cfg.Concurrency, 1))
	wg := sync.WaitGroup{}
	mu := sync.Mutex{}
	var conversionFailure error

	var gasPrices []*big.Int

	for _, receipt := range receipts {
		sem <- struct{}{}
		wg.Add(1)
		go func(receipt *etypes.Receipt) {
			defer wg.Done()
			defer func() { <-sem }()
			if receipt == nil {
				e.logger.Warn().Uint64("blockNumber", block.NumberU64()).Msg("receipt tx not in block.Transactions or nil, ignoring...")
				return
			}
			txForReceipt, ok := txByHash[receipt.TxHash.String()]
			if !ok {
				e.logger.Warn().
					Str("txHash", receipt.TxHash.String()).
					Uint64("blockNumber", block.NumberU64()).
					Msg("receipt tx not in block.Transactions or nil, ignoring...")
				return
			}

			// collect effective gas price from receipt for gas price tracking
			mu.Lock()
			gasPrices = append(gasPrices, receipt.EffectiveGasPrice)
			mu.Unlock()

			// tx without to address is not valid
			if txForReceipt.To() == nil {
				return
			}

			// extract the txInItem; the error must be goroutine-local, a shared
			// one lets this goroutine drop its item on another goroutine's error
			txInItem, conversionErr := e.receiptToTxInItem(txForReceipt, receipt)
			if conversionErr != nil {
				// A transport/RPC failure means the block cannot be proven fully
				// scanned, so record it and fail the pass for a rescan. Option 3:
				// gate only on IsRPCError; the recovery-attribution arm is dropped
				// because son-of-odin removed the redundant recovery step, so that
				// error no longer arises on this path.
				if evm.IsRPCError(conversionErr) {
					mu.Lock()
					if conversionFailure == nil {
						conversionFailure = conversionErr
					}
					mu.Unlock()
				}
				e.logger.Error().Err(conversionErr).Msg("failed to convert receipt to txInItem")
				return
			}

			// skip invalid items
			if txInItem == nil {
				return
			}
			if len(txInItem.To) == 0 {
				return
			}
			if len([]byte(txInItem.Memo)) > constants.MaxMemoSize {
				return
			}

			// add the txInItem to the txInbound
			txInItem.BlockHeight = block.Number().Int64()
			txInItem.BlockHash = block.Hash().Hex()
			mu.Lock()
			txInbound.TxArray = append(txInbound.TxArray, txInItem)
			mu.Unlock()
		}(receipt)
	}
	wg.Wait()

	if conversionFailure != nil {
		return stypes.TxIn{}, nil, conversionFailure
	}

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, gasPrices, nil
	}
	return txInbound, gasPrices, nil
}

// usesBlockReceipts reports a chain whose receipts are fetched for a whole block
// at once with eth_getBlockReceipts, both on the scan download path
// (fetchScannerReceipts) and in extraction (getTxIn).
// CHANGEME: if an EVM chain supports some way of fetching all transaction receipts
// within a block, register it here.
func usesBlockReceipts(chain common.Chain) bool {
	switch chain {
	case common.BASEChain, common.BSCChain, common.POLChain:
		return true
	}
	return false
}

// usesReceiptsGate reports a block-receipts chain whose receipts
// gasPricesWithoutReceipts may rule out, see there for what its bloom must cover.
func usesReceiptsGate(chain common.Chain) bool {
	switch chain {
	case common.BASEChain, common.BSCChain:
		return true
	}
	return false
}

// canceledContext is for ethclient calls that must not reach the server.
var canceledContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// gasPricesWithoutReceipts returns the gas prices of a block whose receipts cannot
// produce an observation, so the caller can skip fetching them. ok is false if the
// receipts might produce one, or if the block alone cannot rule that out.
//
// Only logs emitted by a router are parsed, and logsBloom has no false negatives,
// so a block whose bloom lacks every router has no router log. That has to come from
// the bloom: with the contract whitelist off, a deposit can arrive through any
// contract, so tx.To cannot find it. It also covers a tx to a router or whitelisted
// contract, which has nothing to parse without a router log. Everything else
// receiptToTxInItem observes is visible in the tx: sends to and from a vault (including
// failed ones, which have no logs) and direct ERC20 transfers to a vault.
//
// Senders are taken as the RPC reported them where possible, see txSender. A provider
// that lied about a sender could hide that tx from this check, but it could equally
// leave the tx out of the block or misreport its receipt.
//
// A chain needs a bloom that covers the block's own logs. AVAX's does not: under ACP-194
// it covers the blocks being settled, so a router log there can be absent from its
// block's bloom, and AVAX must not use this.
func (e *EVMScanner) gasPricesWithoutReceipts(block *etypes.Block) (gasPrices []*big.Int, ok bool) {
	baseFee := block.BaseFee()
	if baseFee == nil {
		return nil, false
	}
	routers := e.pubkeyMgr.GetContracts(e.cfg.ChainID, false)
	if len(routers) == 0 {
		return nil, false
	}
	bloom := block.Bloom()
	// no logs at all, or nothing filled the bloom in: only the receipts can tell
	if bloom == (etypes.Bloom{}) && block.Transactions().Len() > 0 {
		return nil, false
	}
	for _, router := range routers {
		if etypes.BloomLookup(bloom, ecommon.HexToAddress(router.String())) {
			return nil, false
		}
	}

	// A transaction the occurrence journal owns is a recovery replay: only the
	// receipt path restores the vaults and routers it was observed with, or keeps
	// the replay open while they are unknown. The registry alone could miss it.
	// The condition is the receipt path's, see receiptToTxInItem.
	if e.blockMetaAccessor != nil && e.cfg.MaxReorgRescanBlocks > 0 {
		journal, err := e.occurrenceJournal()
		if err != nil {
			return nil, false
		}
		if owned, ownErr := journal.OwnsAnyBlockTransaction(block); ownErr != nil || owned {
			return nil, false
		}
	}

	txs := make([]*etypes.Transaction, 0, block.Transactions().Len())
	for i, tx := range block.Transactions() {
		// same txs as getTxInOptimized: blob txs have no place in the gas price
		if tx == nil || tx.Type() == etypes.BlobTxType {
			continue
		}
		if tx.IsDepositTx() {
			// reported as 0 in the receipt, where base fee + tip would give the base fee
			gasPrices = append(gasPrices, new(big.Int))
		} else {
			tip, err := tx.EffectiveGasTip(baseFee)
			if err != nil {
				return nil, false
			}
			gasPrices = append(gasPrices, new(big.Int).Add(tip, baseFee))
		}
		if e.txMayBeObserved(tx, block.Hash(), uint(i)) {
			return nil, false
		}
		txs = append(txs, tx)
	}

	// son-of-odin: a block that may hold one of our signed (outbound) txs
	// cannot be ruled out by the bloom gate, since a failed outbound has no
	// logs and its signing vault may have left the vault list.
	if evm.BlockMayHaveSignedTx(block, e.signerCacheManager) {
		return nil, false
	}

	for _, tx := range txs {
		// best effort remove the tx from the signed txs (ok if it does not exist)
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
		}
	}
	return gasPrices, true
}

// txMayBeObserved reports whether tx's own fields could make receiptToTxInItem
// observe it, leaving aside logs emitted by a router.
func (e *EVMScanner) txMayBeObserved(tx *etypes.Transaction, blockHash ecommon.Hash, index uint) bool {
	to := tx.To()
	if to == nil {
		return false
	}
	if e.isPoolAddress(*to) {
		return true
	}
	if evm.HasERC20TransferSelector(tx) {
		// transfer(address,uint256): the recipient is the first argument
		const recipientEnd = 4 + ecommon.HashLength
		data := tx.Data()
		if len(data) < recipientEnd || e.isPoolAddress(ecommon.BytesToAddress(data[4:recipientEnd])) {
			return true
		}
	}
	sender, err := e.txSender(tx, blockHash, index)
	return err != nil || e.isPoolAddress(sender)
}

// txSender returns the sender the RPC reported when the block was decoded, which
// ethclient keeps with the tx, and otherwise recovers it from the signature. Recovery
// costs about 70us, or most of a block's time in gasPricesWithoutReceipts.
func (e *EVMScanner) txSender(tx *etypes.Transaction, blockHash ecommon.Hash, index uint) (ecommon.Address, error) {
	// On a miss TransactionSender asks the server. The cancelled context turns that
	// into an error, so a block without reported senders cannot cost one call per tx.
	if sender, err := e.ethClient.TransactionSender(canceledContext, tx, blockHash, index); err == nil {
		return sender, nil
	}
	return e.eipSigner.Sender(tx)
}

func (e *EVMScanner) isPoolAddress(addr ecommon.Address) bool {
	ok, _ := e.pubkeyMgr.IsValidPoolAddress(evm.LowerHexAddress(addr), e.cfg.ChainID)
	return ok
}

func (e *EVMScanner) getTxIn(block *etypes.Block, prefetched ...[]*etypes.Receipt) (stypes.TxIn, []*big.Int, error) {
	if usesBlockReceipts(e.cfg.ChainID) {
		// Receipts prefetched outside scanMu are already paid for; without them the
		// logsBloom gate may still spare the fetch.
		if len(prefetched) == 0 && usesReceiptsGate(e.cfg.ChainID) {
			if gasPrices, ok := e.gasPricesWithoutReceipts(block); ok {
				return stypes.TxIn{}, gasPrices, nil
			}
		}
		return e.getTxInOptimized("eth_getBlockReceipts", block, prefetched...)
	}

	txInbound := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		Filtered: false,
		MemPool:  false,
	}
	var gasPrices []*big.Int

	var receipts []*etypes.Receipt
	var err error
	if len(prefetched) > 0 {
		receipts = prefetched[0]
	} else {
		receipts, err = e.fetchBatchedReceipts(block)
		if err != nil {
			return stypes.TxIn{}, nil, err
		}
	}
	txByHash := make(map[ecommon.Hash]*etypes.Transaction, len(block.Transactions()))
	for _, tx := range block.Transactions() {
		if tx != nil {
			txByHash[tx.Hash()] = tx
		}
	}
	for _, receipt := range receipts {
		// The batched fetch matches every receipt to a block transaction. A
		// receipt from any other source that matches none is ignored, as on the
		// optimized path, rather than panic.
		var tx *etypes.Transaction
		if receipt != nil {
			tx = txByHash[receipt.TxHash]
		}
		if tx == nil {
			e.logger.Warn().Uint64("blockNumber", block.NumberU64()).Msg("receipt tx not in block.Transactions or nil, ignoring...")
			continue
		}
		// Signed-tx bookkeeping and conversion belong to the serialized scan.
		// Removal waits for the block's receipts: a failed fetch, or a
		// missing receipt (which fails the block), leaves the items until a
		// retry fetches them.
		if removeErr := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); removeErr != nil {
			e.logger.Err(removeErr).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
		}

		// collect effective gas price from receipt for gas price tracking
		gasPrices = append(gasPrices, receipt.EffectiveGasPrice)

		// extract the txInItem
		txInItem, err := e.receiptToTxInItem(tx, receipt)
		if err != nil {
			e.logger.Error().Err(err).Msg("failed to convert receipt to txInItem")
			if evm.IsRPCError(err) || errors.Is(err, evm.ErrRecoveryAttribution) {
				return stypes.TxIn{}, nil, err
			}
			continue
		}

		// skip invalid items
		if txInItem == nil {
			continue
		}
		if len(txInItem.To) == 0 {
			continue
		}
		if len([]byte(txInItem.Memo)) > constants.MaxMemoSize {
			continue
		}

		// add the txInItem to the txInbound
		txInItem.BlockHeight = block.Number().Int64()
		txInItem.BlockHash = block.Hash().Hex()
		txInbound.TxArray = append(txInbound.TxArray, txInItem)
	}

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, gasPrices, nil
	}
	return txInbound, gasPrices, nil
}

// fetchScannerReceipts performs only RPC retrieval and block/receipt association.
// It may overlap other downloads; conversion and journal writes stay under scanMu.
func (e *EVMScanner) fetchScannerReceipts(block *etypes.Block) ([]*etypes.Receipt, error) {
	if block.Transactions().Len() == 0 {
		return nil, nil
	}
	if usesBlockReceipts(e.cfg.ChainID) {
		return e.fetchOptimizedReceipts("eth_getBlockReceipts", block)
	}
	return e.fetchBatchedReceipts(block)
}

// receiptRPCContext bounds a receipt RPC with the configured request timeout so
// a stalled node cannot wedge the scanner on context.Background() forever. A
// non-positive timeout (an unset config, or a unit-test fixture) means no
// deadline, matching the pre-deadline behaviour.
func (e *EVMScanner) receiptRPCContext() (context.Context, context.CancelFunc) {
	if e.cfg.HTTPRequestTimeout > 0 {
		return context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
	}
	return context.Background(), func() {}
}

func (e *EVMScanner) fetchOptimizedReceipts(method string, block *etypes.Block) ([]*etypes.Receipt, error) {
	var receipts []*etypes.Receipt
	ctx, cancel := e.receiptRPCContext()
	defer cancel()
	if err := e.ethClient.Client().CallContext(ctx, &receipts, method, hexutil.EncodeBig(block.Number())); err != nil {
		return nil, err
	}
	return validateBlockReceipts(e.cfg.ChainID, block, receipts)
}

func (e *EVMScanner) fetchBatchedReceipts(block *etypes.Block) ([]*etypes.Receipt, error) {
	var receipts []*etypes.Receipt
	// collect all relevant transactions from the block into batches
	batches := [][]*etypes.Transaction{}
	batch := []*etypes.Transaction{}
	for _, tx := range block.Transactions() {
		if tx == nil || tx.To() == nil {
			continue
		}

		// skip blob transactions
		if tx.Type() == etypes.BlobTxType {
			continue
		}

		batch = append(batch, tx)
		if len(batch) >= e.cfg.TransactionBatchSize {
			batches = append(batches, batch)
			batch = []*etypes.Transaction{}
		}
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}

	// A receipt's blockHash is the chain's own. Go recomputes AVAX's header
	// hash without its extra fields, so a differing hash is resolved once.
	blockHash, reportedHash := block.Hash(), block.Hash()

	// process all batches
	for _, batch := range batches {
		// create the batch rpc request
		var rpcBatch []rpc.BatchElem
		for _, tx := range batch {
			rpcBatch = append(rpcBatch, rpc.BatchElem{
				Method: "eth_getTransactionReceipt",
				Args:   []interface{}{tx.Hash().String()},
				Result: &etypes.Receipt{},
			})
		}

		// send the batch rpc request under a deadline, so a stalled RPC cannot
		// wedge the scanner on context.Background() forever.
		ctx, cancel := e.receiptRPCContext()
		err := e.ethClient.Client().BatchCallContext(ctx, rpcBatch)
		cancel()
		if err != nil {
			e.logger.Error().Int("size", len(batch)).Err(err).Msg("failed to batch fetch transaction receipts")
			return nil, err
		}

		// process the batch rpc response
		for i, elem := range rpcBatch {
			// A missing or misplaced receipt fails the block (the elem.Error flows
			// into the checks below), as in the eth_getBlockReceipts path; dropping
			// it with an early continue would complete an owned journal replay empty.
			receipt, ok := elem.Result.(*etypes.Receipt)
			if elem.Error == nil && (!ok || receipt == nil || receipt.TxHash != batch[i].Hash() || receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(block.Number()) != 0) {
				elem.Error = errors.New("receipt does not belong to extracted block height")
			}
			if elem.Error == nil && receipt.BlockHash != reportedHash {
				same, sameErr := evm.SameBlock(e.ethRpc, receipt.BlockHash, blockHash.Hex(), block.Number().Int64())
				switch {
				case sameErr != nil:
					elem.Error = sameErr
				case !same:
					elem.Error = errors.New("receipt does not belong to extracted block")
				default:
					reportedHash = receipt.BlockHash
				}
			}
			if elem.Error != nil {
				e.logger.Error().Err(elem.Error).Str("hash", batch[i].Hash().Hex()).Msg("failed to fetch transaction receipt")
				return nil, elem.Error
			}

			receipts = append(receipts, receipt)
		}
	}
	return receipts, nil
}

// TODO: This is only used by unit tests now, but covers receiptToTxInItem internally -
// refactor so this logic is only in test code.
func (e *EVMScanner) getTxInItem(tx *etypes.Transaction) (*stypes.TxInItem, error) {
	if tx == nil || tx.To() == nil {
		return nil, nil
	}

	receipt, err := e.ethRpc.GetReceipt(tx.Hash().Hex())
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get transaction receipt: %w", err)
	}

	return e.receiptToTxInItem(tx, receipt)
}

func (e *EVMScanner) receiptToTxInItem(tx *etypes.Transaction, receipt *etypes.Receipt) (result *stypes.TxInItem, resultErr error) {
	var journal *evm.OccurrenceJournal
	if e.blockMetaAccessor != nil && e.cfg.MaxReorgRescanBlocks > 0 {
		var err error
		journal, err = e.occurrenceJournal()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", evm.ErrRecoveryAttribution, err)
		}
	}
	provider := evm.NewRecoveryPubKeyValidator(e.pubkeyMgr, journal, tx.Hash().Hex(), e.cfg.ChainID)
	// The adapter never converts a storage error into an acknowledged empty block.
	defer func() {
		if result != nil {
			// Router parsing may stop after recognizing the source vault. Capture
			// both actual endpoints so a later replay can still emit both legs.
			provider.IsValidPoolAddress(result.Sender, e.cfg.ChainID)
			provider.IsValidPoolAddress(result.To, e.cfg.ChainID)
		} else if resultErr == nil {
			result = provider.UnattributedItem(tx, e.eipSigner)
		}
		if provider.Err != nil {
			result = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: %w", evm.ErrRecoveryAttribution, provider.Err))
		} else if result != nil {
			result.VaultAttribution = provider.Attribution()
		}
	}()

	if receipt.Status != 1 {
		e.logger.Debug().Stringer("txid", tx.Hash()).Uint64("status", receipt.Status).Msg("tx failed")

		// remove failed transactions from signer cache so they are retried
		if e.signerCacheManager != nil {
			e.signerCacheManager.RemoveSigned(tx.Hash().String())
		}

		return e.getTxInFromFailedTransaction(tx, receipt, provider), nil
	}

	disableWhitelist, err := e.bridge.GetMimir(constants.EVMDisableContractWhitelist.String())
	if err != nil {
		e.logger.Err(err).Msgf("fail to get %s", constants.EVMDisableContractWhitelist.String())
		disableWhitelist = 0
	}

	if disableWhitelist == 1 {
		// parse tx without whitelist
		destination := tx.To()
		isToVault, _ := provider.IsValidPoolAddress(destination.String(), e.cfg.ChainID)

		// resolve sender upfront to check vault outbounds (approvals, cancels, etc.)
		sender, senderErr := etypes.Sender(e.eipSigner, tx)
		var isFromVault bool
		if senderErr == nil {
			isFromVault, _ = provider.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
		}

		switch {
		case isToVault:
			// Tx to a vault
			return e.getTxInFromTransaction(tx, receipt, provider)
		case e.isToValidContractAddress(destination, true, provider):
			// Deposit directly to router
			return e.getTxInFromSmartContract(tx, receipt, 0, provider)
		case isFromVault:
			// Tx from a vault (outbound, approval, cancel, etc.)
			return e.getTxInFromTransaction(tx, receipt, provider)
		case evm.HasERC20TransferSelector(tx):
			// Reject unknown tokens before the cached Mimir gate.
			if !e.isRelevantERC20Token(destination) {
				return nil, nil
			}
			// Confirm this is a successful top-level ERC20 transfer from tx.To().
			if !evm.IsERC20Transfer(tx, receipt) {
				return nil, nil
			}
			// Keep direct inbounds behind the per-chain operator flag.
			if !e.directERC20InboundEnabled() {
				return nil, nil
			}
			if senderErr != nil {
				// Match the whitelist-enabled path: ignore txs whose sender we
				// cannot determine rather than surfacing scanner noise.
				return nil, nil
			}
			// Reuse the sender recovered above to avoid a second ecrecover.
			return e.getTxInFromDirectERC20Transfer(tx, receipt, &sender, provider)
		case evm.IsSmartContractCall(tx, receipt):
			// Tx to a different contract, attempt to parse with max allowable logs
			return e.getTxInFromSmartContract(tx, receipt, int64(e.cfg.MaxContractTxLogs), provider)
		default:
			// ignore tx not involving any vault
			return nil, nil
		}
	} else {
		// parse tx with whitelist
		if e.isToValidContractAddress(tx.To(), true, provider) {
			return e.getTxInFromSmartContract(tx, receipt, 0, provider)
		}
		// check if tx is to or from a vault
		sender, err := etypes.Sender(e.eipSigner, tx)
		if err != nil {
			return nil, nil // ignore tx if we cannot determine sender
		}
		isFromVault, _ := provider.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
		isToVault, _ := provider.IsValidPoolAddress(tx.To().String(), e.cfg.ChainID)
		if isFromVault || isToVault {
			return e.getTxInFromTransaction(tx, receipt, provider)
		}
		if evm.HasERC20TransferSelector(tx) {
			// Reject unknown tokens before the cached Mimir gate.
			if !e.isRelevantERC20Token(tx.To()) {
				return nil, nil
			}
			// Confirm this is a successful top-level ERC20 transfer from tx.To().
			if !evm.IsERC20Transfer(tx, receipt) {
				return nil, nil
			}
			// Keep direct inbounds behind the per-chain operator flag.
			if !e.directERC20InboundEnabled() {
				return nil, nil
			}
			// Reuse the sender recovered above to avoid a second ecrecover.
			return e.getTxInFromDirectERC20Transfer(tx, receipt, &sender, provider)
		}
		return nil, nil
	}
}

// --------------------------------- reorg ---------------------------------

// processReorg compares the block's parent hash with the stored block hash. When a
// reorg is detected, it triggers a rescan of all cached blocks in the reorg window.
// Recovery is delivered by the durable journal and ordered rewind.
func (e *EVMScanner) processReorg(header *etypes.Header) error {
	currentMeta, readErr := e.blockMetaAccessor.GetBlockMeta(header.Number.Int64())
	if readErr != nil {
		return readErr
	}
	replacedHere := currentMeta != nil && !strings.EqualFold(currentMeta.BlockHash, header.Hash().Hex())

	previousHeight := header.Number.Int64() - 1
	prevBlockMeta, err := e.blockMetaAccessor.GetBlockMeta(previousHeight)
	if err != nil {
		return fmt.Errorf("fail to get block meta of height(%d) : %w", previousHeight, err)
	}

	// skip re-org processing if we did not store the previous block meta
	if prevBlockMeta == nil && !replacedHere {
		return nil
	}

	// The parent hash is the chain's own; stored hashes are recomputed locally.
	linked := false
	if prevBlockMeta != nil {
		if linked, err = evm.SameBlock(e.ethRpc, header.ParentHash, prevBlockMeta.BlockHash, previousHeight); err != nil {
			return fmt.Errorf("%w: %w", btypes.ErrUnavailableBlock, err)
		}
	}

	// no re-org if stored block hash at previous height is equal to current block parent
	if !replacedHere && linked {
		return nil
	}
	// The previous replayed block links to this replacement, and the durable
	// sweep already cleaned every occurrence here. Re-extract this block directly
	// instead of sweeping the entire retained window again at each rewind height.
	if replacedHere && linked {
		journal, journalErr := e.occurrenceJournal()
		if journalErr != nil {
			return journalErr
		}
		prepared, prepareErr := journal.ReplayPrepared(currentMeta)
		if prepareErr != nil {
			return prepareErr
		}
		if prepared {
			return nil
		}
	}

	e.canonicalObservationView().Invalidate()
	e.logger.Info().Int64("height", header.Number.Int64()).Msg("reorg detected")

	// send erratas and determine the block heights to rescan
	_, err = e.reprocessTxs()
	if err != nil {
		return fmt.Errorf("%w: %v", btypes.ErrUnavailableBlock, err)
	}

	// Classification may encounter an older held request before the current
	// fork. Apply the same rewind bounds as entry-point scheduling and select
	// the earliest eligible request, rather than clamping or dropping evidence.
	journal, journalErr := e.occurrenceJournal()
	if journalErr != nil {
		return journalErr
	}
	next, pending, nextErr := journal.NextRescanWithin(header.Number.Int64(), e.cfg.FloorBlockHeight)
	if nextErr != nil {
		return nextErr
	}
	if pending && next < header.Number.Int64() {
		return btypes.RewindRequiredError{NextHeight: next}
	}
	return nil
}

// reprocessTxs classifies canonical locations without overwriting an original
// anchor. Unresolved local cleanup or transaction evidence remains retryable.
func (e *EVMScanner) reprocessTxs() ([]int64, error) {
	journal, err := e.occurrenceJournal()
	if err != nil {
		return nil, err
	}
	return evm.ReconcileBlockOccurrences(journal, e.ethRpc, e.canonicalObservationView(), e.globalErrataQueue, e.cfg.HTTPRequestTimeout)
}

func (e *EVMScanner) updateGasPrice(prices []*big.Int) {
	// skip empty blocks
	if len(prices) == 0 {
		return
	}

	// filter out nil gas prices (defensive check for EIP-1559 and other dynamic fee transactions)
	validPrices := make([]*big.Int, 0, len(prices))
	for _, price := range prices {
		if price != nil {
			validPrices = append(validPrices, price)
		}
	}
	if len(validPrices) == 0 {
		return
	}

	// find the median gas price in the block
	sort.Slice(validPrices, func(i, j int) bool { return validPrices[i].Cmp(validPrices[j]) == -1 })
	gasPrice := new(big.Int).Set(validPrices[len(validPrices)/2])

	// FetchTxs can run in prefetch goroutines, so serialize gasCache updates before publishing gasPrice.
	e.gasCacheMu.Lock()
	defer e.gasCacheMu.Unlock()

	// add to the cache
	e.gasCache = append(e.gasCache, gasPrice)
	if len(e.gasCache) > e.cfg.GasCacheBlocks {
		e.gasCache = e.gasCache[(len(e.gasCache) - e.cfg.GasCacheBlocks):]
	}

	// skip update unless cache is full
	if len(e.gasCache) < e.cfg.GasCacheBlocks {
		return
	}

	// compute the median of the median prices in the cache
	medians := make([]*big.Int, 0, len(e.gasCache))
	for _, price := range e.gasCache {
		if price != nil {
			medians = append(medians, price)
		}
	}
	if len(medians) == 0 {
		return
	}
	sort.Slice(medians, func(i, j int) bool { return medians[i].Cmp(medians[j]) == -1 })
	median := new(big.Int).Set(medians[len(medians)/2])

	// round the price up to nearest configured resolution, converting gas rate units to wei
	_, gasRateUnitsPerOne := e.cfg.ChainID.GetGasUnits()
	resolution := cosmos.NewUint(uint64(e.cfg.GasPriceResolution)).MulUint64(common.WeiPerOne).Quo(gasRateUnitsPerOne).BigInt()
	median.Add(median, new(big.Int).Sub(resolution, big.NewInt(1)))
	median = median.Div(median, resolution)
	median = median.Mul(median, resolution)
	e.gasPrice.Store(median)

	// record metrics
	gasPriceFloat, _ := new(big.Float).SetInt(median).Float64()
	e.m.GetGauge(metrics.GasPrice(e.cfg.ChainID)).Set(gasPriceFloat)
	e.m.GetCounter(metrics.GasPriceChange(e.cfg.ChainID)).Inc()
}

// reportNetworkFee reports current network fee to thornode
func (e *EVMScanner) reportNetworkFee(height int64) {
	gasPrice := e.GetGasPrice()

	// skip posting if there is not yet a fee
	if gasPrice.Sign() == 0 {
		return
	}

	// gas rate from wei to gas rate units
	_, gasRateUnitsPerOne := e.cfg.ChainID.GetGasUnits()
	gasRate := cosmos.NewUintFromBigInt(gasPrice).Mul(gasRateUnitsPerOne).QuoUint64(common.WeiPerOne).Uint64()
	if gasRate == 0 {
		gasRate = 1
	}

	networkFee, postNetworkFee, checkThorchainFee := e.prepareNetworkFeeReport(height, gasRate, nil)
	if checkThorchainFee {
		hasNetworkFee, err := e.bridge.HasNetworkFee(e.cfg.ChainID)
		if err != nil || hasNetworkFee {
			return
		}

		// A concurrent scanner may post the same fee between HasNetworkFee and this
		// retry. Duplicate network-fee observations are harmless because THORChain
		// handles them idempotently.
		networkFee, postNetworkFee, _ = e.prepareNetworkFeeReport(height, gasRate, &hasNetworkFee)
	}

	if postNetworkFee {
		e.globalNetworkFeeQueue <- networkFee
	}
}

// prepareNetworkFeeReport decides whether the current gas rate should be reported.
// It returns the fee to post, whether it should be posted, and whether the caller
// must check THORNode for an existing network fee before deciding.
func (e *EVMScanner) prepareNetworkFeeReport(height int64, gasRate uint64, hasNetworkFee *bool) (common.NetworkFee, bool, bool) {
	// FetchTxs can run in prefetch goroutines. Keep the fee comparison and
	// lastReportedGasPrice update atomic, but leave THORNode RPCs to the caller.
	e.networkFeeMu.Lock()
	defer e.networkFeeMu.Unlock()

	// skip fee if less than 1 resolution away from the last
	lastReportedGasPrice := e.lastReportedGasPrice.Load()
	var feeDelta uint64
	if gasRate >= lastReportedGasPrice {
		feeDelta = gasRate - lastReportedGasPrice
	} else {
		feeDelta = lastReportedGasPrice - gasRate
	}
	if lastReportedGasPrice != 0 && feeDelta <= uint64(e.cfg.GasPriceResolution) {
		if height%100 != 0 {
			return common.NetworkFee{}, false, false
		}

		// every 100 blocks send the fee if none is set
		if hasNetworkFee == nil {
			// The caller must perform HasNetworkFee outside networkFeeMu and retry
			// this decision with the result.
			return common.NetworkFee{}, false, true
		}
		if *hasNetworkFee {
			return common.NetworkFee{}, false, false
		}
	}

	e.lastReportedGasPrice.Store(gasRate)
	// build the network fee to post to THORChain after releasing networkFeeMu
	return common.NetworkFee{
		Chain:           e.cfg.ChainID,
		Height:          height,
		TransactionSize: e.cfg.MaxGasLimit,
		TransactionRate: gasRate,
	}, true, false
}

// --------------------------------- parse transaction ---------------------------------

func (e *EVMScanner) getTxInFromTransaction(tx *etypes.Transaction, receipt *etypes.Receipt, providers ...pubkeymanager.PubKeyValidator) (*stypes.TxInItem, error) {
	provider := e.pubkeyMgr
	if len(providers) > 0 {
		provider = providers[0]
	}
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:], // drop the "0x" prefix
	}

	sender, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	txInItem.To = strings.ToLower(tx.To().String())

	// on native transactions the memo is hex encoded in the data field
	data := tx.Data()
	if len(data) > 0 {
		var memo []byte
		memo, err = hex.DecodeString(string(data))
		if err != nil {
			txInItem.Memo = string(data)
		} else {
			txInItem.Memo = string(memo)
		}
	}

	nativeValue := e.tokenManager.ConvertAmount(evm.NativeTokenAddr, tx.Value())
	txInItem.Coins = append(txInItem.Coins, common.NewCoin(e.cfg.ChainID.GetGasAsset(), nativeValue))
	txGasPrice := receipt.EffectiveGasPrice
	if txGasPrice == nil {
		txGasPrice = big.NewInt(0)
	}
	txInItem.Gas = common.MakeEVMGas(e.cfg.ChainID, txGasPrice, receipt.GasUsed, receipt.L1Fee)
	txInItem.Gas[0].Asset = e.cfg.ChainID.GetGasAsset()

	// drop non-outbound transactions below dust threshold
	isOutbound, _ := provider.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
	if !isOutbound &&
		len(txInItem.Coins) == 1 &&
		txInItem.Coins[0].Asset.Equals(e.cfg.ChainID.GetGasAsset()) &&
		txInItem.Coins[0].Amount.LT(e.cfg.ChainID.DustThreshold()) {

		e.logger.Debug().Str("hash", tx.Hash().Hex()).Msg("dropping tx below dust threshold")
		return nil, nil
	}

	if txInItem.Coins.IsEmpty() {
		if isOutbound {
			// outbound with no coins that is not a cancel is assumed to be router approval
			isCancel := txInItem.Sender == txInItem.To
			if !isCancel {
				txInItem.Memo = "" // clear the memo (otherwise contains call data)
			}

			// Outbounds with no coins are either a cancel from unstuck.go, or a router
			// token approval - both should be observed to take gas fees from the vault.
			observableAmount := e.cfg.ChainID.DustThreshold()
			txInItem.Coins = common.NewCoins(common.NewCoin(txInItem.Gas[0].Asset, observableAmount))

			// remove the outbound from signer cache so it can be re-attempted
			e.signerCacheManager.RemoveSigned(tx.Hash().Hex())
		} else {
			e.logger.Debug().Msgf("there is no coin in this tx, ignore, %+v", txInItem)
			return nil, nil
		}
	}

	return txInItem, nil
}

// isToValidContractAddress this method make sure the transaction to address is to
// THORChain router or a whitelist address
func (e *EVMScanner) isToValidContractAddress(addr *ecommon.Address, includeWhiteList bool, providers ...pubkeymanager.PubKeyValidator) (valid bool) {
	provider := e.pubkeyMgr
	if len(providers) > 0 {
		provider = providers[0]
	}
	if retained, ok := provider.(*evm.RecoveryPubKeyValidator); ok && addr != nil {
		defer func() { retained.RecordContractLookup(addr.String(), valid) }()
		if retained.IsValidContract(addr.String()) {
			return true
		}
	}
	if addr == nil {
		return false
	}
	// get the smart contract used by thornode
	contractAddresses := provider.GetContracts(e.cfg.ChainID, false)

	// EqualFold-equal to the checksummed addr.String(), without its keccak256
	addrString := evm.LowerHexAddress(*addr)
	for _, item := range contractAddresses {
		if strings.EqualFold(item.String(), addrString) {
			return true
		}
	}
	// combine the whitelist smart contract address
	if includeWhiteList {
		for _, item := range e.whitelistContracts {
			if strings.EqualFold(item.String(), addrString) {
				return true
			}
		}
	}
	return false
}

func (e *EVMScanner) isRelevantERC20Token(addr *ecommon.Address) bool {
	if addr == nil || e.tokenManager == nil {
		return false
	}
	return e.tokenManager.IsRelevantToken(addr.String())
}

func (e *EVMScanner) directERC20InboundEnabled() bool {
	enabled, err := e.bridge.GetMimirWithRef(
		constants.MimirTemplateEVMDirectERC20Inbound,
		e.cfg.ChainID.String(),
	)
	if err != nil {
		e.logger.Err(err).
			Str("mimir_template", constants.MimirTemplateEVMDirectERC20Inbound).
			Str("chain", e.cfg.ChainID.String()).
			Msg("fail to get direct ERC20 inbound mimir")
		return false
	}
	return enabled > 0
}

// getTxInFromDirectERC20Transfer observes direct ERC20 token transfers sent to
// active vault addresses. These txs bypass the router, so they are emitted as
// memo-less inbounds and left to THORNode's normal memoless/refund handling.
func (e *EVMScanner) getTxInFromDirectERC20Transfer(tx *etypes.Transaction, receipt *etypes.Receipt, sender *ecommon.Address, providers ...pubkeymanager.PubKeyValidator) (*stypes.TxInItem, error) {
	provider := e.pubkeyMgr
	if len(providers) > 0 {
		provider = providers[0]
	}
	var l1Fee *big.Int
	if receipt != nil {
		l1Fee = receipt.L1Fee
	}
	return evm.GetTxInFromDirectERC20Transfer(tx, receipt, evm.DirectERC20TransferTxInOptions{
		Chain:                 e.cfg.ChainID,
		Signer:                e.eipSigner,
		Sender:                sender,
		MaxContractTxLogs:     int64(e.cfg.MaxContractTxLogs),
		L1Fee:                 l1Fee,
		PoolAddressProvider:   provider,
		TokenResolver:         e.tokenManager,
		Logger:                e.logger,
		SenderErrorMessage:    "failed to get sender",
		ParseLogsErrorMessage: "failed to parse direct ERC20 transfer logs",
	})
}

// getTxInFromSmartContract returns txInItem
func (e *EVMScanner) getTxInFromSmartContract(tx *etypes.Transaction, receipt *etypes.Receipt, maxLogs int64, providers ...pubkeymanager.PubKeyValidator) (*stypes.TxInItem, error) {
	provider := e.pubkeyMgr
	if len(providers) > 0 {
		provider = providers[0]
	}
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:], // drop the "0x" prefix
	}
	sender, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	// 1 is Transaction success state
	if receipt.Status != 1 {
		e.logger.Debug().Stringer("txid", tx.Hash()).Uint64("status", receipt.Status).Msg("tx failed")
		return nil, nil
	}
	p := evm.NewSmartContractLogParser(
		func(address *ecommon.Address, includeWhitelist bool) bool {
			return e.isToValidContractAddress(address, includeWhitelist, provider)
		},
		e.tokenManager.GetAssetFromTokenAddress,
		e.tokenManager.GetTokenDecimalsForTHORChain,
		e.tokenManager.ConvertAmount,
		e.vaultABI,
		e.cfg.ChainID.GetGasAsset(),
		maxLogs,
	)

	// txInItem will be changed in p.getTxInItem function, so if the function return an
	// error txInItem should be abandoned
	isVaultTransfer, err := p.GetTxInItem(receipt.Logs, txInItem)
	if err != nil {
		return nil, fmt.Errorf("failed to parse logs, err: %w", err)
	}
	if isVaultTransfer {
		contractAddresses := provider.GetContracts(e.cfg.ChainID, false)
		isDirectlyToRouter := false
		if retained, ok := provider.(*evm.RecoveryPubKeyValidator); ok {
			isDirectlyToRouter = retained.IsValidContract(tx.To().String())
		}
		for _, item := range contractAddresses {
			if strings.EqualFold(item.String(), tx.To().String()) {
				isDirectlyToRouter = true
				break
			}
		}
		if isDirectlyToRouter {
			// it is important to keep this part outside the above loop, as when we do router
			// upgrade, which might generate multiple deposit event, along with tx that has
			// native value in it
			nativeValue := cosmos.NewUintFromBigInt(tx.Value())
			if !nativeValue.IsZero() {
				nativeValue = e.tokenManager.ConvertAmount(evm.NativeTokenAddr, tx.Value())
				if txInItem.Coins.GetCoin(e.cfg.ChainID.GetGasAsset()).IsEmpty() && !nativeValue.IsZero() {
					txInItem.Coins = append(txInItem.Coins, common.NewCoin(e.cfg.ChainID.GetGasAsset(), nativeValue))
				}
			}
		}
	}

	// Validate that the deposit's target vault is associated with the router
	// that emitted the event. This prevents an attacker from depositing to an
	// old router targeting the current active vault (or vice versa), which would
	// cause bifrost to observe an inbound the vault cannot actually spend.
	// Skip this check for vault-initiated transactions (e.g. migrations), which
	// legitimately use the old router to send to a vault on a new router.
	// NOTE: We compare against the log-emitting address (the actual router that
	// produced the Deposit event), NOT tx.To(), because aggregator contracts
	// forward deposits to the router internally — tx.To() is the aggregator.
	if txInItem.To != "" {
		senderIsVault, _ := provider.IsValidPoolAddress(txInItem.Sender, e.cfg.ChainID)
		if !senderIsVault {
			ok, cpi := provider.IsValidPoolAddress(txInItem.To, e.cfg.ChainID)
			if ok {
				expectedRouter := provider.GetContract(e.cfg.ChainID, cpi.PubKey)
				if expectedRouter != common.NoAddress {
					depositRouter := evm.DepositEventRouter(receipt.Logs)
					if depositRouter != nil && !strings.EqualFold(expectedRouter.String(), depositRouter.String()) {
						e.logger.Warn().
							Str("tx", txInItem.Tx).
							Str("vault", txInItem.To).
							Str("sender", txInItem.Sender).
							Str("expected_router", expectedRouter.String()).
							Str("deposit_router", depositRouter.String()).
							Msg("deposit targets vault via wrong router, ignoring")
						return nil, nil
					}
				}
			}
		}
	}

	// under no circumstance EVM gas price will be less than 1 Gwei, unless it is in dev environment
	txGasPrice := receipt.EffectiveGasPrice
	if txGasPrice == nil {
		txGasPrice = big.NewInt(0)
	}
	txInItem.Gas = common.MakeEVMGas(e.cfg.ChainID, txGasPrice, receipt.GasUsed, receipt.L1Fee)
	if txInItem.Coins.IsEmpty() {
		return nil, nil
	}
	return txInItem, nil
}

// getTxInFromFailedTransaction when a transaction failed due to out of gas, this method
// will check whether the transaction is an outbound. It fakes a txInItem if the failed
// transaction is an outbound, and reports it back to THORNode so the gas fee can be
// deducted from the vault balance to keep accounting in sync.
func (e *EVMScanner) getTxInFromFailedTransaction(tx *etypes.Transaction, receipt *etypes.Receipt, providers ...pubkeymanager.PubKeyValidator) *stypes.TxInItem {
	provider := e.pubkeyMgr
	if len(providers) > 0 {
		provider = providers[0]
	}
	if receipt.Status == 1 {
		e.logger.Info().Str("hash", tx.Hash().String()).Msg("success transaction should not get into getTxInFromFailedTransaction")
		return nil
	}
	fromAddr, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		e.logger.Err(err).Msg("failed to get from address")
		return nil
	}
	ok, cif := provider.IsValidPoolAddress(fromAddr.String(), e.cfg.ChainID)
	if !ok || cif.IsEmpty() {
		return nil
	}
	txGasPrice := receipt.EffectiveGasPrice
	if txGasPrice == nil {
		txGasPrice = big.NewInt(0)
	}
	txHash := tx.Hash().Hex()[2:]
	return &stypes.TxInItem{
		EVMFailedSynthetic: true,
		Tx:                 txHash,
		Memo:               memo.NewOutboundMemo(common.TxID(strings.ToUpper(txHash))).String(),
		Sender:             strings.ToLower(fromAddr.String()),
		To:                 strings.ToLower(tx.To().String()),
		Coins:              common.NewCoins(common.NewCoin(e.cfg.ChainID.GetGasAsset(), cosmos.NewUint(1))),
		Gas:                common.MakeEVMGas(e.cfg.ChainID, txGasPrice, receipt.GasUsed, receipt.L1Fee),
	}
}

// canonicalObservationView shares one generation and evidence cache across scanner
// and Observer checks. Lazy initialization is serialized with concurrent callers.
func (e *EVMScanner) canonicalObservationView() *evm.CanonicalValidator {
	e.canonicalViewMu.Lock()
	defer e.canonicalViewMu.Unlock()
	if e.canonicalView == nil {
		e.canonicalView = evm.NewCanonicalValidator(e.ethRpc, e.cfg.BlockHeightDiscoverBackoff)
	}
	return e.canonicalView
}

// occurrenceJournal returns the single owner of recovery records and metadata writes.
// Construction errors leave it unset so a later call can retry initialization.
func (e *EVMScanner) occurrenceJournal() (*evm.OccurrenceJournal, error) {
	e.occurrenceJournalMu.Lock()
	defer e.occurrenceJournalMu.Unlock()
	if e.journal == nil {
		var err error
		e.journal, err = evm.NewOccurrenceJournal(e.blockMetaAccessor, e.cfg.ChainID)
		if err != nil {
			return nil, err
		}
	}
	return e.journal, nil
}

// ServiceRecovery runs one bounded recovery pass independently of extraction.
// The scan-loop supplies its completed delivery height so prefetch cannot claim
// spare recovery time. Maintenance still runs while paused or caught up.
func (e *EVMScanner) ServiceRecovery(scannedHeight int64, paused bool) error {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	journal, err := e.occurrenceJournal()
	if err != nil {
		return err
	}
	if err = journal.ReportStorage(); err != nil {
		return err
	}
	if scannedHeight >= 0 && scannedHeight < math.MaxInt64 {
		if err = journal.ReportReplayWindow(scannedHeight+1, e.cfg.FloorBlockHeight); err != nil {
			return err
		}
	}
	return e.serviceRecovery(journal, scannedHeight, paused)
}

// serviceRecovery runs matching and maturity checks before cleanup delivery. A failed
// cleanup must not starve unrelated watches. The caller holds scanMu; this pass
// never advances the ordinary observation cursor.
func (e *EVMScanner) serviceRecovery(journal *evm.OccurrenceJournal, scannedHeight int64, paused bool) error {
	maintenanceErr := journal.RefreshAttribution(e.pubkeyMgr, e.canonicalObservationView())
	if err := evm.ServiceOccurrenceMatcher(journal, e.ethRpc, e.canonicalObservationView(), e.eipSigner, scannedHeight, paused); err != nil {
		maintenanceErr = errors.Join(maintenanceErr, err)
	}
	if e.confirmationPolicy != nil {
		// BSC/POL policy is fixed for every block; decoding vault outputs would add
		// receipt RPCs without changing this requirement.
		policy := func(*etypes.Block) (int64, error) { return e.confirmationPolicy(stypes.TxIn{Chain: e.cfg.ChainID}) }

		if err := evm.ProbeOccurrenceCandidates(journal, e.ethRpc, e.canonicalObservationView(), e.eipSigner, policy, e.globalErrataQueue, e.cfg.HTTPRequestTimeout); err != nil {
			maintenanceErr = errors.Join(maintenanceErr, err)
		}
		if probeErr := evm.ProbeOccurrenceWatches(journal, e.ethRpc, e.canonicalObservationView(), e.eipSigner, policy, e.globalErrataQueue, e.cfg.HTTPRequestTimeout, &e.watchOffset); probeErr != nil {
			maintenanceErr = errors.Join(maintenanceErr, probeErr)
		}
	}

	return errors.Join(maintenanceErr, evm.DrainOccurrenceCleanup(journal, e.canonicalObservationView(), e.globalErrataQueue, e.cfg.HTTPRequestTimeout))
}

// RecoveryObservationVaults reads retained local network attribution. It does not
// certify canonicality or maturity; those gates still run on the extracted item.
func (e *EVMScanner) RecoveryObservationVaults(item stypes.TxInItem) (common.PubKeys, error) {
	journal, err := e.occurrenceJournal()
	if err != nil {
		return nil, err
	}
	return journal.RecoveryObservationVaults(item)
}
