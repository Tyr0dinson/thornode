package ethereum

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

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/semaphore"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	tokenlist "gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// SolvencyReporter is to report solvency info to THORNode
type SolvencyReporter func(int64) error

const (
	ethToken        = "0x0000000000000000000000000000000000000000"
	symbolMethod    = "symbol"
	decimalMethod   = "decimals"
	defaultDecimals = 18 // on ETH , consolidate all decimals to 18, in Wei
	tenGwei         = 10000000000
	// prefixTokenMeta declares prefix to use in leveldb to avoid conflicts
	prefixTokenMeta    = `eth-tokenmeta-` // nolint gosec:G101 not a hardcoded credential
	prefixBlockMeta    = `eth-blockmeta-`
	prefixSignedTxItem = `signed-txitem-`
)

// ETHScanner is a scanner that understand how to interact with ETH chain ,and scan block , parse smart contract etc
type ETHScanner struct {
	// endpointMgr is set by the client; with several endpoints, a reorg is read
	// from one witness endpoint proven on the scanned block's fork (evm.ReorgWitness)
	endpointMgr *endpoints.Manager
	// receipts moves an ordinary scan off an active endpoint that cannot serve the
	// receipts of the blocks it serves
	receipts evm.ReceiptsFailover
	// metaMu serializes the read-modify-write of stored block metas between the
	// observer's onObservedTxIn and a reorg commit, so neither loses the other's write
	metaMu          sync.Mutex
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
	client                *ethclient.Client
	blockMetaAccessor     evm.BlockMetaAccessor
	globalErrataQueue     chan<- stypes.ErrataBlock
	globalNetworkFeeQueue chan<- common.NetworkFee
	vaultABI              *abi.ABI
	erc20ABI              *abi.ABI
	tokens                *evm.LevelDBTokenMeta
	bridge                thorclient.ThorchainBridge
	pubkeyMgr             pubkeymanager.PubKeyValidator
	eipSigner             etypes.Signer
	currentBlockHeight    stdatomic.Int64
	gasCache              []*big.Int
	solvencyReporter      SolvencyReporter
	whitelistTokens       []tokenlist.ERC20Token
	signerCacheManager    *signercache.CacheManager
}

// NewETHScanner create a new instance of ETHScanner
func NewETHScanner(cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorage,
	chainID *big.Int,
	client *ethclient.Client,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter SolvencyReporter,
	signerCacheManager *signercache.CacheManager,
) (*ETHScanner, error) {
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics manager is nil")
	}
	if client == nil {
		return nil, errors.New("ETH client is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}
	blockMetaAccessor, err := evm.NewLevelDBBlockMetaAccessor(prefixBlockMeta, prefixSignedTxItem, storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create block meta accessor: %w", err)
	}
	tokens, err := evm.NewLevelDBTokenMeta(storage.GetInternalDb(), prefixTokenMeta)
	if err != nil {
		return nil, fmt.Errorf("fail to create token meta db: %w", err)
	}
	err = tokens.SaveTokenMeta("ETH", ethToken, defaultDecimals)
	if err != nil {
		return nil, err
	}
	vaultABI, erc20ABI, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		return nil, fmt.Errorf("fail to create contract abi: %w", err)
	}

	scanner := &ETHScanner{
		cfg:                cfg,
		logger:             log.Logger.With().Str("module", "block_scanner").Str("chain", common.ETHChain.String()).Logger(),
		errCounter:         m.GetCounterVec(metrics.BlockScanError(common.ETHChain)),
		client:             client,
		db:                 storage,
		m:                  m,
		gasPriceChanged:    false,
		blockMetaAccessor:  blockMetaAccessor,
		tokens:             tokens,
		bridge:             bridge,
		vaultABI:           vaultABI,
		erc20ABI:           erc20ABI,
		eipSigner:          etypes.NewPragueSigner(chainID),
		pubkeyMgr:          pubkeyMgr,
		gasCache:           make([]*big.Int, 0),
		solvencyReporter:   solvencyReporter,
		whitelistTokens:    tokenlist.GetETHTokenList().Tokens,
		signerCacheManager: signerCacheManager,
	}
	scanner.gasPrice.Store(big.NewInt(initialGasPrice))
	return scanner, nil
}

// GetGasPrice returns current gas price
func (e *ETHScanner) GetGasPrice() *big.Int {
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

func (e *ETHScanner) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), e.cfg.HTTPRequestTimeout)
}

// GetHeight return latest block height
func (e *ETHScanner) GetHeight() (int64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	height, err := e.client.BlockNumber(ctx)
	if err != nil {
		return -1, fmt.Errorf("fail to get block height: %w", err)
	}
	return int64(height), nil
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (e *ETHScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return e.cfg.MaxGasLimit, e.lastReportedGasPrice.Load()
}

// FetchMemPool get tx from mempool
func (e *ETHScanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// GetTokens return all the token meta data
func (e *ETHScanner) GetTokens() ([]*types.TokenMeta, error) {
	return e.tokens.GetTokens()
}

// FetchTxs query the ETH chain to get txs in the given block height
func (e *ETHScanner) FetchTxs(height, chainHeight int64) (stypes.TxIn, error) {
	block, err := e.getRPCBlock(height)
	if err != nil {
		return stypes.TxIn{}, err
	}
	txIn, err := e.processBlock(block, chainHeight)
	if err != nil {
		e.logger.Error().Err(err).Int64("height", height).Msg("fail to search tx in block")
		return stypes.TxIn{}, fmt.Errorf("fail to process block: %d, err:%w", height, err)
	}

	// blockMeta need to be saved , even there is no transactions found on this block at the time of scan
	// because at the time of scan , so the block hash will be stored, and it can be used to detect re-org
	blockMeta := types.NewBlockMeta(block.Header(), txIn)
	if err = e.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
		e.logger.Err(err).Msgf("fail to save block meta of height: %d ", blockMeta.Height)
	}

	e.updateCurrentBlockHeight(height)
	pruneHeight := height - e.cfg.MaxReorgRescanBlocks
	if pruneHeight > 0 {
		defer func() {
			if pruneErr := e.blockMetaAccessor.PruneBlockMeta(pruneHeight); pruneErr != nil {
				e.logger.Err(pruneErr).Msgf("fail to prune block meta, height(%d)", pruneHeight)
			}
		}()
	}

	// skip reporting network fee if block more than flexibility blocks from tip
	if chainHeight-height > e.cfg.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	// gas rate from wei to gas rate units
	gasPrice := e.GetGasPrice()
	_, gasRateUnitsPerOne := e.cfg.ChainID.GetGasUnits()
	gasRate := cosmos.NewUintFromBigInt(gasPrice).Mul(gasRateUnitsPerOne).QuoUint64(common.WeiPerOne).Uint64()
	if gasRate == 0 {
		gasRate = 1
	}

	// FetchTxs can run in prefetch goroutines. Serialize fee comparison and lastReportedGasPrice.
	networkFee, postNetworkFee := func() (common.NetworkFee, bool) {
		e.networkFeeMu.Lock()
		defer e.networkFeeMu.Unlock()

		// post to thorchain if there is a fee and it has changed
		if gasPrice.Sign() == 0 || gasRate == e.lastReportedGasPrice.Load() {
			return common.NetworkFee{}, false
		}
		e.lastReportedGasPrice.Store(gasRate)

		return common.NetworkFee{
			Chain:           common.ETHChain,
			Height:          height,
			TransactionSize: e.cfg.MaxGasLimit,
			TransactionRate: gasRate,
		}, true
	}()
	if postNetworkFee {
		e.globalNetworkFeeQueue <- networkFee
	}

	return txIn, nil
}

// ReportSolvency runs after the shared scanner has recomputed health.
func (e *ETHScanner) ReportSolvency(height, chainHeight int64) error {
	if chainHeight-height > e.cfg.ObservationFlexibilityBlocks || e.solvencyReporter == nil {
		return nil
	}
	return e.solvencyReporter(height)
}

func (e *ETHScanner) getCurrentBlockHeight() int64 {
	return e.currentBlockHeight.Load()
}

// updateCurrentBlockHeight records the highest scanner height seen without
// allowing concurrent prefetches to move it backwards.
func (e *ETHScanner) updateCurrentBlockHeight(height int64) {
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

// updateGasPrice records base fee + 25th percentile priority fee, rounded up 10 gwei.
func (e *ETHScanner) updateGasPrice(baseFee *big.Int, priorityFees []*big.Int) {
	// skip empty blocks
	if len(priorityFees) == 0 {
		return
	}

	// find the 25th percentile priority fee in the block
	sort.Slice(priorityFees, func(i, j int) bool { return priorityFees[i].Cmp(priorityFees[j]) == -1 })
	priorityFee := priorityFees[len(priorityFees)/4]

	// consider gas price as base fee + 25th percentile priority fee
	gasPriceWei := new(big.Int).Add(baseFee, priorityFee)

	// round the price up to nearest configured resolution, converting gas rate units to wei
	_, gasRateUnitsPerOne := e.cfg.ChainID.GetGasUnits()
	resolution := cosmos.NewUint(uint64(e.cfg.GasPriceResolution)).MulUint64(common.WeiPerOne).Quo(gasRateUnitsPerOne).BigInt()
	gasPriceWei.Add(gasPriceWei, resolution)
	gasPriceWei.Sub(gasPriceWei, big.NewInt(1))
	gasPriceWei = gasPriceWei.Div(gasPriceWei, resolution)
	gasPriceWei = gasPriceWei.Mul(gasPriceWei, resolution)

	// FetchTxs can run in prefetch goroutines, so serialize gasCache updates before publishing gasPrice.
	e.gasCacheMu.Lock()
	defer e.gasCacheMu.Unlock()

	// add to the cache
	e.gasCache = append(e.gasCache, gasPriceWei)
	if len(e.gasCache) > e.cfg.GasCacheBlocks {
		e.gasCache = e.gasCache[(len(e.gasCache) - e.cfg.GasCacheBlocks):]
	}

	e.updateGasPriceFromCacheLocked()
}

func (e *ETHScanner) updateGasPriceFromCacheLocked() {
	// skip update unless cache is full
	if len(e.gasCache) < e.cfg.GasCacheBlocks {
		return
	}

	// compute the mean of cache
	sum := new(big.Int)
	for _, fee := range e.gasCache {
		sum.Add(sum, fee)
	}
	mean := new(big.Int).Quo(sum, big.NewInt(int64(e.cfg.GasCacheBlocks)))

	// compute the standard deviation of cache
	std := new(big.Int)
	for _, fee := range e.gasCache {
		v := new(big.Int).Sub(fee, mean)
		v.Mul(v, v)
		std.Add(std, v)
	}
	std.Quo(std, big.NewInt(int64(e.cfg.GasCacheBlocks)))
	std.Sqrt(std)

	// mean + 3x standard deviation over cache blocks
	gasPrice := mean.Add(mean, std.Mul(std, big.NewInt(3)))
	e.gasPrice.Store(gasPrice)

	// record metrics
	gasPriceFloat, _ := new(big.Float).SetInt(gasPrice).Float64()
	e.m.GetGauge(metrics.GasPrice(common.ETHChain)).Set(gasPriceFloat)
	e.m.GetCounter(metrics.GasPriceChange(common.ETHChain)).Inc()
}

// processBlock extracts transactions from block
func (e *ETHScanner) processBlock(block *etypes.Block, chainHeight int64) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain:    common.ETHChain,
		TxArray:  nil,
		Filtered: false,
		MemPool:  false,
	}

	// Reorg handling is planned first and committed last: errata, repaired block
	// metas and the rescanned observations are reported and persisted only once the
	// current block has been extracted too. If any step fails none of them was
	// written, so the retry of this height detects the reorg again and redoes all of
	// it. (Extraction's own cleanup, the signed-tx items and signer-cache entries of
	// outbounds seen in a block and stored token metadata, may already have run; it
	// is idempotent and the retry redoes it.)
	plan, err := e.planReorgWith(block.Header(), block.Transactions(), chainHeight)
	if err != nil {
		e.logger.Error().Err(err).Msgf("fail to process reorg for block %d", block.NumberU64())
		return txIn, err
	}

	var txInBlock stypes.TxIn
	if block.Transactions().Len() > 0 {
		source := evm.ReceiptsSource(e.endpointMgr)
		txInBlock, err = e.extractTxs(block, "")
		if err != nil {
			if errors.Is(err, errReceiptUnavailable) {
				e.receipts.Failed(e.endpointMgr, source, block.Number().Int64())
			}
			if plan != nil {
				// the planned reorg is dropped with this step: still unresolved
				err = fmt.Errorf("%w: %w", btypes.ErrReorgUnresolved, err)
			}
			return txIn, err
		}
	}

	e.receipts.Served()
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

	// the gas price is fed once the block is done, so a retried block is not counted twice
	var priorityFees []*big.Int
	for _, tx := range block.Transactions() {
		tipCap := tx.GasTipCap()
		if tipCap == nil {
			tipCap = big.NewInt(0)
		}
		priorityFees = append(priorityFees, tipCap)
	}
	e.updateGasPrice(block.BaseFee(), priorityFees)
	return txIn, nil
}

// errReceiptUnavailable marks a receipt that could not be read at all: the node
// does not have it yet (a lagging endpoint after a failover, or a reorg), or the
// request failed. Only this class of error fails the block so it is retried. Every
// other error is about the transaction's content (an unparsable log, an unknown
// sender) and would fail the same way on every retry, so it is logged and the
// transaction skipped, as before.
var errReceiptUnavailable = errors.New("receipt unavailable")

// extractTxs reads the receipts of block's transactions from the endpoint named by
// pin ("" for unpinned: the router picks), so a reorg rescan reads them from its
// witness, the endpoint whose block it extracts.
func (e *ETHScanner) extractTxs(block *etypes.Block, pin string) (stypes.TxIn, error) {
	txInbound := stypes.TxIn{
		Chain:    common.ETHChain,
		Filtered: false,
		MemPool:  false,
	}

	if !e.receiptsMayProduceObservation(block) {
		e.removeSignedTxItems(block)
		return stypes.TxIn{}, nil
	}

	// Fetch the whole block's receipts with one eth_getBlockReceipts call
	// where the node supports it; transactions missing from the result (or
	// all of them, when the call fails) fall back to individual
	// eth_getTransactionReceipt lookups below.
	receiptsCtx, receiptsCancel := e.getContext()
	receipts := fetchBlockReceipts(endpoints.PinContext(receiptsCtx, pin), e.client.Client(), e.logger, block)
	receiptsCancel()

	// at least one worker: a semaphore smaller than one Acquire never grants it
	sem := semaphore.NewWeighted(max(e.cfg.Concurrency, 1))
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}
	// the first unavailable receipt fails the whole block: a transaction whose receipt
	// could not be read must not be dropped while the block is marked scanned (it
	// would never be revisited, since reorg detection only looks at the block hash).
	// Content errors do not fail the block: they are deterministic and would stall
	// the scanner forever.
	var firstErr error
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	processTx := func(tx *etypes.Transaction) {
		defer wg.Done()
		if err := sem.Acquire(context.Background(), 1); err != nil {
			e.logger.Err(err).Msg("fail to acquire semaphore")
			fail(err)
			return
		}
		defer sem.Release(1)

		if tx.To() == nil {
			return
		}

		// just try to remove the transaction hash from key value store
		// it doesn't matter whether the transaction is ours or not , success or failure
		// as long as the transaction id matches
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Msgf("fail to remove signed tx item, hash:%s", tx.Hash().String())
		}

		txInItem, err := e.fromTxToTxInWithReceipts(tx, receipts, block.Hash(), pin)
		if err != nil {
			e.logger.Error().Err(err).Str("hash", tx.Hash().Hex()).Msg("fail to get one tx from server")
			if errors.Is(err, errReceiptUnavailable) {
				fail(fmt.Errorf("tx %s: %w", tx.Hash().Hex(), err))
			}
			return
		}
		if txInItem == nil {
			return
		}
		// sometimes if a transaction failed due to gas problem , it will have no `to` address
		if len(txInItem.To) == 0 {
			return
		}
		if len([]byte(txInItem.Memo)) > constants.MaxMemoSize {
			return
		}
		txInItem.BlockHeight = block.Number().Int64()
		mu.Lock()
		txInbound.TxArray = append(txInbound.TxArray, txInItem)
		mu.Unlock()
		e.logger.Debug().Str("hash", tx.Hash().Hex()).Msgf("%s got %d tx", e.cfg.ChainID, 1)
	}

	// process txs in parallel
	for _, tx := range block.Transactions() {
		// skip blob transactions
		if tx.Type() == etypes.BlobTxType {
			continue
		}

		wg.Add(1)
		go processTx(tx)
	}
	wg.Wait()
	if firstErr != nil {
		return stypes.TxIn{}, fmt.Errorf("block %d: %w", block.NumberU64(), firstErr)
	}

	count := len(txInbound.TxArray)
	if count == 0 {
		e.logger.Info().Int64("block", int64(block.NumberU64())).Msg("no tx need to be processed in this block")
		return stypes.TxIn{}, nil
	}
	e.logger.Debug().Int64("block", int64(block.NumberU64())).Msgf("there are %d tx in this block need to process", count)
	return txInbound, nil
}

// canceledContext is for ethclient calls that must not reach the server.
var canceledContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// receiptsMayProduceObservation reports whether the block's receipts could lead to
// an observation. It is false only if the block alone rules that out, so the caller
// can skip fetching them. Gas prices don't depend on this: processBlock reads them
// from the tx fields.
//
// A router deposit can come through another contract, so check the bloom for log
// emitters regardless of tx.To. A direct ERC20 inbound names the vault in its
// transfer calldata. A failed outbound can have no logs and its signing vault
// may have left the vault list, so also check the local signer cache.
//
// Senders are taken as the RPC reported them where possible, see txSender. A provider
// that lied about a sender could hide that tx from this check, but it could equally
// leave the tx out of the block or misreport its receipt.
func (e *ETHScanner) receiptsMayProduceObservation(block *etypes.Block) bool {
	bloom := block.Bloom()
	// A block with txs and no bits set says more about whoever filled in the bloom
	// than about the block, and fetching is correct either way.
	if bloom == (etypes.Bloom{}) {
		return true
	}
	routers := e.pubkeyMgr.GetContracts(common.ETHChain, false)
	if len(routers) == 0 {
		return true
	}
	emitters := routers
	if useWhitelistSmartContract { // the log parser accepts these too, see isToValidContractAddress
		emitters = append(append([]common.Address{}, routers...), whitelistSmartContractAddress...)
	}
	for _, emitter := range emitters {
		if etypes.BloomLookup(bloom, ecommon.HexToAddress(emitter.String())) {
			return true
		}
	}

	for i, tx := range block.Transactions() {
		// extractTxs never parses these
		if tx.Type() == etypes.BlobTxType || tx.To() == nil {
			continue
		}
		if e.txMayBeObserved(tx, block.Hash(), uint(i)) {
			return true
		}
	}
	return evm.BlockMayHaveSignedTx(block, e.signerCacheManager)
}

// txMayBeObserved reports whether the transaction fields alone warrant fetching receipts.
func (e *ETHScanner) txMayBeObserved(tx *etypes.Transaction, blockHash ecommon.Hash, index uint) bool {
	if e.isPoolAddress(*tx.To()) {
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
// costs most of a block's time in receiptsMayProduceObservation.
func (e *ETHScanner) txSender(tx *etypes.Transaction, blockHash ecommon.Hash, index uint) (ecommon.Address, error) {
	// On a miss TransactionSender asks the server. The cancelled context turns that
	// into an error, so a block without reported senders cannot cost one call per tx.
	if sender, err := e.client.TransactionSender(canceledContext, tx, blockHash, index); err == nil {
		return sender, nil
	}
	return e.eipSigner.Sender(tx)
}

func (e *ETHScanner) isPoolAddress(addr ecommon.Address) bool {
	ok, _ := e.pubkeyMgr.IsValidPoolAddress(evm.LowerHexAddress(addr), e.cfg.ChainID)
	return ok
}

// removeSignedTxItems clears a skipped block's txs from the signed items, the same
// ones extractTxs would clear when it parses them.
func (e *ETHScanner) removeSignedTxItems(block *etypes.Block) {
	for _, tx := range block.Transactions() {
		if tx.Type() == etypes.BlobTxType || tx.To() == nil {
			continue
		}
		if err := e.blockMetaAccessor.RemoveSignedTxItem(tx.Hash().String()); err != nil {
			e.logger.Err(err).Msgf("fail to remove signed tx item, hash:%s", tx.Hash().String())
		}
	}
}

func (e *ETHScanner) onObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	e.metaMu.Lock()
	defer e.metaMu.Unlock()
	blockMeta, err := e.blockMetaAccessor.GetBlockMeta(blockHeight)
	if err != nil {
		e.logger.Err(err).Msgf("fail to get block meta on block height(%d)", blockHeight)
		return
	}

	if blockMeta == nil {
		e.logger.Error().Msgf("block meta for height:%d is nil", blockHeight)
		return
	}
	for _, item := range blockMeta.Transactions {
		if item.Hash == txIn.Tx {
			return
		}
	}

	blockMeta.Transactions = append(blockMeta.Transactions, types.TransactionMeta{
		Hash:        txIn.Tx,
		BlockHeight: blockHeight,
	})
	if err = e.blockMetaAccessor.SaveBlockMeta(blockHeight, blockMeta); err != nil {
		e.logger.Err(err).Msgf("fail to save block meta to storage,block height(%d)", blockHeight)
	}
}

// reorgPlan is the work a detected reorg requires: errata to report, repaired block
// metas to persist, and the observations of the rescanned blocks. It is computed
// without side effects and committed only once the whole scan step has succeeded.
type reorgPlan struct {
	errata []stypes.ErrataBlock
	metas  []*types.BlockMeta
	txIns  []stypes.TxIn
	// planned holds, per height, the transactions each stored meta had when the plan
	// read it; a transaction recorded after that (onObservedTxIn) is kept on commit
	planned map[int64]map[string]bool
}

// planReorg compares the block's parent hash with the stored hash of the previous
// height. On a reorg it re-confirms the transactions of every cached block, works out
// the repaired metas, and rescans the blocks whose hash changed, taking headers,
// re-confirmations and rescanned blocks from one witness endpoint
// (evm.ReorgWitness); the rescans' receipts must carry the rescanned block's hash
// (extractTxs). It reports and persists no reorg state (commitReorg does, once the
// current block is done too; the rescans' extraction cleanup is idempotent), and
// any read that cannot be answered aborts it, so the scan step is retried as a whole.
func (e *ETHScanner) planReorg(header *etypes.Header) (*reorgPlan, error) {
	return e.planReorgWith(header, nil, 0)
}

// planReorgWith is planReorg for the scanned block, whose transactions are scanned,
// and the chain height the scan was driven by, which the witness must have reached
// (evm.ReorgWitness): the witness's "not found" for a stored transaction the scanned
// block lists is not believed (evm.ScannedTxHashes).
func (e *ETHScanner) planReorgWith(header *etypes.Header, scanned etypes.Transactions, chainHeight int64) (_ *reorgPlan, err error) {
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
	if prevBlockMeta == nil {
		return nil, nil
	}
	// the block's previous hash need to be the same as the block hash chain client recorded in block meta
	// blockMetas[PreviousHeight].BlockHash == Block.PreviousHash
	if strings.EqualFold(prevBlockMeta.BlockHash, header.ParentHash.Hex()) {
		return nil, nil
	}
	e.logger.Info().Msgf("re-org detected, current block height:%d ,previous block hash is : %s , however block meta at height: %d, block hash is %s", header.Number.Int64(), header.ParentHash.Hex(), prevBlockMeta.Height, prevBlockMeta.BlockHash)
	detected = true

	blockMetas, err := e.blockMetaAccessor.GetBlockMetas()
	if err != nil {
		return nil, fmt.Errorf("fail to get block metas from local storage: %w", err)
	}
	planned := make(map[int64]map[string]bool, len(blockMetas))
	for _, blockMeta := range blockMetas {
		txs := make(map[string]bool, len(blockMeta.Transactions))
		for _, tx := range blockMeta.Transactions {
			txs[tx.Hash] = true
		}
		planned[blockMeta.Height] = txs
	}
	wctx, wcancel := e.getContext()
	witness, err := evm.ReorgWitness(wctx, e.endpointMgr, e.client, header, chainHeight)
	wcancel()
	if err != nil {
		return nil, fmt.Errorf("reorg at height %d: %w", header.Number.Int64(), err)
	}
	plan := &reorgPlan{planned: planned}
	listed := evm.ScannedTxHashes(scanned)
	var rescanHeights []int64
	for _, blockMeta := range blockMetas {
		// the witness's header first: it proves the witness has this height before
		// its "not found" for the block's transactions is believed
		h, herr := e.headerFrom(witness, blockMeta.Height)
		if herr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's header %d", blockMeta.Height))
			return nil, fmt.Errorf("block %d: fail to get header to check for reorg: %w", blockMeta.Height, herr)
		}
		metaTxs := make([]types.TransactionMeta, 0)
		var errataTxs []stypes.ErrataTx
		for _, tx := range blockMeta.Transactions {
			presence, rblock, cerr := e.checkTransaction(witness, tx.Hash)
			if cerr != nil {
				evm.SidelineWitness(e.endpointMgr, witness, "cannot re-confirm a transaction for the reorg plan")
				return nil, fmt.Errorf("block %d: cannot re-confirm tx %s: %w", blockMeta.Height, tx.Hash, cerr)
			}
			if presence == evm.TxFailed {
				fctx, fcancel := e.getContext()
				ferr := evm.FailedReceiptOnScannedFork(fctx, e.endpointMgr, e.client, witness, header, listed[evm.TxMetaKey(tx.Hash)], rblock)
				fcancel()
				if ferr != nil {
					return nil, fmt.Errorf("block %d: tx %s: %w", blockMeta.Height, tx.Hash, ferr)
				}
			}
			if presence == evm.TxOnChain {
				e.logger.Debug().Msgf("block height: %d, tx: %s still exist", blockMeta.Height, tx.Hash)
				metaTxs = append(metaTxs, tx)
				continue
			}
			if presence == evm.TxNotFound && listed[evm.TxMetaKey(tx.Hash)] {
				return nil, fmt.Errorf("block %d: endpoint %q does not know tx %s, which the scanned block lists: its answer does not match the scanned block it holds", blockMeta.Height, witness, tx.Hash)
			}
			// the node says the tx is not on chain (any more): errata it
			errataTxs = append(errataTxs, stypes.ErrataTx{
				TxID:  common.TxID(tx.Hash),
				Chain: common.ETHChain,
			})
		}
		if len(errataTxs) > 0 {
			plan.errata = append(plan.errata, stypes.ErrataBlock{Height: blockMeta.Height, Txs: errataTxs})
		}
		if !strings.EqualFold(blockMeta.BlockHash, h.Hash().Hex()) {
			// if the block hash is different as previously recorded , then the block should be rescanned
			rescanHeights = append(rescanHeights, blockMeta.Height)
		}
		blockMeta.PreviousHash = h.ParentHash.Hex()
		blockMeta.BlockHash = h.Hash().Hex()
		blockMeta.Transactions = metaTxs
		plan.metas = append(plan.metas, blockMeta)
	}

	for _, height := range rescanHeights {
		e.logger.Info().Msgf("rescan block height: %d", height)
		block, berr := e.blockFrom(witness, height)
		if berr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's block %d", height))
			return nil, fmt.Errorf("rescan block %d: %w", height, berr)
		}
		if block.Transactions().Len() == 0 {
			continue
		}
		txIn, xerr := e.extractTxs(block, witness)
		if xerr != nil {
			evm.SidelineWitness(e.endpointMgr, witness, fmt.Sprintf("cannot serve the reorg plan's receipts of block %d", height))
			return nil, fmt.Errorf("rescan block %d: %w", height, xerr)
		}
		if len(txIn.TxArray) > 0 {
			plan.txIns = append(plan.txIns, txIn)
		}
	}
	// every answer above came from the witness: it must still hold the scanned block,
	// or they may describe another fork (a failed receipt from there is no errata)
	cctx, ccancel := e.getContext()
	err = evm.WitnessStillHolds(cctx, e.endpointMgr, e.client, witness, header)
	ccancel()
	if err != nil {
		return nil, fmt.Errorf("reorg at height %d: %w", header.Number.Int64(), err)
	}
	return plan, nil
}

// commitReorg reports the errata and persists the repaired block metas of a plan.
// It runs only after every fallible step of the scan step has succeeded.
func (e *ETHScanner) commitReorg(plan *reorgPlan) {
	if plan == nil {
		return
	}
	for _, errata := range plan.errata {
		e.globalErrataQueue <- errata
	}
	// errata are queued before the lock is taken: the observer may hold its own lock
	// while waiting on metaMu, and must never wait on a blocked errata send
	e.metaMu.Lock()
	defer e.metaMu.Unlock()
	for _, blockMeta := range plan.metas {
		// keep a transaction the observer recorded at this height after the plan read
		// it (an errata'd one was there when the plan read it, so it is not re-added)
		if current, err := e.blockMetaAccessor.GetBlockMeta(blockMeta.Height); err == nil && current != nil {
			have := make(map[string]bool, len(blockMeta.Transactions))
			for _, tx := range blockMeta.Transactions {
				have[tx.Hash] = true
			}
			for _, tx := range current.Transactions {
				if !plan.planned[blockMeta.Height][tx.Hash] && !have[tx.Hash] {
					blockMeta.Transactions = append(blockMeta.Transactions, tx)
				}
			}
		}
		if err := e.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta); err != nil {
			e.logger.Err(err).Msgf("fail to save block meta of height: %d ", blockMeta.Height)
		}
	}
}

// headerFrom reads a header from the endpoint named by pin ("" for unpinned).
func (e *ETHScanner) headerFrom(pin string, height int64) (*etypes.Header, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.HeaderByNumber(endpoints.PinContext(ctx, pin), big.NewInt(height))
}

// blockFrom reads a block a reorg plan rescans (below the scanned height) from the
// endpoint named by pin ("" for unpinned). A block the witness does not serve is an
// ordinary error, never ErrUnavailableBlock as in getRPCBlock: that one asks the scan
// loop to retry the scanned height quietly and never skip it.
func (e *ETHScanner) blockFrom(pin string, height int64) (*etypes.Block, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	block, err := e.client.BlockByNumber(endpoints.PinContext(ctx, pin), big.NewInt(height))
	if err != nil {
		return nil, fmt.Errorf("fail to fetch block: %w", err)
	}
	return block, nil
}

// checkTransaction reports what the endpoint named by pin ("" for unpinned) knows
// about an observed transaction after a reorg. Only its "not found" answer, or a
// failed receipt, says it is not on chain; a lookup that fails is returned as an
// error, never turned into an errata. A failed receipt comes with its block, which
// the plan checks is on the scanned fork (evm.FailedReceiptOnScannedFork).
func (e *ETHScanner) checkTransaction(pin, hash string) (evm.TxPresence, evm.ReceiptBlock, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	tx, pending, err := e.client.TransactionByHash(endpoints.PinContext(ctx, pin), ecommon.HexToHash(hash))
	if errors.Is(err, ethereum.NotFound) || (err == nil && tx == nil) {
		return evm.TxNotFound, evm.ReceiptBlock{}, nil
	}
	if err != nil {
		return evm.TxOnChain, evm.ReceiptBlock{}, err
	}

	// pending transactions may fail, but we should only errata when there is certainty
	if pending {
		e.logger.Warn().Msgf("tx: %s is in pending status", hash)
		return evm.TxOnChain, evm.ReceiptBlock{}, nil // unknown, prefer false positive
	}

	// ensure the tx was successful
	rctx, rcancel := e.getContext()
	defer rcancel()
	receipt, err := e.client.TransactionReceipt(endpoints.PinContext(rctx, pin), ecommon.HexToHash(hash))
	if err != nil {
		e.logger.Warn().Err(err).Msgf("fail to get receipt for tx: %s", hash)
		return evm.TxOnChain, evm.ReceiptBlock{}, nil // unknown, prefer false positive
	}
	if receipt.Status != etypes.ReceiptStatusSuccessful {
		return evm.TxFailed, evm.ReceiptBlock{Number: receipt.BlockNumber, Hash: receipt.BlockHash}, nil
	}
	return evm.TxOnChain, evm.ReceiptBlock{}, nil
}

// getReceipt reads a receipt from the endpoint named by pin ("" for unpinned).
func (e *ETHScanner) getReceipt(pin, hash string) (*etypes.Receipt, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.TransactionReceipt(endpoints.PinContext(ctx, pin), ecommon.HexToHash(hash))
}

func (e *ETHScanner) getBlock(height int64) (*etypes.Block, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.BlockByNumber(ctx, big.NewInt(height))
}

func (e *ETHScanner) getRPCBlock(height int64) (*etypes.Block, error) {
	block, err := e.getBlock(height)
	if err == ethereum.NotFound {
		return nil, btypes.ErrUnavailableBlock
	}
	if err != nil {
		return nil, fmt.Errorf("fail to fetch block: %w", err)
	}
	return block, nil
}

func (e *ETHScanner) getDecimals(token string) (uint64, error) {
	if IsETH(token) {
		return defaultDecimals, nil
	}
	to := ecommon.HexToAddress(token)
	input, err := e.erc20ABI.Pack(decimalMethod)
	if err != nil {
		return defaultDecimals, fmt.Errorf("fail to pack decimal method: %w", err)
	}
	ctx, cancel := e.getContext()
	defer cancel()
	res, err := e.client.CallContract(ctx, ethereum.CallMsg{
		To:   &to,
		Data: input,
	}, nil)
	if err != nil {
		return defaultDecimals, fmt.Errorf("fail to call smart contract get decimals: %w", err)
	}
	output, err := e.erc20ABI.Unpack(decimalMethod, res)
	if err != nil {
		return defaultDecimals, fmt.Errorf("fail to unpack decimal method call result: %w", err)
	}
	switch output[0].(type) {
	case uint8:
		decimals, ok := abi.ConvertType(output[0], new(uint8)).(*uint8)
		if !ok {
			return defaultDecimals, fmt.Errorf("dev error: fail to cast uint8")
		}
		return uint64(*decimals), nil
	case *big.Int:
		decimals, ok := abi.ConvertType(output[0], new(*big.Int)).(*big.Int)
		if !ok {
			return defaultDecimals, fmt.Errorf("dev error: fail to cast big.Int")
		}
		return decimals.Uint64(), nil
	}
	return defaultDecimals, fmt.Errorf("%s is %T fail to parse it", output[0], output[0])
}

// . and - had been reserved to use in THORChain symbol
// and + similarly is not accepted by common package NewSymbol's isAlphaNumeric
var symbolReplacer = strings.NewReplacer(".", "", "-", "", `\u0000`, "", "\u0000", "", "+", "")

func sanitiseSymbol(symbol string) string {
	return symbolReplacer.Replace(symbol)
}

func (e *ETHScanner) getSymbol(token string) (string, error) {
	if IsETH(token) {
		return "ETH", nil
	}
	to := ecommon.HexToAddress(token)
	input, err := e.erc20ABI.Pack(symbolMethod)
	if err != nil {
		return "", nil
	}
	ctx, cancel := e.getContext()
	defer cancel()
	res, err := e.client.CallContract(ctx, ethereum.CallMsg{
		To:   &to,
		Data: input,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("fail to call to smart contract and get symbol: %w", err)
	}
	var symbol string
	output, err := e.erc20ABI.Unpack(symbolMethod, res)
	if err != nil {
		symbol = string(res)
		e.logger.Err(err).Msgf("fail to unpack symbol method call,token address: %s , symbol: %s", token, symbol)
		return sanitiseSymbol(symbol), nil
	}
	// nolint
	symbol = *abi.ConvertType(output[0], new(string)).(*string)
	return sanitiseSymbol(symbol), nil
}

// isToValidContractAddress this method make sure the transaction to address is to THORChain router or a whitelist address
func (e *ETHScanner) isToValidContractAddress(addr *ecommon.Address, includeWhiteList bool) bool {
	// get the smart contract used by thornode
	contractAddresses := e.pubkeyMgr.GetContracts(common.ETHChain, false)
	includeWhiteList = includeWhiteList || useWhitelistSmartContract

	// addrString is EqualFold-equal to the checksummed addr.String(), without
	// a keccak256 per comparison. Like addr.String() before, it is derived
	// only once there is something to compare against.
	var addrString string
	for _, item := range contractAddresses {
		if addrString == "" {
			addrString = evm.LowerHexAddress(*addr)
		}
		if strings.EqualFold(item.String(), addrString) {
			return true
		}
	}
	// combine the whitelist smart contract address
	if includeWhiteList {
		for _, item := range whitelistSmartContractAddress {
			if addrString == "" {
				addrString = evm.LowerHexAddress(*addr)
			}
			if strings.EqualFold(item.String(), addrString) {
				return true
			}
		}
	}
	return false
}

func (e *ETHScanner) getTokenMeta(token string) (types.TokenMeta, error) {
	tokenMeta, err := e.tokens.GetTokenMeta(token)
	if err != nil {
		return types.TokenMeta{}, fmt.Errorf("fail to get token meta: %w", err)
	}
	if tokenMeta.IsEmpty() {
		isWhiteListToken := false
		for _, item := range e.whitelistTokens {
			if strings.EqualFold(item.Address, token) {
				isWhiteListToken = true
				break
			}
		}

		if !isWhiteListToken {
			return types.TokenMeta{}, fmt.Errorf("token: %s is not whitelisted", token)
		}
		var symbol string
		symbol, err = e.getSymbol(token)
		if err != nil {
			return types.TokenMeta{}, fmt.Errorf("fail to get symbol: %w", err)
		}
		var decimals uint64
		decimals, err = e.getDecimals(token)
		if err != nil {
			e.logger.Err(err).Msgf("fail to get decimals from smart contract, default to: %d", defaultDecimals)
		}
		e.logger.Info().Msgf("token:%s, decimals: %d", token, decimals)
		tokenMeta = types.NewTokenMeta(symbol, token, decimals)
		if err = e.tokens.SaveTokenMeta(symbol, token, decimals); err != nil {
			return types.TokenMeta{}, fmt.Errorf("fail to save token meta: %w", err)
		}
	}
	return tokenMeta, nil
}

func (e *ETHScanner) isRelevantERC20Token(addr *ecommon.Address) bool {
	if addr == nil {
		return false
	}
	return evm.IsRelevantToken(addr.String(), e.whitelistTokens, e.tokens, e.logger)
}

func (e *ETHScanner) directERC20InboundEnabled() bool {
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

// ConvertAmount will convert the amount to 1e8, the decimals used by THORChain.
func (e *ETHScanner) ConvertAmount(token string, amt *big.Int) cosmos.Uint {
	if IsETH(token) {
		return cosmos.NewUintFromBigInt(amt).QuoUint64(common.One * 100)
	}
	decimals := uint64(defaultDecimals)
	tokenMeta, err := e.getTokenMeta(token)
	if err != nil {
		e.logger.Err(err).Msgf("fail to get token meta for token address: %s", token)
	}
	if !tokenMeta.IsEmpty() {
		decimals = tokenMeta.Decimal
	}
	if decimals != defaultDecimals {
		var value big.Int
		amt = amt.Mul(amt, value.Exp(big.NewInt(10), big.NewInt(defaultDecimals), nil))
		amt = amt.Div(amt, value.Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	}
	return cosmos.NewUintFromBigInt(amt).QuoUint64(common.One * 100)
}

// GetTokenDecimalsForTHORChain returns the coin decimal override for THORChain.
// A return value of 0 means use common.THORChainDecimals, 1e8 precision.
func (e *ETHScanner) GetTokenDecimalsForTHORChain(token string) int64 {
	if IsETH(token) {
		return 0
	}
	tokenMeta, err := e.getTokenMeta(token)
	if err != nil {
		e.logger.Err(err).Msgf("fail to get token meta for token address: %s", token)
	}
	if tokenMeta.IsEmpty() {
		return 0
	}
	// when the token's precision is more than THORChain , that's fine , just use THORChainDecimals
	if tokenMeta.Decimal >= common.THORChainDecimals {
		return 0
	}
	return int64(tokenMeta.Decimal)
}

// GetAssetFromTokenAddress resolves an ETH chain asset from an ERC20 token address.
func (e *ETHScanner) GetAssetFromTokenAddress(token string) (common.Asset, error) {
	if IsETH(token) {
		return common.ETHAsset, nil
	}
	tokenMeta, err := e.getTokenMeta(token)
	if err != nil {
		return common.EmptyAsset, fmt.Errorf("fail to get token meta: %w", err)
	}
	if tokenMeta.IsEmpty() {
		return common.EmptyAsset, fmt.Errorf("token metadata is empty")
	}
	return common.NewAsset(fmt.Sprintf("ETH.%s-%s", tokenMeta.Symbol, strings.ToUpper(tokenMeta.Address)))
}

// getTxInFromDirectERC20Transfer observes direct ERC20 token transfers sent to
// active vault addresses. These txs bypass the router, so they are emitted as
// memo-less inbounds and left to THORNode's normal memoless/refund handling.
func (e *ETHScanner) getTxInFromDirectERC20Transfer(tx *etypes.Transaction, receipt *etypes.Receipt, sender *ecommon.Address) (*stypes.TxInItem, error) {
	return evm.GetTxInFromDirectERC20Transfer(tx, receipt, evm.DirectERC20TransferTxInOptions{
		Chain:                 common.ETHChain,
		Signer:                e.eipSigner,
		Sender:                sender,
		MaxContractTxLogs:     int64(e.cfg.MaxContractTxLogs),
		PoolAddressProvider:   e.pubkeyMgr,
		TokenResolver:         e,
		Logger:                e.logger,
		SenderErrorMessage:    "fail to get sender",
		ParseLogsErrorMessage: "fail to parse direct ERC20 transfer logs",
	})
}

// getTxInFromSmartContract returns txInItem
func (e *ETHScanner) getTxInFromSmartContract(tx *etypes.Transaction, receipt *etypes.Receipt, maxLogs int64) (*stypes.TxInItem, error) {
	e.logger.Debug().Msg("parse tx from smart contract")
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:],
	}
	sender, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		return nil, fmt.Errorf("fail to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	// 1 is Transaction success state
	if receipt.Status != 1 {
		e.logger.Info().Msgf("tx(%s) state: %d means failed , ignore", tx.Hash().String(), receipt.Status)
		return nil, nil
	}
	p := evm.NewSmartContractLogParser(e.isToValidContractAddress,
		e.GetAssetFromTokenAddress,
		e.GetTokenDecimalsForTHORChain,
		e.ConvertAmount,
		e.vaultABI,
		common.ETHAsset,
		maxLogs)
	// txInItem will be changed in p.GetTxInItem function, so if the function return an error
	// txInItem should be abandoned
	if _, err = p.GetTxInItem(receipt.Logs, txInItem); err != nil {
		return nil, fmt.Errorf("fail to parse logs, err: %w", err)
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
		senderIsVault, _ := e.pubkeyMgr.IsValidPoolAddress(txInItem.Sender, common.ETHChain)
		if !senderIsVault {
			ok, cpi := e.pubkeyMgr.IsValidPoolAddress(txInItem.To, common.ETHChain)
			if ok {
				expectedRouter := e.pubkeyMgr.GetContract(common.ETHChain, cpi.PubKey)
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

	// under no circumstance ETH gas price will be less than 1 Gwei , unless it is in dev environment
	txGasPrice := receipt.EffectiveGasPrice

	e.logger.Debug().Msgf("tx: %s, gas price: %s, gas used: %d,receipt status:%d", txInItem.Tx, txGasPrice.String(), receipt.GasUsed, receipt.Status)

	txInItem.Gas = common.MakeEVMGas(common.ETHChain, txGasPrice, receipt.GasUsed, nil)
	if txInItem.Coins.IsEmpty() {
		e.logger.Debug().Msgf("there is no coin in this tx, ignore, %+v", txInItem)
		return nil, nil
	}
	e.logger.Debug().Msgf("tx in item: %+v", txInItem)
	return txInItem, nil
}

func (e *ETHScanner) getTxInFromTransaction(tx *etypes.Transaction, receipt *etypes.Receipt) (*stypes.TxInItem, error) {
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:],
	}
	asset := common.ETHAsset
	sender, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		return nil, fmt.Errorf("fail to get sender: %w", err)
	}
	txInItem.Sender = strings.ToLower(sender.String())
	txInItem.To = strings.ToLower(tx.To().String())
	// this is native , thus memo is data field
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
	ethValue := e.ConvertAmount(ethToken, tx.Value())
	txInItem.Coins = append(txInItem.Coins, common.NewCoin(asset, ethValue))
	txGasPrice := receipt.EffectiveGasPrice

	txInItem.Gas = common.MakeEVMGas(common.ETHChain, txGasPrice, receipt.GasUsed, nil)

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
			observableAmount := common.ETHChain.DustThreshold()
			txInItem.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, observableAmount))

			// remove the outbound from signer cache so it can be re-attempted
			if e.signerCacheManager != nil {
				e.signerCacheManager.RemoveSigned(tx.Hash().Hex())
			}
		} else {
			e.logger.Debug().Msgf("there is no coin in this tx, ignore, %+v", txInItem)
			return nil, nil
		}
	}
	return txInItem, nil
}

func (e *ETHScanner) fromTxToTxIn(tx *etypes.Transaction) (*stypes.TxInItem, error) {
	return e.fromTxToTxInWithReceipts(tx, nil, ecommon.Hash{}, "")
}

// fromTxToTxInWithReceipts converts the transaction to a TxInItem, taking its
// receipt from the supplied block-receipts map when present and falling back
// to an individual eth_getTransactionReceipt lookup otherwise. A fallback receipt
// must belong to blockHash, the block being scanned (the zero hash skips the check
// for callers without a block).
func (e *ETHScanner) fromTxToTxInWithReceipts(tx *etypes.Transaction, receipts map[ecommon.Hash]*etypes.Receipt, blockHash ecommon.Hash, pin string) (*stypes.TxInItem, error) {
	if tx == nil || tx.To() == nil {
		return nil, nil
	}
	receipt := receipts[tx.Hash()]
	if receipt == nil {
		var err error
		receipt, err = e.getReceipt(pin, tx.Hash().Hex())
		if err != nil {
			if errors.Is(err, ethereum.NotFound) {
				// the block lists this transaction, so a node that has the block has
				// its receipt: "not found" means the answering node has not imported
				// the block yet (a lagging endpoint after a failover) or dropped it
				// in a reorg. Either way the block must be retried, never scanned
				// with the transaction silently missing.
				return nil, fmt.Errorf("%w: receipt for tx %s in block is not available on this node yet: %w", errReceiptUnavailable, tx.Hash().Hex(), err)
			}
			return nil, fmt.Errorf("%w: fail to get transaction receipt: %w", errReceiptUnavailable, err)
		}
		// a receipt of another block (the answering node is on another fork, or
		// reorged since the block was read) describes another execution of this
		// transaction: retry the block instead of observing it from the wrong fork
		if blockHash != (ecommon.Hash{}) && receipt.BlockHash != blockHash {
			return nil, fmt.Errorf("%w: receipt for tx %s belongs to block %s, not the scanned block %s",
				errReceiptUnavailable, tx.Hash().Hex(), receipt.BlockHash.Hex(), blockHash.Hex())
		}
	}
	if receipt.Status != 1 {
		// a transaction that is failed
		// remove the Signer cache , so the tx out item can be retried
		if e.signerCacheManager != nil {
			e.signerCacheManager.RemoveSigned(tx.Hash().String())
		}
		e.logger.Debug().Msgf("tx(%s) state: %d means failed , ignore", tx.Hash().String(), receipt.Status)
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

// getTxInFromFailedTransaction when a transaction failed due to out of gas, this method
// will check whether the transaction is an outbound. It fakes a txInItem if the failed
// transaction is an outbound, and reports it back to THORNode so the gas fee can be
// deducted from the vault balance to keep accounting in sync.
func (e *ETHScanner) getTxInFromFailedTransaction(tx *etypes.Transaction, receipt *etypes.Receipt) *stypes.TxInItem {
	if receipt.Status == 1 {
		e.logger.Info().Str("hash", tx.Hash().String()).Msg("success transaction should not get into getTxInFromFailedTransaction")
		return nil
	}
	fromAddr, err := etypes.Sender(e.eipSigner, tx)
	if err != nil {
		e.logger.Err(err).Msg("fail to get from address")
		return nil
	}
	ok, cif := e.pubkeyMgr.IsValidPoolAddress(fromAddr.String(), common.ETHChain)
	if !ok || cif.IsEmpty() {
		return nil
	}
	txGasPrice := receipt.EffectiveGasPrice
	txHash := tx.Hash().Hex()[2:]
	return &stypes.TxInItem{
		Tx:     txHash,
		Memo:   memo.NewOutboundMemo(common.TxID(strings.ToUpper(txHash))).String(),
		Sender: strings.ToLower(fromAddr.String()),
		To:     strings.ToLower(tx.To().String()),
		Coins:  common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		Gas:    common.MakeEVMGas(common.ETHChain, txGasPrice, receipt.GasUsed, nil),
	}
}
