package bittensor

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	taorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/rpc"
	taotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	// defaultFeeRao is the approximate base fee in rao (0.0001 TAO).
	defaultFeeRao = 100_000

	// transferKeepAliveCallIndex is the call index for Balances::transfer_keep_alive.
	transferKeepAliveCallIndex = 3
	// transferAllowDeathCallIndex is the call index for Balances::transfer_allow_death
	// (index 0 in current Substrate, formerly named "transfer").
	transferAllowDeathCallIndex = 0
	// runtimeVersionRefreshInterval is how often (in blocks) to refresh runtime version.
	runtimeVersionRefreshInterval = 100

	// feeCacheSize is the number of fee samples in the rolling window.
	feeCacheSize = 200
	// feeUpdatePeriodBlocks is how often (in blocks) to report the averaged fee.
	feeUpdatePeriodBlocks = 100

	// expectedSpecName is the expected Substrate runtime specName for Bittensor.
	// Used at startup to warn if connected to a different chain.
	expectedSpecName = "node-subtensor-runtime"
)

// TAOScanner is a block scanner for the Bittensor (TAO) chain.
type TAOScanner struct {
	cfg              config.BifrostBlockScannerConfiguration
	logger           zerolog.Logger
	rpcClient        *taorpc.Client
	solvencyReporter types.SolvencyReporter

	globalNetworkFeeQueue chan common.NetworkFee

	// Set once at construction, immutable thereafter.
	genesisHash string

	// Cached runtime metadata (protected by mu)
	mu                sync.RWMutex
	specVersion       uint32
	txVersion         uint32
	latestBlockHash   string
	latestBlockHeight int64
	feeCache          []uint64 // rolling window of observed fees (rao)
	lastReportedFee   uint64   // last fee sent to THORChain (rao)

	// Bittensor pallet indices (set once at construction, immutable thereafter)
	balancesPalletIndex           byte
	transferCallIndex             byte
	systemPalletIndex             byte
	remarkCallIndex               byte
	utilityPalletIndex            byte
	batchAllCallIndex             byte
	transactionPaymentPalletIndex byte
}

// NewTAOScanner creates a new TAO block scanner.
func NewTAOScanner(
	cfg config.BifrostBlockScannerConfiguration,
	solvencyReporter types.SolvencyReporter,
	rpcClient *taorpc.Client,
) (*TAOScanner, error) {
	scanner := &TAOScanner{
		cfg:              cfg,
		logger:           log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		rpcClient:        rpcClient,
		solvencyReporter: solvencyReporter,
		// Default pallet indices for Bittensor
		balancesPalletIndex:           5,
		transferCallIndex:             transferKeepAliveCallIndex,
		systemPalletIndex:             0,
		remarkCallIndex:               0,
		utilityPalletIndex:            11,
		batchAllCallIndex:             2,
		transactionPaymentPalletIndex: 6,
	}

	// Fetch genesis hash and runtime version
	genesisHash, err := rpcClient.GetGenesisHash()
	if err != nil {
		return nil, fmt.Errorf("fail to get genesis hash: %w", err)
	}
	scanner.genesisHash = genesisHash

	rv, err := rpcClient.GetRuntimeVersion("")
	if err != nil {
		return nil, fmt.Errorf("fail to get runtime version: %w", err)
	}
	scanner.specVersion = rv.SpecVersion
	scanner.txVersion = rv.TransactionVersion

	// Validate we're connected to a Bittensor node, not another Substrate chain.
	// Pallet indices are hardcoded for Bittensor's runtime; a different runtime
	// would have different pallet ordering, causing silent misattribution of events.
	if rv.SpecName != expectedSpecName {
		scanner.logger.Warn().
			Str("expected", expectedSpecName).
			Str("got", rv.SpecName).
			Msg("runtime specName mismatch — pallet indices may be incorrect")
	}

	scanner.logger.Info().
		Str("spec_name", rv.SpecName).
		Uint32("spec_version", rv.SpecVersion).
		Uint32("tx_version", rv.TransactionVersion).
		Str("genesis_hash", genesisHash).
		Msg("TAO scanner initialized")

	return scanner, nil
}

// GetHeight returns the current finalized block height.
func (s *TAOScanner) GetHeight() (int64, error) {
	hash, err := s.rpcClient.GetFinalizedHead()
	if err != nil {
		return -1, fmt.Errorf("fail to get finalized head: %w", err)
	}

	header, err := s.rpcClient.GetHeader(hash)
	if err != nil {
		return -1, fmt.Errorf("fail to get header: %w", err)
	}

	blockNum, err := header.BlockNumber()
	if err != nil {
		return -1, fmt.Errorf("fail to parse block number: %w", err)
	}

	return int64(blockNum), nil
}

// FetchTxs is called by the standard block scanner for each block.
func (s *TAOScanner) FetchTxs(fetchHeight, chainHeight int64) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain: s.cfg.ChainID,
	}

	height := uint64(fetchHeight)
	blockHash, err := s.rpcClient.GetBlockHash(&height)
	if err != nil {
		return txIn, fmt.Errorf("fail to get block hash for height %d: %w", fetchHeight, err)
	}

	block, err := s.rpcClient.GetBlock(blockHash)
	if err != nil {
		return txIn, fmt.Errorf("fail to get block at height %d: %w", fetchHeight, err)
	}

	txIn.TxArray = make([]*stypes.TxInItem, 0, len(block.Extrinsics))

	// Fetch events and determine which extrinsics succeeded
	successMap, feeMap, err := s.getExtrinsicResults(blockHash, len(block.Extrinsics))
	if err != nil {
		return txIn, fmt.Errorf("fail to get extrinsic results at height %d: %w", fetchHeight, err)
	}

	// Store latest block hash and height for transaction construction (thread-safe)
	s.setLatestBlock(blockHash, fetchHeight)

	// Refresh runtime version periodically
	if fetchHeight > 0 && fetchHeight%runtimeVersionRefreshInterval == 0 {
		s.refreshRuntimeVersion(blockHash)
	}

	// Process extrinsics in the block
	for i, extHex := range block.Extrinsics {
		// Only process extrinsics confirmed as successful
		succeeded, known := successMap[uint32(i)]
		if !known {
			// Could not determine success status — skip (safe direction)
			continue
		}
		if !succeeded {
			continue
		}

		actualFee := feeMap[uint32(i)]
		txItem, err := s.processExtrinsic(extHex, fetchHeight, actualFee)
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
	if fetchHeight > 0 && fetchHeight%feeUpdatePeriodBlocks == 0 {
		s.reportNetworkFee(fetchHeight)
	}

	// Report solvency
	if s.solvencyReporter != nil {
		if err := s.solvencyReporter(fetchHeight); err != nil {
			s.logger.Err(err).Msg("fail to report solvency")
		}
	}

	return txIn, nil
}

// FetchMemPool is not used for TAO.
func (s *TAOScanner) FetchMemPool(height int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// GetNetworkFee returns the current network fee estimate.
func (s *TAOScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	fee := s.averageFee()
	if fee == 0 {
		fee = defaultFeeRao
	}
	// Transaction size = 1 (we use a single gas amount)
	return 1, fee
}

// getExtrinsicResults queries System::Events and determines which extrinsics succeeded.
// numExtrinsics is the number of extrinsics in the block, used to validate scan results.
func (s *TAOScanner) getExtrinsicResults(blockHash string, numExtrinsics int) (map[uint32]bool, map[uint32]uint64, error) {
	eventsHex, err := s.rpcClient.GetEventsStorage(blockHash)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get events storage: %w", err)
	}

	eventsData, err := taotypes.HexDecode(eventsHex)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to decode events hex: %w", err)
	}

	palletConfig := taotypes.EventPalletConfig{
		SystemPalletIndex:             s.systemPalletIndex,
		BalancesPalletIndex:           s.balancesPalletIndex,
		UtilityPalletIndex:            s.utilityPalletIndex,
		TransactionPaymentPalletIndex: s.transactionPaymentPalletIndex,
	}

	results, fees, err := taotypes.ParseExtrinsicResults(eventsData, palletConfig)
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
			Str("events_hex_head", taotypes.HexEncodeHead(eventsData, 200)).
			Msg("using scan-based event fallback for undetermined extrinsics")
		scanResults := taotypes.ScanExtrinsicResults(eventsData, palletConfig, numExtrinsics)
		for idx, success := range scanResults {
			if _, ok := results[idx]; !ok {
				results[idx] = success
			}
		}
	}

	return results, fees, nil
}

// refreshRuntimeVersion re-fetches the runtime version from the node.
func (s *TAOScanner) refreshRuntimeVersion(blockHash string) {
	rv, err := s.rpcClient.GetRuntimeVersion(blockHash)
	if err != nil {
		s.logger.Warn().Err(err).Msg("fail to refresh runtime version")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if rv.SpecVersion != s.specVersion || rv.TransactionVersion != s.txVersion {
		s.logger.Info().
			Uint32("old_spec", s.specVersion).Uint32("new_spec", rv.SpecVersion).
			Uint32("old_tx", s.txVersion).Uint32("new_tx", rv.TransactionVersion).
			Msg("runtime version updated")
		s.specVersion = rv.SpecVersion
		s.txVersion = rv.TransactionVersion
	}
}

// updateFeeCache adds a fee to the rolling window, truncating if over feeCacheSize.
func (s *TAOScanner) updateFeeCache(fee uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feeCache = append(s.feeCache, fee)
	if len(s.feeCache) > feeCacheSize {
		s.feeCache = s.feeCache[len(s.feeCache)-feeCacheSize:]
	}
}

// averageFee returns the mean of cached fees, or 0 if the cache is empty.
func (s *TAOScanner) averageFee() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.feeCache) == 0 {
		return 0
	}
	var sum uint64
	for _, f := range s.feeCache {
		sum += f
	}
	return sum / uint64(len(s.feeCache))
}

// processExtrinsic parses a hex-encoded extrinsic and returns a TxInItem if it's
// a relevant transfer to a vault address.
func (s *TAOScanner) processExtrinsic(extHex string, blockHeight int64, actualFee uint64) (*stypes.TxInItem, error) {
	raw, err := taotypes.HexDecode(extHex)
	if err != nil {
		return nil, fmt.Errorf("fail to decode extrinsic hex: %w", err)
	}

	// Compute extrinsic hash (Blake2b-256 of the raw encoded extrinsic)
	txHash := taotypes.ExtrinsicHash(raw)

	// Decode the length prefix
	_, consumed, err := taotypes.DecodeCompact(raw)
	if err != nil {
		return nil, fmt.Errorf("fail to decode extrinsic length: %w", err)
	}
	body := raw[consumed:]

	if len(body) == 0 {
		return nil, fmt.Errorf("empty extrinsic body")
	}

	// Check if this is a signed extrinsic (bit 7 set) using format version 4
	versionByte := body[0]
	if versionByte&0x80 == 0 {
		return nil, nil // Unsigned extrinsic
	}
	if versionByte&0x7F != 4 {
		return nil, fmt.Errorf("unsupported extrinsic version: %d", versionByte&0x7F)
	}

	// Parse signed extrinsic (version 4)
	pos := 1

	// Signer: MultiAddress type
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for signer type")
	}
	signerType := body[pos]
	pos++

	if signerType != taotypes.MultiAddressID {
		return nil, fmt.Errorf("unsupported signer type: %d", signerType)
	}

	// 32-byte signer account ID
	if pos+32 > len(body) {
		return nil, fmt.Errorf("extrinsic too short for signer")
	}
	signerPubkey := body[pos : pos+32]
	pos += 32

	// Signature type: Ed25519 (0x00) and Sr25519 (0x01) use 64-byte signatures,
	// ECDSA (0x02) uses 65 bytes. Reject unknown types to prevent parser misalignment.
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for signature type")
	}
	sigType := body[pos]
	pos++
	sigLen := 64
	if sigType == 0x02 { // ECDSA
		sigLen = 65
	} else if sigType > 0x02 {
		return nil, fmt.Errorf("unsupported signature type: %d", sigType)
	}

	if pos+sigLen > len(body) {
		return nil, fmt.Errorf("extrinsic too short for signature")
	}
	pos += sigLen

	// Era
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for era")
	}
	if body[pos] == 0x00 {
		pos++ // immortal era
	} else {
		if pos+2 > len(body) {
			return nil, fmt.Errorf("extrinsic too short for mortal era")
		}
		pos += 2 // mortal era
	}

	// Nonce (compact)
	_, nonceLen, err := taotypes.DecodeCompact(body[pos:])
	if err != nil {
		return nil, fmt.Errorf("fail to decode nonce: %w", err)
	}
	pos += nonceLen

	// Tip (compact)
	_, tipLen, err := taotypes.DecodeCompact(body[pos:])
	if err != nil {
		return nil, fmt.Errorf("fail to decode tip: %w", err)
	}
	pos += tipLen

	// CheckMetadataHash mode byte (part of Bittensor's TransactionExtensions)
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for metadata mode byte")
	}
	pos++ // skip metadata mode byte

	// The remainder is call data
	callData := body[pos:]
	if len(callData) < 2 {
		return nil, fmt.Errorf("call data too short")
	}

	// Get sender SS58 address
	senderAddr, err := ss58.Encode(ss58.PrefixTAO, signerPubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to encode sender address: %w", err)
	}

	palletIdx := callData[0]
	callIdx := callData[1]

	var txItem *stypes.TxInItem
	switch {
	case palletIdx == s.balancesPalletIndex && isTransferCall(callIdx):
		txItem, err = s.parseTransfer(callData[2:], senderAddr, txHash, blockHeight, "", actualFee)

	case palletIdx == s.utilityPalletIndex && callIdx == s.batchAllCallIndex:
		txItem, err = s.parseBatchAll(callData[2:], senderAddr, txHash, blockHeight, actualFee)

	default:
		return nil, nil
	}
	if err != nil || txItem == nil {
		return txItem, err
	}

	// Filter oversized memos (defense-in-depth, matches UTXO/EVM/Gaia scanners).
	if len([]byte(txItem.Memo)) > constants.MaxMemoSize {
		return nil, nil
	}

	return txItem, nil
}

// parseTransfer parses a balance transfer call and returns a TxInItem.
func (s *TAOScanner) parseTransfer(data []byte, sender, txHash string, blockHeight int64, memo string, actualFee uint64) (*stypes.TxInItem, error) {
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

	if addrType != taotypes.MultiAddressID {
		return nil, fmt.Errorf("unsupported destination address type: %d", addrType)
	}

	if pos+32 > len(data) {
		return nil, fmt.Errorf("transfer data too short for destination")
	}
	destPubkey := data[pos : pos+32]
	pos += 32

	destAddr, err := ss58.Encode(ss58.PrefixTAO, destPubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to encode destination address: %w", err)
	}

	// Amount (compact)
	amount, _, err := taotypes.DecodeCompact(data[pos:])
	if err != nil {
		return nil, fmt.Errorf("fail to decode transfer amount: %w", err)
	}

	amountThorchain := convertRaoToThorchain(amount)
	if amountThorchain.IsZero() {
		return nil, nil // dust amount, ignore
	}

	taoCoin := common.NewCoin(common.TAOAsset, amountThorchain)
	taoCoin.Decimals = 9

	feeRao := actualFee
	if feeRao == 0 {
		feeRao = defaultFeeRao
	}
	gasCoin := common.NewCoin(common.TAOAsset, convertRaoToThorchain(feeRao))
	gasCoin.Decimals = 9

	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txHash,
		Memo:        memo,
		Sender:      sender,
		To:          destAddr,
		Coins:       []common.Coin{taoCoin},
		Gas:         common.Gas{gasCoin},
	}, nil
}

// parseBatchAll parses a batch_all call looking for transfer + remark.
func (s *TAOScanner) parseBatchAll(data []byte, sender, txHash string, blockHeight int64, actualFee uint64) (*stypes.TxInItem, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty batch data")
	}

	numCalls, consumed, err := taotypes.DecodeCompact(data)
	if err != nil {
		return nil, fmt.Errorf("fail to decode batch call count: %w", err)
	}
	pos := consumed

	var memo string
	var transferData []byte
	var hasTransfer bool

	for i := uint64(0); i < numCalls; i++ {
		if pos+2 > len(data) {
			break
		}

		callPalletIdx := data[pos]
		callCallIdx := data[pos+1]
		pos += 2

		if callPalletIdx == s.systemPalletIndex && callCallIdx == s.remarkCallIndex {
			if memo != "" {
				// Reject batches with multiple remarks to prevent memo confusion attacks.
				return nil, fmt.Errorf("batch_all contains multiple remark calls")
			}
			remarkLen, rl, decErr := taotypes.DecodeCompact(data[pos:])
			if decErr != nil {
				return nil, fmt.Errorf("fail to decode remark length: %w", decErr)
			}
			pos += rl
			if remarkLen > uint64(len(data)-pos) {
				return nil, fmt.Errorf("remark data exceeds bounds")
			}
			rlInt := int(remarkLen)
			memo = string(data[pos : pos+rlInt])
			pos += rlInt
		} else if callPalletIdx == s.balancesPalletIndex && isTransferCall(callCallIdx) {
			// Reject batches with multiple transfers to prevent ambiguity about
			// which transfer the memo applies to.
			if hasTransfer {
				return nil, fmt.Errorf("batch_all contains multiple transfer calls")
			}
			transferData = data[pos:]
			hasTransfer = true

			// Validate MultiAddress variant before advancing
			if pos >= len(data) {
				return nil, fmt.Errorf("transfer data too short for address type")
			}
			if data[pos] != taotypes.MultiAddressID {
				return nil, fmt.Errorf("unsupported MultiAddress variant in batch transfer: %d", data[pos])
			}
			// Advance past MultiAddress::Id (1 byte variant + 32 bytes pubkey)
			if pos+1+32 > len(data) {
				return nil, fmt.Errorf("transfer data too short")
			}
			pos += 1 + 32
			// Skip compact amount
			_, al, decErr := taotypes.DecodeCompact(data[pos:])
			if decErr != nil {
				return nil, fmt.Errorf("fail to skip transfer amount: %w", decErr)
			}
			pos += al
		} else {
			// Unknown call in batch — we can't determine its size, so stop parsing.
			// Any transfer + memo already captured will still be used.
			s.logger.Warn().
				Uint64("call_index", i).
				Uint64("total_calls", numCalls).
				Uint8("pallet", callPalletIdx).
				Uint8("call", callCallIdx).
				Bool("has_transfer", hasTransfer).
				Msg("batch_all contains unknown call, stopping parse early")
			break
		}
	}

	if !hasTransfer {
		return nil, nil
	}

	return s.parseTransfer(transferData, sender, txHash, blockHeight, memo, actualFee)
}

// reportNetworkFee sends a network fee update to THORChain.
// Rounds to GasPriceResolution and skips if unchanged (like XRP).
func (s *TAOScanner) reportNetworkFee(height int64) {
	if s.globalNetworkFeeQueue == nil {
		return
	}

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
	lastReported := s.lastReportedFee
	s.mu.RUnlock()

	// Wait for a full cache before reporting to avoid noisy early estimates.
	if cacheLen < feeCacheSize {
		return
	}

	if fee == 0 {
		fee = defaultFeeRao
	}

	// Round up to GasPriceResolution
	resolution := uint64(s.cfg.GasPriceResolution)
	if resolution > 0 {
		fee = ((fee + resolution - 1) / resolution) * resolution
	}

	if fee == lastReported {
		return
	}

	s.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           common.TAOChain,
		Height:          height,
		TransactionSize: 1,
		TransactionRate: fee,
	}

	s.mu.Lock()
	s.lastReportedFee = fee
	s.mu.Unlock()

	s.logger.Info().
		Uint64("fee", fee).
		Int64("height", height).
		Msg("sent network fee to THORChain")
}

// --------------------------------- thread-safe getters/setters ---------------------------------

// setLatestBlock stores the latest block hash and height (thread-safe).
func (s *TAOScanner) setLatestBlock(hash string, height int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latestBlockHash = hash
	s.latestBlockHeight = height
}

// getGenesisHash returns the genesis hash (immutable after construction).
func (s *TAOScanner) getGenesisHash() string {
	return s.genesisHash
}

// getLatestBlock returns the latest observed block hash and height atomically.
// Both values come from the same block, which is critical for mortal era
// construction — the era phase must match the block hash in the signing payload.
func (s *TAOScanner) getLatestBlock() (string, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestBlockHash, s.latestBlockHeight
}

// getRuntimeVersion returns the cached spec and transaction versions atomically.
// Both are updated together during runtime upgrades, so reading them under a
// single lock prevents the signing payload from mixing old and new versions.
func (s *TAOScanner) getRuntimeVersion() (specVersion, txVersion uint32) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.specVersion, s.txVersion
}

// --------------------------------- helpers ---------------------------------

// isTransferCall returns true if the call index is a balance transfer variant.
// Accepts: transfer_allow_death (0), transfer_keep_alive (3).
// Note: transfer_all (4) is intentionally excluded — it has no amount field,
// requiring event correlation to determine the transferred amount.
// Note: call index 7 is force_set_balance (governance-only), NOT a transfer.
func isTransferCall(callIdx byte) bool {
	return callIdx == transferAllowDeathCallIndex ||
		callIdx == transferKeepAliveCallIndex
}

// convertRaoToThorchain converts rao (1e9) to THORChain units (1e8).
func convertRaoToThorchain(rao uint64) cosmos.Uint {
	return cosmos.NewUint(rao / 10)
}
