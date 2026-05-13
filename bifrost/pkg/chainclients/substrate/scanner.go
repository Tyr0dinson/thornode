package substrate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	ttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	FeeCacheSize    = 200
	FeeUpdatePeriod = 20
	NonceHashBlocks = 100
)

// Scanner is a generic block scanner for Substrate chains.
type Scanner struct {
	cfg              config.BifrostBlockScannerConfiguration
	logger           zerolog.Logger
	rpcClient        *rpc.Client
	solvencyReporter types.SolvencyReporter
	pubkeyMgr        pubkeymanager.PubKeyValidator

	globalNetworkFeeQueue chan common.NetworkFee

	// Set once at construction, immutable thereafter.
	genesisHash string
	params      Params

	nonceCache *nonceCache

	// Cached runtime metadata (protected by mu)
	mu                sync.RWMutex
	specVersion       uint32
	txVersion         uint32
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
) (*Scanner, error) {
	params, found := ChainParams[cfg.ChainID]
	if !found {
		return nil, fmt.Errorf("no params found for %s", cfg.ChainID)
	}

	s := &Scanner{
		cfg:              cfg,
		logger:           log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		params:           params,
		rpcClient:        rpcClient,
		solvencyReporter: solvencyReporter,
		pubkeyMgr:        pubkeyMgr,
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

	err = s.verifyMetadata("")
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
	err = s.checkRuntimeVersion(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("failed to refresh runtime version: %w", err)
	}

	block, err := s.rpcClient.GetBlock(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("failed to get block at height %d: %w", fetchHeight, err)
	}

	txIn.TxArray = make([]*ttypes.TxInItem, 0, len(block.Extrinsics))

	// Fetch events and determine which extrinsics succeeded
	successMap, feeMap, err := s.getExtrinsicResults(blockHash, len(block.Extrinsics))
	if err != nil {
		return txIn, fmt.Errorf("failed to get extrinsic results at height %d: %w", fetchHeight, err)
	}

	// Store latest block hash and height for transaction construction (thread-safe)
	s.setLatestBlock(blockHash, fetchHeight)

	// Process extrinsics in the block
	for i, extHex := range block.Extrinsics {
		// Only process extrinsics confirmed as successful
		succeeded, known := successMap[uint32(i)]
		if !known {
			// Could not determine success status — skip (safe direction)
			continue
		}

		actualFee := feeMap[uint32(i)]

		var txItem *ttypes.TxInItem

		if succeeded {
			txItem, err = s.processExtrinsic(extHex, fetchHeight, actualFee)
		} else {
			txItem, err = s.processFailedExtrinsic(extHex, fetchHeight, actualFee)
		}

		if err != nil {
			s.logger.Debug().Err(err).Int("index", i).Msg("skip extrinsic")
			continue
		}

		if txItem == nil {
			continue
		}

		txIn.TxArray = append(txIn.TxArray, txItem)

		// Collect actual fee from events for the rolling cache.
		// These are the most representative since they match our tx type.
		if actualFee > 0 {
			s.updateFeeCache(actualFee)
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

	return txIn, nil
}

// FetchMemPool is not used for Substrate chains.
func (s *Scanner) FetchMemPool(_ int64) (ttypes.TxIn, error) {
	return ttypes.TxIn{}, nil
}

// GetNetworkFee returns the current network fee estimate.
func (s *Scanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	fee := s.currentFee.Load()
	if fee == 0 {
		fee = s.params.DefaultFee
	}
	return 1, fee
}

// getExtrinsicResults queries System::Events and determines which extrinsics succeeded.
// numExtrinsics is the number of extrinsics in the block, used to validate scan results.
func (s *Scanner) getExtrinsicResults(blockHash string, numExtrinsics int) (map[uint32]bool, map[uint32]uint64, error) {
	eventsHex, err := s.rpcClient.GetEventsStorage(blockHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get events storage: %w", err)
	}

	eventsData, err := stypes.HexDecode(eventsHex)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode events hex: %w", err)
	}

	palletConfig := stypes.EventPalletConfig{
		SystemPalletIndex:             s.params.Index(SystemPallet),
		BalancesPalletIndex:           s.params.Index(BalancesPallet),
		BalancesTransferEventIndex:    s.params.Index(BalancesTransferEvent),
		UtilityPalletIndex:            s.params.Index(UtilityPallet),
		TransactionPaymentPalletIndex: s.params.Index(TransactionPaymentPallet),
		BalanceSize:                   s.params.BalanceSize,
	}

	results, fees, err := stypes.ParseExtrinsicResults(eventsData, palletConfig)
	if err != nil {
		s.logger.Warn().Err(err).Msg("events parsing encountered error, using partial results")
	}

	// Check if all extrinsics have determined results
	allDetermined := true
	for i := 0; i < numExtrinsics; i++ {
		if _, ok := results[uint32(i)]; !ok {
			allDetermined = false
			break
		}
	}

	// If some extrinsics are missing results, use scan-based fallback
	if !allDetermined && len(eventsData) > 0 {
		s.logger.Info().
			Int("num_extrinsics", numExtrinsics).
			Int("determined", len(results)).
			Int("events_data_len", len(eventsData)).
			Str("events_hex_head", stypes.HexEncodeHead(eventsData, 200)).
			Msg("using scan-based event fallback for undetermined extrinsics")
		scanResults, scanFees := stypes.ScanExtrinsicResults(eventsData, palletConfig, numExtrinsics)
		for idx, success := range scanResults {
			if _, ok := results[idx]; ok {
				continue
			}
			results[idx] = success
			s.logger.Warn().
				Uint32("ext_idx", idx).
				Str("block_hash", blockHash).
				Msg("extrinsic success determined solely by scan fallback")
		}
		// Back-fill fees the linear parser missed because it stopped at an
		// unknown event before reaching the TransactionFeePaid event for a
		// later extrinsic. Without this, outbound observations report gas of
		// 1 unit and thornode under-debits the vault.
		for idx, fee := range scanFees {
			if _, ok := fees[idx]; ok {
				continue
			}
			fees[idx] = fee
			s.logger.Warn().
				Uint32("ext_idx", idx).
				Uint64("fee", fee).
				Str("block_hash", blockHash).
				Msg("extrinsic fee recovered solely by scan fallback")
		}
	}

	return results, fees, nil
}

// checkRuntimeVersion re-fetches the runtime version from the node and
// verifies index validity on a version change
func (s *Scanner) checkRuntimeVersion(blockHash string) error {
	rv, err := s.rpcClient.GetRuntimeVersion(blockHash)
	if err != nil {
		return fmt.Errorf("failed to refresh runtime version: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if rv.SpecVersion != s.specVersion || rv.TransactionVersion != s.txVersion {
		s.logger.Info().
			Uint32("old_spec", s.specVersion).Uint32("new_spec", rv.SpecVersion).
			Uint32("old_tx", s.txVersion).Uint32("new_tx", rv.TransactionVersion).
			Msg("runtime version updated")

		err = s.verifyMetadata(blockHash)
		if err != nil {
			return fmt.Errorf("failed to verify metadata: %w", err)
		}

		s.specVersion = rv.SpecVersion
		s.txVersion = rv.TransactionVersion
	}

	return nil
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

func (s *Scanner) processFailedExtrinsic(extHex string, blockHeight int64, actualFee uint64) (*ttypes.TxInItem, error) {
	if actualFee == 0 {
		return nil, nil
	}

	item, err := s.processExtrinsic(extHex, blockHeight, actualFee)
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

	// Return gas-only TxInItem for failed extrinsics
	return &ttypes.TxInItem{
		BlockHeight: item.BlockHeight,
		Tx:          item.Tx,
		Memo:        item.Memo,
		Sender:      item.Sender,
		To:          item.To,
		Gas:         item.Gas,
	}, nil
}

// processExtrinsic parses a hex-encoded extrinsic and returns a TxInItem if it's
// a relevant transfer to a vault address.
func (s *Scanner) processExtrinsic(extHex string, blockHeight int64, actualFee uint64) (*ttypes.TxInItem, error) {
	raw, err := stypes.HexDecode(extHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode extrinsic hex: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic)
	txHash := stypes.ExtrinsicHash(raw)

	var extrinsic *stypes.Extrinsic
	extrinsic, err = stypes.ParseSignedExtrinsic(raw, s.params.CheckMetadataHash)
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
		txItem, err = s.parseTransfer(extrinsic.CallData[2:], senderAddr, txHash, blockHeight, "", actualFee)

	case palletIdx == s.params.Index(UtilityPallet) && callIdx == s.params.Index(UtilityBatchAllCall):
		txItem, err = s.parseBatchAll(extrinsic.CallData[2:], senderAddr, txHash, blockHeight, actualFee)

	default:
		return nil, nil
	}
	if err != nil || txItem == nil {
		return txItem, err
	}

	s.nonceCache.Add(txItem.Tx, extrinsic.Nonce, uint64(blockHeight))

	// Filter oversized memos (defense-in-depth, matches UTXO/EVM/Gaia scanners).
	if len([]byte(txItem.Memo)) > constants.MaxMemoSize {
		return nil, nil
	}

	return txItem, nil
}

// parseTransfer parses a balance transfer call and returns a TxInItem.
func (s *Scanner) parseTransfer(data []byte, sender, txHash string, blockHeight int64, memo string, actualFee uint64) (*ttypes.TxInItem, error) {
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
		return nil, fmt.Errorf("unsupported destination address type: %d", addrType)
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
	gasCoin := s.cfg.ChainID.NativeGasToThorCoinCeil(cosmos.NewUint(actualFee))
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
func (s *Scanner) parseBatchAll(data []byte, sender, txHash string, blockHeight int64, actualFee uint64) (*ttypes.TxInItem, error) {
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

	stop := false
	for i := 0; i < numCalls; i++ {
		if pos+2 > len(data) {
			break
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
				return nil, fmt.Errorf("batch_all contains multiple remark calls")
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
				return nil, fmt.Errorf("batch_all contains multiple transfer calls")
			}
			start := pos

			// Validate MultiAddress variant before advancing
			if pos >= len(data) {
				return nil, fmt.Errorf("transfer data too short for address type")
			}
			if data[pos] != stypes.MultiAddressID {
				return nil, fmt.Errorf("unsupported MultiAddress variant in batch transfer: %d", data[pos])
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
			// Unknown call in batch — we can't determine its size, so stop parsing.
			// Any transfer + memo already captured will still be used.
			stop = true
			s.logger.Warn().
				Int("call_index", i).
				Int("total_calls", numCalls).
				Uint8("pallet", pallet).
				Uint8("call", call).
				Bool("has_transfer", hasTransfer).
				Msg("batch_all contains unknown call, stopping parse early")
		}

		if stop {
			break
		}
	}

	if !hasTransfer {
		return nil, nil
	}

	return s.parseTransfer(transferData, sender, txHash, blockHeight, memo, actualFee)
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

	// Read cache length, average, and last-reported under a single RLock
	// to avoid TOCTOU between the length check and the average calculation.
	s.mu.RLock()
	cacheLen := len(s.feeCache)
	var fee uint64
	if cacheLen > 0 {
		var sum uint64
		for _, f := range s.feeCache {
			sum += f
		}
		fee = sum / uint64(cacheLen)
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
		fee = ((fee + resolution - 1) / resolution) * resolution
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

	s.currentFee.Swap(fee)

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

// getRuntimeVersion returns the cached spec and transaction versions atomically.
// Both are updated together during runtime upgrades, so reading them under a
// single lock prevents the signing payload from mixing old and new versions.
func (s *Scanner) getRuntimeVersion() (specVersion, txVersion uint32) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.specVersion, s.txVersion
}

func (s *Scanner) verifyMetadata(blockHash string) error {
	raw, err := s.rpcClient.GetMetadata(blockHash)
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	metadata, err := stypes.ParseMetadata(raw)
	if err != nil {
		return fmt.Errorf("failed to parse metadata: %w", err)
	}

	return s.params.ValidateMetadata(metadata)
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
