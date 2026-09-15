package substrate

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/generic"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	ttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

const (
	FeeCacheSize    = 200
	FeeUpdatePeriod = 20
	NonceHashBlocks = 100

	// Scan recovery records every 200 blocks.
	recordSweepPeriod = 200

	// Retain recovery records for eight eras after their recheck height.
	recordSweepEras = 8

	// Allow reference storage for block intervals down to 100 milliseconds.
	minBlockInterval = 100 * time.Millisecond

	// Keep signing anchors longer than the nominal DOT and TAO mortal eras.
	blockReferenceRetention = 15 * time.Minute
)

var (
	errFinalizedFailureCleanup = errors.New("finalized failed extrinsic cleanup failed")
	errUnsupportedObservation  = errors.New("unsupported observation shape")
)

// FinalizedFailureHandler clears client-owned state after the scanner proves an
// extrinsic from one of our vaults failed in a finalized block.
type FinalizedFailureHandler func(txHash string) error

type runtimeContext struct {
	metadata    *stypes.Metadata
	layout      stypes.ExtrinsicLayout
	specVersion uint32
	txVersion   uint32
}

// Scanner is a generic block scanner for Substrate chains.
type Scanner struct {
	*generic.Scanner

	cfg              config.BifrostBlockScannerConfiguration
	logger           zerolog.Logger
	metrics          *metrics.Metrics
	rpcClient        *rpc.Client
	solvencyReporter types.SolvencyReporter
	pubkeyMgr        pubkeymanager.PubKeyValidator
	failHandler      FinalizedFailureHandler
	stateStore       *stateStore

	globalNetworkFeeQueue chan common.NetworkFee

	// Set once at construction, immutable thereafter.
	genesisHash string
	params      Params

	nonceCache *nonceCache

	// Mutable scanner state (protected by mu).
	mu                sync.RWMutex
	runtime           runtimeContext
	latestBlockHash   string
	latestBlockHeight int64
	feeCache          []uint64
	currentFee        atomic.Uint64
}

// NewScanner creates a new generic Substrate block scanner.
func NewScanner(
	cfg config.BifrostBlockScannerConfiguration,
	solvencyReporter types.SolvencyReporter,
	rpcClient *rpc.Client,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	failHandler FinalizedFailureHandler,
	state generic.StateStore,
	m *metrics.Metrics,
) (*Scanner, error) {
	params, found := ChainParams[cfg.ChainID]
	if !found {
		return nil, fmt.Errorf("no params found for %s", cfg.ChainID)
	}
	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("invalid params for %s: %w", cfg.ChainID, err)
	}

	retention := blockReferenceRetention
	references := int(params.MortalEraPeriod) * 2
	if byRetention := int(retention / minBlockInterval); byRetention > references {
		references = byRetention
	}

	genericScanner, err := generic.NewScanner(state, references, retention)
	if err != nil {
		return nil, fmt.Errorf("failed to create generic scanner: %w", err)
	}

	store, err := newStateStore(state)
	if err != nil {
		return nil, fmt.Errorf("failed to create signed extrinsic record store: %w", err)
	}

	s := &Scanner{
		Scanner:          genericScanner,
		cfg:              cfg,
		metrics:          m,
		logger:           log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		params:           params,
		rpcClient:        rpcClient,
		solvencyReporter: solvencyReporter,
		pubkeyMgr:        pubkeyMgr,
		failHandler:      failHandler,
		stateStore:       store,
		nonceCache:       newNonceCache(),
	}

	rv, err := rpcClient.GetRuntimeVersion("")
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime version: %w", err)
	}

	// Validate we're connected to the correct node, not another Substrate chain.
	// Pallet indices are hardcoded per chain runtime; a different runtime would
	// have different pallet ordering, silently misattributing events and
	// potentially crediting bogus transfers.
	if rv.SpecName != s.params.SpecName {
		return nil, fmt.Errorf(
			"runtime specName mismatch (expected %q, got %q)",
			s.params.SpecName, rv.SpecName,
		)
	}

	s.runtime, err = s.loadRuntimeContext("", rv)
	if err != nil {
		return nil, fmt.Errorf("failed to verify metadata: %w", err)
	}

	s.genesisHash, err = rpcClient.GetGenesisHash()
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis hash: %w", err)
	}

	s.logger.Info().
		Str("spec_name", rv.SpecName).
		Uint32("spec_version", rv.SpecVersion).
		Uint32("tx_version", rv.TransactionVersion).
		Str("genesis_hash", s.genesisHash).
		Msg("scanner initialized")

	return s, nil
}

// GetHeight returns the current finalized block height.
func (s *Scanner) GetHeight() (int64, error) {
	hash, err := s.rpcClient.GetFinalizedHead()
	if err != nil {
		return -1, fmt.Errorf("failed to get finalized head: %w", err)
	}

	header, err := s.rpcClient.GetHeader(hash)
	if err != nil {
		return -1, fmt.Errorf("failed to get header: %w", err)
	}

	blockNum, err := header.BlockNumber()
	if err != nil {
		return -1, fmt.Errorf("failed to parse block number: %w", err)
	}

	return int64(blockNum), nil
}

// FetchTxs is called by the standard block scanner for each block.
func (s *Scanner) FetchTxs(fetchHeight, _ int64) (ttypes.TxIn, error) {
	txIn := ttypes.TxIn{
		Chain: s.cfg.ChainID,
	}

	if fetchHeight <= 0 {
		return txIn, fmt.Errorf("invalid fetch height: %d", fetchHeight)
	}

	height := uint64(fetchHeight)
	blockHash, err := s.rpcClient.GetBlockHash(&height)
	if err != nil {
		return txIn, fmt.Errorf("failed to get block hash for height %d: %w", fetchHeight, err)
	}

	// Check and refresh runtime version before parsing the block
	runtime, err := s.checkRuntimeVersion(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("failed to refresh runtime version: %w", err)
	}

	block, err := s.rpcClient.GetBlock(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("failed to get block at height %d: %w", fetchHeight, err)
	}
	blockTimestamp, err := s.rpcClient.GetBlockTimestamp(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("failed to get timestamp at height %d: %w", fetchHeight, err)
	}

	txIn.TxArray = make([]*ttypes.TxInItem, 0, len(block.Extrinsics))

	// Fetch events and determine which extrinsics succeeded
	successMap, feeMap, err := s.getExtrinsicResults(blockHash, len(block.Extrinsics), runtime.metadata)
	if err != nil {
		return txIn, fmt.Errorf("failed to get extrinsic results at height %d: %w", fetchHeight, err)
	}

	// Process extrinsics in the block
	for i, extHex := range block.Extrinsics {
		// Only process extrinsics confirmed as successful
		succeeded, known := successMap[uint32(i)]
		if !known {
			// Could not determine success status — skip (safe direction)
			continue
		}

		fee := feeMap[uint32(i)]

		var txItem *ttypes.TxInItem

		if succeeded {
			txItem, err = s.processExtrinsicWithRuntime(extHex, fetchHeight, fee.Total, runtime)
		} else {
			txItem, err = s.processFailedExtrinsicWithRuntime(extHex, fetchHeight, fee.Total, runtime)
		}

		if err != nil {
			if errors.Is(err, errUnsupportedObservation) {
				event := s.logger.Warn().Err(err).Int64("height", fetchHeight).Int("index", i)
				if raw, decodeErr := stypes.HexDecode(extHex); decodeErr == nil {
					event.Str("txid", stypes.ExtrinsicHash(raw))
				}
				event.Msg("skip unsupported extrinsic")

				if s.metrics != nil {
					if counter := s.metrics.GetCounterVec(metrics.BlockScanError(s.cfg.ChainID)); counter != nil {
						counter.WithLabelValues("unsupported_observation", "").Inc()
					}
				}
				continue
			}
			return txIn, fmt.Errorf("failed to process extrinsic %d: %w", i, err)
		}

		if txItem == nil {
			continue
		}

		txIn.TxArray = append(txIn.TxArray, txItem)

		// Estimate fees without optional tips.
		if fee.Total > fee.Tip {
			s.updateFeeCache(fee.Total - fee.Tip)
		}
	}

	// Report network fee periodically
	s.updateNetworkFee(fetchHeight)

	// Report solvency
	if s.solvencyReporter != nil {
		err = s.solvencyReporter(fetchHeight)
		if err != nil {
			s.logger.Err(err).Msg("failed to report solvency")
		}
	}

	s.nonceCache.Purge(fetchHeight - NonceHashBlocks)
	s.sweepExpiredRecords(fetchHeight)

	err = s.SetBlockReference(generic.BlockReference{
		Height:    fetchHeight,
		Hash:      blockHash,
		Timestamp: blockTimestamp,
	})
	if err != nil {
		return txIn, fmt.Errorf("failed to index block reference at height %d: %w", fetchHeight, err)
	}
	// Track the highest fully processed block. Prefetch workers can complete out
	// of order, so setLatestBlock never moves this value backwards.
	s.setLatestBlock(blockHash, fetchHeight)

	return txIn, nil
}

// sweepExpiredRecords periodically removes old recovery records.
func (s *Scanner) sweepExpiredRecords(fetchHeight int64) {
	if fetchHeight%recordSweepPeriod != 0 {
		return
	}

	cutoff := fetchHeight - int64(s.params.MortalEraPeriod)*recordSweepEras
	if cutoff <= 0 {
		return
	}

	dropped, err := s.stateStore.SweepExpired(cutoff)
	if err != nil {
		s.logger.Err(err).
			Int64("cutoff", cutoff).
			Msg("failed to sweep expired signed extrinsic records")
		return
	}
	if dropped > 0 {
		s.logger.Info().
			Int("records", dropped).
			Int64("cutoff", cutoff).
			Msg("dropped expired signed extrinsic records")
	}
}

// FetchMemPool is not used for Substrate chains.
func (s *Scanner) FetchMemPool(_ int64) (ttypes.TxIn, error) {
	return ttypes.TxIn{}, nil
}

// GetNetworkFee returns size 1 for one extrinsic and its estimated fee in native units.
func (s *Scanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	fee := s.currentFee.Load()
	if fee == 0 {
		fee = s.params.DefaultFee
	}
	return 1, fee
}

// getExtrinsicResults queries System::Events and determines which extrinsics succeeded.
// numExtrinsics is the number of extrinsics in the block, used to validate decoded results.
func (s *Scanner) getExtrinsicResults(blockHash string, numExtrinsics int, metadata *stypes.Metadata) (map[uint32]bool, map[uint32]stypes.Fee, error) {
	eventsHex, err := s.rpcClient.GetEventsStorage(blockHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get events storage: %w", err)
	}

	eventsData, err := stypes.HexDecode(eventsHex)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode events hex: %w", err)
	}

	palletConfig := stypes.EventPalletConfig{
		Metadata:                      metadata,
		SystemPalletIndex:             s.params.Index(SystemPallet),
		TransactionPaymentPalletIndex: s.params.Index(TransactionPaymentPallet),
	}

	results, fees, err := stypes.ParseExtrinsicResults(eventsData, palletConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse runtime events: %w", err)
	}

	for idx := range results {
		if int(idx) >= numExtrinsics {
			return nil, nil, fmt.Errorf("event references out-of-range extrinsic %d", idx)
		}
	}
	for i := 0; i < numExtrinsics; i++ {
		_, ok := results[uint32(i)]
		if !ok {
			return nil, nil, fmt.Errorf("missing result event for extrinsic %d", i)
		}
	}

	return results, fees, nil
}

// checkRuntimeVersion re-fetches the runtime version from the node and
// verifies index validity on a version change
func (s *Scanner) checkRuntimeVersion(blockHash string) (runtimeContext, error) {
	rv, err := s.rpcClient.GetRuntimeVersion(blockHash)
	if err != nil {
		return runtimeContext{}, fmt.Errorf("failed to refresh runtime version: %w", err)
	}

	current := s.getRuntimeContext()
	if rv.SpecVersion != current.specVersion || rv.TransactionVersion != current.txVersion {
		s.logger.Info().
			Uint32("old_spec", current.specVersion).Uint32("new_spec", rv.SpecVersion).
			Uint32("old_tx", current.txVersion).Uint32("new_tx", rv.TransactionVersion).
			Msg("runtime version updated")

		var next runtimeContext
		next, err = s.loadRuntimeContext(blockHash, rv)
		if err != nil {
			return runtimeContext{}, fmt.Errorf("failed to verify metadata: %w", err)
		}

		s.mu.Lock()
		s.runtime = next
		s.mu.Unlock()
		current = next
	}

	return current, nil
}

// updateFeeCache adds a fee to the rolling window, truncating if over feeCacheSize.
func (s *Scanner) updateFeeCache(fee uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feeCache = append(s.feeCache, fee)
	if len(s.feeCache) > FeeCacheSize {
		s.feeCache = s.feeCache[len(s.feeCache)-FeeCacheSize:]
	}
}

func (s *Scanner) processFailedExtrinsic(extHex string, blockHeight int64, fee uint64) (*ttypes.TxInItem, error) {
	return s.processFailedExtrinsicWithRuntime(extHex, blockHeight, fee, s.getRuntimeContext())
}

// processFailedExtrinsicWithRuntime parses a failed extrinsic using the runtime
// snapshot for its block and emits gas-only vault observations.
func (s *Scanner) processFailedExtrinsicWithRuntime(extHex string, blockHeight int64, fee uint64, runtime runtimeContext) (*ttypes.TxInItem, error) {
	item, err := s.processExtrinsicWithRuntime(extHex, blockHeight, fee, runtime)
	if err != nil {
		return nil, err
	}

	if item == nil {
		return nil, nil
	}

	fromVault, _ := s.pubkeyMgr.IsValidPoolAddress(item.Sender, s.cfg.ChainID)
	if !fromVault {
		return nil, nil
	}
	if s.failHandler != nil {
		err = s.failHandler(item.Tx)
		if err != nil {
			return nil, fmt.Errorf("%w for %s: %w", errFinalizedFailureCleanup, item.Tx, err)
		}
	}
	if fee == 0 {
		return nil, nil
	}
	txID, err := common.NewTxID(item.Tx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse failed extrinsic hash: %w", err)
	}

	// Match the established failed-outbound observation shape: a dust coin makes
	// the item observable, while the self-referencing memo prevents it from
	// matching and completing the original outbound.
	return &ttypes.TxInItem{
		BlockHeight: item.BlockHeight,
		Tx:          item.Tx,
		Memo:        mem.NewOutboundMemo(txID).String(),
		Sender:      item.Sender,
		To:          item.To,
		Coins: common.NewCoins(common.NewCoin(
			s.cfg.ChainID.GetGasAsset(), cosmos.OneUint(),
		)),
		Gas: item.Gas,
	}, nil
}

// processExtrinsic parses a hex-encoded extrinsic and returns a TxInItem if it's
// a relevant transfer to a vault address.
func (s *Scanner) processExtrinsic(extHex string, blockHeight int64, fee uint64) (*ttypes.TxInItem, error) {
	return s.processExtrinsicWithRuntime(extHex, blockHeight, fee, s.getRuntimeContext())
}

// processExtrinsicWithRuntime parses an extrinsic using the runtime snapshot for
// the block that contains it.
func (s *Scanner) processExtrinsicWithRuntime(extHex string, blockHeight int64, fee uint64, runtime runtimeContext) (*ttypes.TxInItem, error) {
	raw, err := stypes.HexDecode(extHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode extrinsic hex: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic)
	txHash := stypes.ExtrinsicHash(raw)

	var extrinsic *stypes.Extrinsic
	extrinsic, err = stypes.ParseSignedExtrinsic(raw, runtime.metadata, runtime.layout)
	if err != nil {
		if errors.Is(err, stypes.ErrUnsignedExtrinsic) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to parse extrinsic: %w", err)
	}

	if len(extrinsic.CallData) < 2 {
		return nil, fmt.Errorf("call data too short")
	}

	// Get sender SS58 address
	senderAddr, err := ss58.Encode(s.cfg.ChainID.SS58Prefix(), extrinsic.Signer)
	if err != nil {
		return nil, fmt.Errorf("failed to encode sender address: %w", err)
	}

	palletIdx := extrinsic.CallData[0]
	callIdx := extrinsic.CallData[1]

	var txItem *ttypes.TxInItem
	switch {
	case palletIdx == s.params.Index(BalancesPallet) && s.isTransferCall(callIdx):
		txItem, err = s.parseTransfer(extrinsic.CallData[2:], senderAddr, txHash, blockHeight, "", fee)

	case palletIdx == s.params.Index(UtilityPallet) && callIdx == s.params.Index(UtilityBatchAllCall):
		txItem, err = s.parseBatchAll(extrinsic.CallData[2:], senderAddr, txHash, blockHeight, fee)

	default:
		return nil, nil
	}
	if err != nil || txItem == nil {
		return txItem, err
	}

	s.nonceCache.Add(txItem.Tx, extrinsic.Nonce, uint64(blockHeight))

	// Filter oversized memos (defense-in-depth, matches UTXO/EVM/Gaia scanners).
	if len(txItem.Memo) > constants.MaxMemoSize {
		return nil, nil
	}

	return txItem, nil
}

// parseTransfer parses a balance transfer call and returns a TxInItem.
func (s *Scanner) parseTransfer(data []byte, sender, txHash string, blockHeight int64, memo string, fee uint64) (*ttypes.TxInItem, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty transfer data")
	}

	pos := 0

	// Destination: MultiAddress
	if pos >= len(data) {
		return nil, fmt.Errorf("transfer data too short for address type")
	}
	addrType := data[pos]
	pos++

	if addrType != stypes.MultiAddressID {
		return nil, fmt.Errorf("%w: unsupported destination address type: %d", errUnsupportedObservation, addrType)
	}

	if pos+32 > len(data) {
		return nil, fmt.Errorf("transfer data too short for destination")
	}
	destPubkey := data[pos : pos+32]
	pos += 32

	destAddr, err := ss58.Encode(s.cfg.ChainID.SS58Prefix(), destPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	// Amount (compact)
	amount, _, err := stypes.DecodeCompact(data[pos:])
	if err != nil {
		return nil, fmt.Errorf("failed to decode transfer amount: %w", err)
	}

	coin := s.cfg.ChainID.NativeGasToThorCoin(amount)
	if coin.Amount.IsZero() {
		return nil, nil // dust amount, ignore
	}

	// Report +1
	gasCoin := s.cfg.ChainID.NativeGasToThorCoinCeil(cosmos.NewUint(fee))
	if gasCoin.Amount.IsZero() {
		// Gas is not used for inbound observation but cannot be empty.
		gasCoin.Amount = cosmos.OneUint()
	}

	return &ttypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txHash,
		Memo:        memo,
		Sender:      sender,
		To:          destAddr,
		Coins:       []common.Coin{coin},
		Gas:         common.Gas{gasCoin},
	}, nil
}

// parseBatchAll parses a batch_all call looking for transfer + remark.
func (s *Scanner) parseBatchAll(data []byte, sender, txHash string, blockHeight int64, fee uint64) (*ttypes.TxInItem, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty batch data")
	}

	numCalls, consumed, err := stypes.DecodeCompactToInt(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode batch call count: %w", err)
	}
	pos := consumed

	var memo string
	var transferData []byte
	var hasTransfer bool
	var hasRemark bool

	for i := 0; i < numCalls; i++ {
		if pos+2 > len(data) {
			return nil, fmt.Errorf("batch_all call %d is truncated", i)
		}

		pallet := data[pos]
		call := data[pos+1]
		pos += 2

		switch {
		case pallet == s.params.Index(SystemPallet) && call == s.params.Index(SystemRemarkCall):
			// Reject batches with multiple remarks to prevent memo confusion
			// attacks. We track presence with a dedicated flag rather than
			// `memo != ""` because `System.remark("")` is a legal empty-bytes
			// call that would otherwise let a `[remark(""), transfer, remark(...)]`
			// batch slip past the guard.
			if hasRemark {
				return nil, fmt.Errorf("%w: batch_all contains multiple remark calls", errUnsupportedObservation)
			}
			hasRemark = true

			var remarkLength int
			remarkLength, consumed, err = stypes.DecodeCompactToInt(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("failed to decode remark length: %w", err)
			}

			pos += consumed

			if remarkLength > len(data)-pos {
				return nil, fmt.Errorf("remark data exceeds bounds")
			}

			memo = string(data[pos : pos+remarkLength])

			pos += remarkLength
		case pallet == s.params.Index(BalancesPallet) && s.isTransferCall(call):
			// Reject batches with multiple transfers to prevent ambiguity about
			// which transfer the memo applies to.
			if hasTransfer {
				return nil, fmt.Errorf("%w: batch_all contains multiple transfer calls", errUnsupportedObservation)
			}
			start := pos

			// Validate MultiAddress variant before advancing
			if pos >= len(data) {
				return nil, fmt.Errorf("transfer data too short for address type")
			}
			if data[pos] != stypes.MultiAddressID {
				return nil, fmt.Errorf("%w: unsupported destination address type: %d", errUnsupportedObservation, data[pos])
			}
			// Advance past MultiAddress::Id (1 byte variant + 32 bytes pubkey)
			if pos+1+32 > len(data) {
				return nil, fmt.Errorf("transfer data too short")
			}
			pos += 1 + 32
			// Skip compact amount
			_, consumed, err = stypes.DecodeCompact(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("failed to skip transfer amount: %w", err)
			}
			pos += consumed

			transferData = data[start:pos]
			hasTransfer = true
		default:
			return nil, fmt.Errorf(
				"%w: batch_all call %d uses unsupported pallet %d call %d",
				errUnsupportedObservation, i, pallet, call,
			)
		}
	}

	if pos != len(data) {
		return nil, fmt.Errorf("batch_all has %d trailing bytes", len(data)-pos)
	}

	if !hasTransfer {
		return nil, nil
	}

	return s.parseTransfer(transferData, sender, txHash, blockHeight, memo, fee)
}

// updateNetworkFee sends a network fee update to THORChain.
// Rounds to GasPriceResolution and skips if unchanged (like XRP).
func (s *Scanner) updateNetworkFee(height int64) {
	if s.globalNetworkFeeQueue == nil {
		return
	}

	if height%FeeUpdatePeriod != 0 {
		return
	}

	currentFee := s.currentFee.Load()

	// Read the sample count and average under the same lock.
	s.mu.RLock()
	cacheLen := len(s.feeCache)
	var fee uint64
	if cacheLen > 0 {
		// The sum can exceed uint64 even though the average cannot.
		sum := cosmos.ZeroUint()
		for _, f := range s.feeCache {
			sum = sum.AddUint64(f)
		}
		fee = sum.QuoUint64(uint64(cacheLen)).Uint64()
	}
	s.mu.RUnlock()

	// Wait for a full cache before reporting to avoid noisy early estimates.
	if cacheLen < FeeCacheSize {
		fee = s.params.DefaultFee
	}

	if fee == 0 {
		return
	}

	// Round up to GasPriceResolution
	resolution := uint64(s.cfg.GasPriceResolution)
	if resolution > 0 {
		if remainder := fee % resolution; remainder != 0 {
			increment := resolution - remainder
			if fee > math.MaxUint64-increment {
				s.logger.Error().Uint64("fee", fee).Uint64("resolution", resolution).Msg("rounded network fee exceeds uint64")
				return
			}
			fee += increment
		}
	}

	if s.metrics != nil {
		if gauge := s.metrics.GetGauge(metrics.GasPrice(s.cfg.ChainID)); gauge != nil {
			// Whole-transaction fee in native units.
			gauge.Set(float64(fee))
		}
	}

	if fee == currentFee {
		return
	}

	networkFee := common.NetworkFee{
		Chain:           s.cfg.ChainID,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: fee,
	}

	timer := time.NewTimer(constants.ThorchainBlockTime)
	defer timer.Stop()

	select {
	case s.globalNetworkFeeQueue <- networkFee:
	case <-timer.C:
		s.logger.Error().Msg("timeout sending network fee to THORChain")
		return
	}

	s.currentFee.Store(fee)
	if s.metrics != nil {
		if counter := s.metrics.GetCounter(metrics.GasPriceChange(s.cfg.ChainID)); counter != nil {
			counter.Inc()
		}
	}

	s.logger.Info().
		Uint64("fee", fee).
		Int64("height", height).
		Msg("sent network fee to THORChain")
}

// --------------------------------- thread-safe getters/setters ---------------------------------

// setLatestBlock stores the latest block hash and height (thread-safe).
func (s *Scanner) setLatestBlock(hash string, height int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if height < s.latestBlockHeight {
		return
	}
	s.latestBlockHash = hash
	s.latestBlockHeight = height
}

// getGenesisHash returns the genesis hash (immutable after construction).
func (s *Scanner) getGenesisHash() string {
	return s.genesisHash
}

// getLatestBlock returns the latest observed block hash and height atomically.
// Both values come from the same block, which is critical for mortal era
// construction — the era phase must match the block hash in the signing payload.
func (s *Scanner) getLatestBlock() (string, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestBlockHash, s.latestBlockHeight
}

// getRuntimeContext returns metadata, its signed-extension layout, and runtime
// versions from one verified snapshot.
func (s *Scanner) getRuntimeContext() runtimeContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtime
}

func (s *Scanner) loadRuntimeContext(blockHash string, rv *rpc.RuntimeVersion) (runtimeContext, error) {
	if rv == nil {
		return runtimeContext{}, fmt.Errorf("nil runtime version")
	}
	if rv.SpecName != s.params.SpecName {
		return runtimeContext{}, fmt.Errorf(
			"runtime specName mismatch (expected %q, got %q)",
			s.params.SpecName, rv.SpecName,
		)
	}

	raw, err := s.rpcClient.GetMetadata(blockHash)
	if err != nil {
		return runtimeContext{}, fmt.Errorf("failed to get metadata: %w", err)
	}

	metadata, err := stypes.ParseMetadata(raw)
	if err != nil {
		return runtimeContext{}, fmt.Errorf("failed to parse metadata: %w", err)
	}

	layout, err := s.params.ValidateMetadata(metadata)
	if err != nil {
		return runtimeContext{}, err
	}

	return runtimeContext{
		metadata:    metadata,
		layout:      layout,
		specVersion: rv.SpecVersion,
		txVersion:   rv.TransactionVersion,
	}, nil
}

// --------------------------------- helpers ---------------------------------

// isTransferCall returns true if the call index is a balance transfer variant.
// Accepts: transfer_allow_death (0), transfer_keep_alive (3).
// Note: transfer_all (4) is intentionally excluded — it has no amount field,
// requiring event correlation to determine the transferred amount.
// Note: call index 7 is force_set_balance (governance-only), NOT a transfer.
func (s *Scanner) isTransferCall(callIdx byte) bool {
	return callIdx == s.params.Index(BalancesTransferAllowDeathCall) ||
		callIdx == s.params.Index(BalancesTransferKeepAliveCall)
}
