package evm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
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
	// endpointMgr is set by the client; with several endpoints, a reorg is read
	// from one witness endpoint proven on the scanned block's fork (evm.ReorgWitness)
	endpointMgr     *endpoints.Manager
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
	gasCache              []*big.Int
	solvencyReporter      SolvencyReporter
	whitelistTokens       []tokenlist.ERC20Token
	whitelistContracts    []common.Address
	signerCacheManager    *signercache.CacheManager
	tokenManager          *evm.TokenManager

	vaultABI *abi.ABI
	erc20ABI *abi.ABI
}

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

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (e *EVMScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	// log height every 100 blocks
	if height%100 == 0 {
		e.logger.Info().Int64("height", height).Msg("fetching txs for height")
	}

	// process all transactions in the block
	e.updateCurrentBlockHeight(height)
	block, err := e.ethRpc.GetBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	txIn, err := e.processBlock(block, chainHeight)
	if err != nil {
		e.logger.Error().Err(err).Int64("height", height).Msg("failed to search tx in block")
		return stypes.TxIn{}, fmt.Errorf("failed to process block: %d, err:%w", height, err)
	}

	// if reorgs are possible on this chain store block meta for handling
	if e.cfg.MaxReorgRescanBlocks > 0 {
		blockMeta := evmtypes.NewBlockMeta(block.Header(), txIn)
		if err = e.blockMetaAccessor.SaveBlockMeta(height, blockMeta); err != nil {
			e.logger.Err(err).Int64("height", height).Msg("fail to save block meta")
		}
		pruneHeight := height - e.cfg.MaxReorgRescanBlocks
		if pruneHeight > 0 {
			defer func() {
				if err = e.blockMetaAccessor.PruneBlockMeta(pruneHeight); err != nil {
					e.logger.Err(err).Int64("height", height).Msg("fail to prune block meta")
				}
			}()
		}
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

func (e *EVMScanner) processBlock(block *etypes.Block, chainHeight int64) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		TxArray:  nil,
		Filtered: false,
		MemPool:  false,
	}

	// skip empty blocks
	if block.Transactions().Len() == 0 {
		return txIn, nil
	}

	// Reorg handling is planned first and committed last: errata, repaired block
	// metas and the rescanned observations are reported and persisted only once the
	// current block has been extracted too. If any step fails none of them was
	// written, so the retry of this height detects the reorg again and redoes all of
	// it. (Extraction's own cleanup, the signed-tx items and signer-cache entries of
	// outbounds seen in a block and stored token metadata, may already have run; it
	// is idempotent and the retry redoes it.)
	var plan *reorgPlan
	if e.cfg.MaxReorgRescanBlocks > 0 {
		var err error
		plan, err = e.planReorgWith(block.Header(), block.Transactions(), chainHeight)
		if err != nil {
			e.logger.Error().Err(err).Msgf("fail to process reorg for block %d", block.NumberU64())
			return txIn, err
		}
	}

	// collect all relevant transactions from the block and their gas prices
	txInBlock, gasPrices, err := e.getTxIn(block, "")
	if err != nil {
		if plan != nil {
			// the planned reorg is dropped with this step: still unresolved
			err = fmt.Errorf("%w: %w", btypes.ErrReorgUnresolved, err)
		}
		return txIn, err
	}

	e.commitReorg(plan)
	if plan != nil {
		for _, item := range plan.txIns {
			if len(item.TxArray) == 0 {
				continue
			}
			txIn.TxArray = append(txIn.TxArray, item.TxArray...)
		}
	}
	if len(txInBlock.TxArray) > 0 {
		txIn.TxArray = append(txIn.TxArray, txInBlock.TxArray...)
	}

	// update gas price from collected receipt effective gas prices, once the block is
	// done, so a retried block is not counted twice
	e.updateGasPrice(gasPrices)

	return txIn, nil
}

func (e *EVMScanner) getTxInOptimized(method string, block *etypes.Block, pin string) (stypes.TxIn, []*big.Int, error) {
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
	blockNumStr := hexutil.EncodeBig(block.Number())
	err := e.ethClient.Client().CallContext(
		endpoints.PinContext(context.Background(), pin),
		&receipts,
		method,
		blockNumStr,
	)
	if err != nil {
		e.logger.Error().Err(err).Msg("failed to fetch block receipts")
		return stypes.TxIn{}, nil, err
	}

	// every transaction the block lists must have a receipt: a null or short answer
	// means the node has not imported the block yet (a lagging endpoint after a
	// failover) and the block must be retried, not scanned with transactions missing
	blockHash := block.Hash()
	have := 0
	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		// receipts are asked for by number, so a node that reorged since the block was
		// read, or another endpoint on another fork, can answer for a different block
		// at the same height: never observe those (BSC, BASE and POL compute the same
		// block hash as their nodes; AVAX does not, and does not take this path)
		if receipt.BlockHash != blockHash {
			return stypes.TxIn{}, nil, fmt.Errorf("block %d: %s returned a receipt of block %s, not of the scanned block %s",
				block.NumberU64(), method, receipt.BlockHash.Hex(), blockHash.Hex())
		}
		if _, ok := txByHash[receipt.TxHash.String()]; ok {
			have++
		}
	}
	if have < len(txByHash) {
		return stypes.TxIn{}, nil, fmt.Errorf("block %d: %s returned %d receipts for %d transactions; node has not imported the block yet",
			block.NumberU64(), method, have, len(txByHash))
	}

	// at least one worker: an unbuffered semaphore would block the first
	// receipt forever, and a negative capacity panics
	sem := make(chan struct{}, max(e.cfg.Concurrency, 1))
	wg := sync.WaitGroup{}
	mu := sync.Mutex{}
	var gasPrices []*big.Int

	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(receipt *etypes.Receipt) {
			defer wg.Done()
			defer func() { <-sem }()
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

			// extract the txInItem; err must be goroutine-local, a shared one
			// lets this goroutine drop its item on another goroutine's error
			txInItem, err := e.receiptToTxInItem(txForReceipt, receipt)
			if err != nil {
				e.logger.Error().Err(err).Msg("failed to convert receipt to txInItem")
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
			mu.Lock()
			txInbound.TxArray = append(txInbound.TxArray, txInItem)
			mu.Unlock()
		}(receipt)
	}
	wg.Wait()

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, gasPrices, nil
	}
	return txInbound, gasPrices, nil
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
// A router deposit can come through another contract, so check the bloom for log
// emitters regardless of tx.To. A direct ERC20 inbound names the vault in its
// transfer calldata. A failed outbound can have no logs and its signing vault
// may have left the vault list, so also check the local signer cache.
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

// txMayBeObserved reports whether the transaction fields alone warrant fetching receipts.
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

// getTxIn reads the receipts of block's transactions from the endpoint named by pin
// ("" for unpinned: the router picks), so a reorg rescan reads them from its witness,
// the endpoint whose block it extracts.
func (e *EVMScanner) getTxIn(block *etypes.Block, pin string) (stypes.TxIn, []*big.Int, error) {
	// CHANGEME: if an EVM chain supports some way of fetching all transaction receipts
	// within a block, register it here.
	switch e.cfg.ChainID {
	case common.BASEChain, common.BSCChain:
		// a block that cannot yield an observation needs no receipts, scanned or
		// rescanned
		if gasPrices, ok := e.gasPricesWithoutReceipts(block); ok {
			return stypes.TxIn{}, gasPrices, nil
		}
		return e.getTxInOptimized("eth_getBlockReceipts", block, pin)
	case common.POLChain:
		return e.getTxInOptimized("eth_getBlockReceipts", block, pin)
	}

	txInbound := stypes.TxIn{
		Chain:    e.cfg.ChainID,
		Filtered: false,
		MemPool:  false,
	}
	var gasPrices []*big.Int

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

		// best effort remove the tx from the signed txs (ok if it does not exist)
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Str("tx hash", tx.Hash().String()).Msg("failed to remove signed tx item")
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

		// send the batch rpc request
		err := e.ethClient.Client().BatchCallContext(endpoints.PinContext(context.Background(), pin), rpcBatch)
		if err != nil {
			e.logger.Error().Int("size", len(batch)).Err(err).Msg("failed to batch fetch transaction receipts")
			return stypes.TxIn{}, nil, err
		}

		// process the batch rpc response
		for i, elem := range rpcBatch {
			if elem.Error != nil {
				// the block lists this transaction, so its receipt exists on any node
				// that has the block: a missing or unreadable receipt (a null element
				// from a node that has not imported the block yet) fails the block so
				// it is retried instead of scanned with the transaction missing
				return stypes.TxIn{}, nil, fmt.Errorf("block %d: receipt for tx %s unavailable: %w",
					block.NumberU64(), batch[i].Hash().Hex(), elem.Error)
			}

			// get the receipt
			receipt, ok := elem.Result.(*etypes.Receipt)
			if !ok || receipt == nil {
				return stypes.TxIn{}, nil, fmt.Errorf("block %d: receipt for tx %s is not a receipt", block.NumberU64(), batch[i].Hash().Hex())
			}

			// collect effective gas price from receipt for gas price tracking
			gasPrices = append(gasPrices, receipt.EffectiveGasPrice)

			// extract the txInItem
			var txInItem *stypes.TxInItem
			txInItem, err = e.receiptToTxInItem(batch[i], receipt)
			if err != nil {
				e.logger.Error().Err(err).Msg("failed to convert receipt to txInItem")
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
			txInbound.TxArray = append(txInbound.TxArray, txInItem)
		}
	}

	if len(txInbound.TxArray) == 0 {
		e.logger.Debug().Uint64("block", block.NumberU64()).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, gasPrices, nil
	}
	return txInbound, gasPrices, nil
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

func (e *EVMScanner) receiptToTxInItem(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
	if receipt.Status != 1 {
		e.logger.Debug().Stringer("txid", tx.Hash()).Uint64("status", receipt.Status).Msg("tx failed")

		// remove failed transactions from signer cache so they are retried
		if e.signerCacheManager != nil {
			e.signerCacheManager.RemoveSigned(tx.Hash().String())
		}

		return e.getTxInFromFailedTransaction(tx, receipt), nil
	}

	disableWhitelist, err := e.bridge.GetMimir(constants.EVMDisableContractWhitelist.String())
	if err != nil {
		e.logger.Err(err).Msgf("fail to get %s", constants.EVMDisableContractWhitelist.String())
		disableWhitelist = 0
	}

	if disableWhitelist == 1 {
		// parse tx without whitelist
		destination := tx.To()
		isToVault, _ := e.pubkeyMgr.IsValidPoolAddress(destination.String(), e.cfg.ChainID)

		// resolve sender upfront to check vault outbounds (approvals, cancels, etc.)
		sender, senderErr := etypes.Sender(e.eipSigner, tx)
		var isFromVault bool
		if senderErr == nil {
			isFromVault, _ = e.pubkeyMgr.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
		}

		switch {
		case isToVault:
			// Tx to a vault
			return e.getTxInFromTransaction(tx, receipt)
		case e.isToValidContractAddress(destination, true):
			// Deposit directly to router
			return e.getTxInFromSmartContract(tx, receipt, 0)
		case isFromVault:
			// Tx from a vault (outbound, approval, cancel, etc.)
			return e.getTxInFromTransaction(tx, receipt)
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
			return e.getTxInFromDirectERC20Transfer(tx, receipt, &sender)
		case evm.IsSmartContractCall(tx, receipt):
			// Tx to a different contract, attempt to parse with max allowable logs
			return e.getTxInFromSmartContract(tx, receipt, int64(e.cfg.MaxContractTxLogs))
		default:
			// ignore tx not involving any vault
			return nil, nil
		}
	} else {
		// parse tx with whitelist
		if e.isToValidContractAddress(tx.To(), true) {
			return e.getTxInFromSmartContract(tx, receipt, 0)
		}
		// check if tx is to or from a vault
		sender, err := etypes.Sender(e.eipSigner, tx)
		if err != nil {
			return nil, nil // ignore tx if we cannot determine sender
		}
		isFromVault, _ := e.pubkeyMgr.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
		isToVault, _ := e.pubkeyMgr.IsValidPoolAddress(tx.To().String(), e.cfg.ChainID)
		if isFromVault || isToVault {
			return e.getTxInFromTransaction(tx, receipt)
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
			return e.getTxInFromDirectERC20Transfer(tx, receipt, &sender)
		}
		return nil, nil
	}
}

// --------------------------------- reorg ---------------------------------

// reorgPlan is the work a detected reorg requires: errata to report, repaired block
// metas to persist, and the observations of the rescanned blocks. It is computed
// without side effects and committed only once the whole scan step has succeeded.
type reorgPlan struct {
	errata []stypes.ErrataBlock
	metas  []*types.BlockMeta
	txIns  []stypes.TxIn
}

// planReorg compares the block's parent hash with the stored block hash. On a reorg
// it re-confirms the transactions of every cached block in the reorg window, works
// out the repaired metas, and rescans the blocks whose hash changed, taking headers,
// re-confirmations and rescanned blocks from one witness endpoint
// (evm.ReorgWitness); on BSC, BASE and POL the rescans' receipts must carry the
// rescanned block's hash (getTxInOptimized). It reports and persists no reorg state
// (commitReorg does, once the current block is done too; the rescans' extraction
// cleanup is idempotent), and any read that cannot be answered aborts it, so the scan
// step is retried as a whole.
func (e *EVMScanner) planReorg(header *etypes.Header) (*reorgPlan, error) {
	return e.planReorgWith(header, nil, 0)
}

// planReorgWith is planReorg for the scanned block, whose transactions are scanned,
// and the chain height the scan was driven by, which the witness must have reached
// (evm.ReorgWitness): the witness's "not found" for a stored transaction the scanned
// block lists is not believed (evm.ScannedTxHashes).
func (e *EVMScanner) planReorgWith(header *etypes.Header, scanned etypes.Transactions, chainHeight int64) (_ *reorgPlan, err error) {
	// a failure once a reorg was detected leaves it unresolved (the block scanner
	// logs a stall skip-ahead past it); one before that is an ordinary failure
	detected := false
	defer func() {
		if err != nil && detected {
			err = fmt.Errorf("%w: %w", btypes.ErrReorgUnresolved, err)
		}
	}()
	previousHeight := header.Number.Int64() - 1
	prevBlockMeta, err := e.blockMetaAccessor.GetBlockMeta(previousHeight)
	if err != nil {
		return nil, fmt.Errorf("fail to get block meta of height(%d) : %w", previousHeight, err)
	}

	// skip re-org processing if we did not store the previous block meta
	if prevBlockMeta == nil {
		return nil, nil
	}

	// no re-org if stored block hash at previous height is equal to current block parent
	if strings.EqualFold(prevBlockMeta.BlockHash, header.ParentHash.Hex()) {
		return nil, nil
	}
	e.logger.Info().
		Int64("height", previousHeight).
		Str("stored_hash", prevBlockMeta.BlockHash).
		Str("current_parent_hash", header.ParentHash.Hex()).
		Msg("reorg detected")
	detected = true

	blockMetas, err := e.blockMetaAccessor.GetBlockMetas()
	if err != nil {
		return nil, fmt.Errorf("fail to get block metas from local storage: %w", err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
	witness, err := evm.ReorgWitness(wctx, e.endpointMgr, e.ethClient, header, chainHeight)
	wcancel()
	if err != nil {
		return nil, fmt.Errorf("reorg at height %d: %w", header.Number.Int64(), err)
	}
	plan := &reorgPlan{}
	listed := evm.ScannedTxHashes(scanned)
	var rescanHeights []int64
	for _, blockMeta := range blockMetas {
		// the witness's header first: it proves the witness has this height before
		// its "not found" for the block's transactions is believed
		h, herr := e.headerFrom(witness, blockMeta.Height)
		if herr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's header %d", blockMeta.Height))
			return nil, fmt.Errorf("height %d: fail to get block header to check for reorg: %w", blockMeta.Height, herr)
		}
		metaTxs := make([]types.TransactionMeta, 0)
		var errataTxs []stypes.ErrataTx
		for _, tx := range blockMeta.Transactions {
			presence, rblock, cerr := e.ethRpc.CheckTransactionWithBlock(witness, tx.Hash)
			if cerr != nil {
				evm.SidelineWitness(e.endpointMgr, witness, "cannot re-confirm a transaction for the reorg plan")
				return nil, fmt.Errorf("height %d: cannot re-confirm tx %s: %w", blockMeta.Height, tx.Hash, cerr)
			}
			if presence == evm.TxFailed {
				fctx, fcancel := context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
				ferr := evm.FailedReceiptOnScannedFork(fctx, e.endpointMgr, e.ethClient, witness, header, listed[evm.TxMetaKey(tx.Hash)], rblock)
				fcancel()
				if ferr != nil {
					return nil, fmt.Errorf("height %d: tx %s: %w", blockMeta.Height, tx.Hash, ferr)
				}
			}
			if presence == evm.TxOnChain {
				e.logger.Debug().Msgf("height: %d, tx: %s still exists", blockMeta.Height, tx.Hash)
				metaTxs = append(metaTxs, tx)
				continue
			}
			if presence == evm.TxNotFound && listed[evm.TxMetaKey(tx.Hash)] {
				return nil, fmt.Errorf("height %d: endpoint %q does not know tx %s, which the scanned block lists: its answer does not match the scanned block it holds", blockMeta.Height, witness, tx.Hash)
			}

			// the node says the transaction is not on chain (any more): errata it
			errataTxs = append(errataTxs, stypes.ErrataTx{
				TxID:  common.TxID(tx.Hash),
				Chain: e.cfg.ChainID,
			})
		}
		if len(errataTxs) > 0 {
			plan.errata = append(plan.errata, stypes.ErrataBlock{Height: blockMeta.Height, Txs: errataTxs})
		}

		// if the block hash is different than previously recorded, rescan the block
		if !strings.EqualFold(blockMeta.BlockHash, h.Hash().Hex()) {
			rescanHeights = append(rescanHeights, blockMeta.Height)
		}

		blockMeta.PreviousHash = h.ParentHash.Hex()
		blockMeta.BlockHash = h.Hash().Hex()
		blockMeta.Transactions = metaTxs
		plan.metas = append(plan.metas, blockMeta)
	}

	// rescan heights
	for _, rescanHeight := range rescanHeights {
		e.logger.Info().Msgf("rescan block height: %d", rescanHeight)
		block, berr := e.blockFrom(witness, rescanHeight)
		if berr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's block %d", rescanHeight))
			return nil, fmt.Errorf("rescan height %d: %w", rescanHeight, berr)
		}
		if block.Transactions().Len() == 0 {
			continue
		}
		txIn, _, xerr := e.getTxIn(block, witness) // discard gas prices during reorg rescan
		if xerr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's receipts of block %d", rescanHeight))
			return nil, fmt.Errorf("rescan height %d: %w", rescanHeight, xerr)
		}
		if len(txIn.TxArray) > 0 {
			plan.txIns = append(plan.txIns, txIn)
		}
	}
	// every answer above came from the witness: it must still hold the scanned block,
	// or they may describe another fork (a failed receipt from there is no errata)
	cctx, ccancel := context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
	err = evm.WitnessStillHolds(cctx, e.endpointMgr, e.ethClient, witness, header)
	ccancel()
	if err != nil {
		return nil, fmt.Errorf("reorg at height %d: %w", header.Number.Int64(), err)
	}
	return plan, nil
}

// headerFrom reads a header from the endpoint named by pin ("" for unpinned).
func (e *EVMScanner) headerFrom(pin string, height int64) (*etypes.Header, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
	defer cancel()
	return e.ethClient.HeaderByNumber(endpoints.PinContext(ctx, pin), big.NewInt(height))
}

// blockFrom reads a block from the endpoint named by pin ("" for unpinned).
func (e *EVMScanner) blockFrom(pin string, height int64) (*etypes.Block, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
	defer cancel()
	return e.ethClient.BlockByNumber(endpoints.PinContext(ctx, pin), big.NewInt(height))
}

// commitReorg reports the errata and persists the repaired block metas of a plan.
// It runs only after every fallible step of the scan step has succeeded.
func (e *EVMScanner) commitReorg(plan *reorgPlan) {
	if plan == nil {
		return
	}
	for _, errata := range plan.errata {
		e.globalErrataQueue <- errata
	}
	for _, blockMeta := range plan.metas {
		if err := e.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
			e.logger.Err(err).Int64("height", blockMeta.Height).Msg("fail to save block meta")
		}
	}
}

// --------------------------------- gas ---------------------------------

// updateGasPrice calculates and stores the current gas price to reported to thornode
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

func (e *EVMScanner) getTxInFromTransaction(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
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
	isOutbound, _ := e.pubkeyMgr.IsValidPoolAddress(sender.String(), e.cfg.ChainID)
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
func (e *EVMScanner) isToValidContractAddress(addr *ecommon.Address, includeWhiteList bool) bool {
	if addr == nil {
		return false
	}
	// get the smart contract used by thornode
	contractAddresses := e.pubkeyMgr.GetContracts(e.cfg.ChainID, false)

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
func (e *EVMScanner) getTxInFromDirectERC20Transfer(tx *etypes.Transaction, receipt *etypes.Receipt, sender *ecommon.Address) (*stypes.TxInItem, error) {
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
		PoolAddressProvider:   e.pubkeyMgr,
		TokenResolver:         e.tokenManager,
		Logger:                e.logger,
		SenderErrorMessage:    "failed to get sender",
		ParseLogsErrorMessage: "failed to parse direct ERC20 transfer logs",
	})
}

// getTxInFromSmartContract returns txInItem
func (e *EVMScanner) getTxInFromSmartContract(tx *etypes.Transaction, receipt *etypes.Receipt, maxLogs int64) (*stypes.TxInItem, error) {
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
		e.isToValidContractAddress,
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
		contractAddresses := e.pubkeyMgr.GetContracts(e.cfg.ChainID, false)
		isDirectlyToRouter := false
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
		senderIsVault, _ := e.pubkeyMgr.IsValidPoolAddress(txInItem.Sender, e.cfg.ChainID)
		if !senderIsVault {
			ok, cpi := e.pubkeyMgr.IsValidPoolAddress(txInItem.To, e.cfg.ChainID)
			if ok {
				expectedRouter := e.pubkeyMgr.GetContract(e.cfg.ChainID, cpi.PubKey)
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
func (e *EVMScanner) getTxInFromFailedTransaction(tx *etypes.Transaction, receipt *etypes.Receipt) *stypes.TxInItem {
	if receipt.Status == 1 {
		e.logger.Info().Str("hash", tx.Hash().String()).Msg("success transaction should not get into getTxInFromFailedTransaction")
		return nil
	}
	fromAddr, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		e.logger.Err(err).Msg("failed to get from address")
		return nil
	}
	ok, cif := e.pubkeyMgr.IsValidPoolAddress(fromAddr.String(), e.cfg.ChainID)
	if !ok || cif.IsEmpty() {
		return nil
	}
	txGasPrice := receipt.EffectiveGasPrice
	if txGasPrice == nil {
		txGasPrice = big.NewInt(0)
	}
	txHash := tx.Hash().Hex()[2:]
	return &stypes.TxInItem{
		Tx:     txHash,
		Memo:   memo.NewOutboundMemo(common.TxID(strings.ToUpper(txHash))).String(),
		Sender: strings.ToLower(fromAddr.String()),
		To:     strings.ToLower(tx.To().String()),
		Coins:  common.NewCoins(common.NewCoin(e.cfg.ChainID.GetGasAsset(), cosmos.NewUint(1))),
		Gas:    common.MakeEVMGas(e.cfg.ChainID, txGasPrice, receipt.GasUsed, receipt.L1Fee),
	}
}
