package solana

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mr-tron/base58"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	shtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	sdk "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"golang.org/x/sync/errgroup"
)

const (
	baseFeePerSigLamports = 5000 // "Base" fee per signature of a transaction

	transactionQueryLimit = 1000 // maximum number of transactions to query in a single request

	maxConcurrentTxQueries = 5 // maximum number of concurrent tx queries to the RPC node

	// solFeeReportDeadbandSteps is the minimum distance, in GasPriceResolution steps,
	// between a sampled fee and the agreed fee before a node votes. Single-block
	// medians move by about one step most boundaries; voting on each move would make
	// nearly every boundary a quorum.
	solFeeReportDeadbandSteps = 2
)

var (
	errNoSOLFeeSampleBlocks    = errors.New("no finalized blocks in SOL fee sample")
	errSOLFeeSampleUnavailable = errors.New("SOL fee sample unavailable")
)

////////////////////////////////////////////////////////////////////////////////////////
// SOLScanner
////////////////////////////////////////////////////////////////////////////////////////

type SOLScanner struct {
	cfg                config.BifrostBlockScannerConfiguration
	logger             zerolog.Logger
	db                 blockscanner.ScannerStorageSolana
	m                  *metrics.Metrics
	errCounter         *prometheus.CounterVec
	solRpc             *rpc.SolRPC
	pubKeyMgr          pubkeymanager.PubKeyValidator
	bridge             thorclient.ThorchainBridge
	solvencyReporter   shtypes.SolvencyReporter
	signerCacheManager *signercache.CacheManager

	globalNetworkFeeQueue chan common.NetworkFee
	globalTxsQueue        chan stypes.TxIn

	lastFeeRate atomic.Uint64 // latest local sample, not THORChain's agreed fee

	healthy *atomic.Bool

	// currentScanSlot holds the slot the scan loop is currently processing. It is
	// stored immediately before invoking the solvency reporter so ReportSolvency
	// can tell a scan-driven call (height == currentScanSlot) apart from a
	// SolvencyCheckRunner call (height == live chain tip). Mirrors EVM's
	// evmScanner.currentBlockHeight, and is kept separate from lastHeight (the
	// persisted resume cursor, which is only advanced after a scan completes).
	currentScanSlot atomic.Int64

	// Network fee scheduling is owned by the scan goroutine. The pending slot
	// anchors startup or pruned-history recovery until that boundary completes.
	netFeeSlotMultiple uint64
	netFeePendingSlot  *uint64

	lastLog time.Time
	// counter for observed txs since last log
	txCount int

	stopChan chan struct{}

	// state for if we've successfully processed the initial height
	initialHeight map[string]struct{}
	lastHeight    uint64
	// Guards lastHeight between scan progress writes and height/solvency runner reads.
	lastHeightMu      sync.RWMutex
	vaultScanStatuses map[string]vaultScanStatus

	isChainPaused  bool
	lastMimirCheck time.Time
}

type vaultScanStatus struct {
	lastSig  string
	lastSlot uint64
}

// NewSOLScanner create a new instance of SOLScanner.
func NewSOLScanner(
	stopChan chan struct{},
	cfg config.BifrostBlockScannerConfiguration,
	storage blockscanner.ScannerStorageSolana,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	rpc *rpc.SolRPC,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	solvencyReporter shtypes.SolvencyReporter,
	signerCacheManager *signercache.CacheManager,
) (*SOLScanner, error) {
	// check required arguments
	if storage == nil {
		return nil, errors.New("storage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics manager is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}

	return &SOLScanner{
		cfg:                cfg,
		logger:             log.Logger.With().Stringer("chain", cfg.ChainID).Logger(),
		errCounter:         m.GetCounterVec(metrics.BlockScanError(cfg.ChainID)),
		db:                 storage,
		m:                  m,
		solRpc:             rpc,
		pubKeyMgr:          pubkeyMgr,
		bridge:             bridge,
		solvencyReporter:   solvencyReporter,
		signerCacheManager: signerCacheManager,
		stopChan:           stopChan,
		healthy:            &atomic.Bool{},
		initialHeight:      make(map[string]struct{}),
		vaultScanStatuses:  map[string]vaultScanStatus{},
	}, nil
}

func (s *SOLScanner) Start() {
	s.lastMimirCheck = time.Now().Add(-constants.ThorchainBlockTime)
	s.isChainPaused = false

	initialSlot, ok := waitForInitialSlot(s.solRpc, s.stopChan, s.cfg.Solana.ScanInterval, s.logger)
	if !ok {
		s.logger.Info().Msg("stopping SOL scanner")
		return
	}

	lastHeight, err := s.db.GetScanPos()
	// if err is ErrNotFound, then we set to 0.
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		s.logger.Error().Err(err).Msg("failed to get last scan pos")
		return
	}
	// FloorBlockHeight prevents the scanner from ever resuming below this slot.
	// Bump lastHeight forward and persist so subsequent restarts honour the floor.
	if s.cfg.FloorBlockHeight > 0 && uint64(s.cfg.FloorBlockHeight) > lastHeight {
		s.logger.Info().
			Uint64("floor", uint64(s.cfg.FloorBlockHeight)).
			Uint64("previous", lastHeight).
			Msg("advancing SOL scanner to configured floor block height")
		lastHeight = uint64(s.cfg.FloorBlockHeight)
		if err := s.db.SetScanPos(lastHeight); err != nil {
			s.logger.Error().Err(err).Msg("failed to persist floor block height")
		}
	}
	s.setLastHeight(lastHeight)

	// Anchor startup near the tip, but retain this boundary on failure instead of
	// replaying from zero or silently skipping it when the tip advances.
	if err := s.initializeNetworkFee(initialSlot); err != nil {
		s.logger.Error().Err(err).Msg("failed to check recent blocks and update network fee")
	}

	for {
		select {
		case <-s.stopChan:
			s.logger.Info().Msg("stopping SOL scanner")
			return
		default:
			s.scan()
		}
	}
}

// scan is the main scanning loop for the SOLScanner. It will check if the chain is paused, get the current slot,
// and then scan for new transactions in the vaults. It will also update the network fee if necessary.min
func (s *SOLScanner) scan() {
	if time.Since(s.lastMimirCheck) >= constants.ThorchainBlockTime {
		s.isChainPaused = blockscanner.IsChainPaused(s.cfg, s.logger, s.bridge)
		s.lastMimirCheck = time.Now()
	}

	if s.isChainPaused {
		s.healthy.Store(false)
		time.Sleep(constants.ThorchainBlockTime)
		return
	}

	var vaultAddrs []string
	for _, vault := range s.pubKeyMgr.GetAlgoPubKeys(common.SigningAlgoEd25519, s.cfg.Solana.ScanInactiveVaults) {
		vaultAddrSol, err := vault.GetAddress(common.SOLChain)
		if err != nil {
			s.logger.Error().Err(err).Msg("failed to get vault address")
			continue
		}
		vaultAddrs = append(vaultAddrs, vaultAddrSol.String())
	}

	currentSlot, err := s.solRpc.GetSlot()
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to get current slot")
		time.Sleep(time.Second)
		return
	}

	// Only one fee boundary per pass so catch-up cannot starve vault scanning.
	if err = s.updateNetworkFee(currentSlot); err != nil {
		s.logger.Error().Err(err).Msg("failed to check recent blocks and update network fee")
	}

	lastSlot := s.getLastHeight()

	// skip if rpc slot is behind last scanned slot
	if currentSlot <= lastSlot {
		time.Sleep(s.cfg.Solana.ScanInterval)
		return
	}

	// coalesce scan to configured scan interval
	solanaBlockTime := time.Duration(common.SOLChain.ApproximateBlockMilliseconds()) * time.Millisecond
	lag := time.Duration(currentSlot-lastSlot) * solanaBlockTime
	if lag < s.cfg.Solana.ScanInterval {
		time.Sleep(s.cfg.Solana.ScanInterval)
		return
	}

	var wg sync.WaitGroup
	wg.Add(len(vaultAddrs))

	healthy := true
	var mu sync.Mutex
	setUnhealthy := func() {
		mu.Lock()
		defer mu.Unlock()
		healthy = false
	}

	newLastSigs := make([]string, len(vaultAddrs))
	successfulQueries := make([]bool, len(vaultAddrs))
	countTxs := make([]int, len(vaultAddrs))
	vaultLastSigs := make([]string, len(vaultAddrs))
	for i, vaultAddr := range vaultAddrs {
		var lastSig string
		var vaultLastSlot uint64
		if vss, ok := s.vaultScanStatuses[vaultAddr]; ok {
			lastSig = vss.lastSig
			vaultLastSlot = vss.lastSlot
		} else {
			lastSig, vaultLastSlot, err = s.db.GetScanStatus(vaultAddr)
			// if err is ErrNotFound, then we set to "" and 0.
			if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
				s.logger.Error().Err(err).Str("vault", vaultAddr).Msg("failed to get vault scan status from db")
				setUnhealthy()
				wg.Done()
				continue
			}
			s.vaultScanStatuses[vaultAddr] = vaultScanStatus{
				lastSig:  lastSig,
				lastSlot: vaultLastSlot,
			}
		}
		vaultLastSigs[i] = lastSig

		var minContextSlot uint64
		var queryLastSig string
		if _, ok := s.initialHeight[vaultAddr]; ok || s.cfg.StartBlockHeight == 0 {
			minContextSlot = vaultLastSlot
			queryLastSig = lastSig
		} else {
			minContextSlot = uint64(s.cfg.StartBlockHeight)
			s.logger.Info().
				Str("vault", vaultAddr).
				Uint64("minContextSlot", minContextSlot).
				Msg("Initial height is set, querying all rpc node tx history")
			queryLastSig = ""
			// NOTE: this can observe txs that we already observed in the past,
			// because minContextSlot does not set a minimum bound for the query,
			// it only ensures that minContextSlot is in the history on the rpc node.
		}

		// FloorBlockHeight: never query a vault below the configured floor. If the
		// resolved minContextSlot is below the floor, advance to the floor and
		// clear the cursor since any cached lastSig predates the floor.
		if s.cfg.FloorBlockHeight > 0 && uint64(s.cfg.FloorBlockHeight) > minContextSlot {
			s.logger.Info().
				Str("vault", vaultAddr).
				Uint64("floor", uint64(s.cfg.FloorBlockHeight)).
				Uint64("previous", minContextSlot).
				Msg("advancing vault scanner to configured floor block height")
			minContextSlot = uint64(s.cfg.FloorBlockHeight)
			queryLastSig = ""
		}

		go s.scanVault(vaultAddr, i, queryLastSig, minContextSlot, setUnhealthy, countTxs, successfulQueries, newLastSigs, &wg)
	}
	wg.Wait()

	s.healthy.Store(healthy)

	for i, vaultAddr := range vaultAddrs {
		txCount := countTxs[i]

		// increment tx counts for this scan iteration and also since last log.
		s.txCount += txCount

		if successfulQueries[i] {
			// we are up to date as of the current slot, so minContextSlot
			// can be the current slot for the next call

			// since tx query was successful, we want to update last scanned slot for this vault. We may or may not have queried new txs.
			// If no txs were queried, we want to retain the last sig from the db.
			var lastSig string
			if txCount == 0 {
				lastSig = vaultLastSigs[i]
			} else {
				lastSig = newLastSigs[i]
			}

			// we have not scanned this vault before, so we mark it as such so that
			// the cfg.StartBlockHeight is only used for the first successful query.
			if _, ok := s.initialHeight[vaultAddr]; !ok {
				s.initialHeight[vaultAddr] = struct{}{}
			}

			s.vaultScanStatuses[vaultAddr] = vaultScanStatus{
				lastSig:  lastSig,
				lastSlot: currentSlot,
			}

			// update the db in a goroutine, we don't need to block here
			go func() {
				if scanStatusErr := s.db.SetScanStatus(vaultAddr, lastSig, currentSlot); scanStatusErr != nil {
					s.logger.Error().Err(scanStatusErr).Str("vault", vaultAddr).Msg("failed to set vault scan status")
				}
			}()
		}
	}

	// Record the slot being scanned before reporting so ReportSolvency can
	// distinguish this scan-driven call from a SolvencyCheckRunner call (which
	// passes the live chain tip). Without this, the unhealthy-scanner guard in
	// ReportSolvency never matches and a lagging/unhealthy scanner keeps
	// reporting where it should defer to the runner.
	s.currentScanSlot.Store(int64(currentSlot))
	if err = s.solvencyReporter(int64(currentSlot)); err != nil {
		s.logger.Err(err).Msg("fail to send solvency to THORChain")
	}

	if (time.Since(s.lastLog) > (5 * time.Second)) || !healthy {
		s.lastLog = time.Now()
		s.logger.Info().
			Uint64("block height", currentSlot).
			Int("gap", 0).
			Int("txs", s.txCount).
			Bool("healthy", healthy).
			Msg("scan block")
		s.txCount = 0
	}

	if lastSlot > 0 {
		s.m.GetCounter(metrics.TotalBlockScanned).Add(float64(currentSlot - lastSlot))
	}

	s.setLastHeight(currentSlot)

	// update the db in a goroutine, we don't need to block here
	go func() {
		if err := s.db.SetScanPos(currentSlot); err != nil {
			s.logger.Error().Err(err).Msg("failed to set scan position")
		}
	}()
}

// scanVault scans a vault for new transactions. It will query the RPC node for the vault's
// address and get any transactions involving the address, starting from most recent until queryLastSig.
// If queryLastSig is empty, it will query all transactions for the vault address that exist on the node.
// minContextSlot is the minimum slot to query for transactions. It does not set a bound on the query,
// but does ensure that history exists on the RPC node until at least this slot.
func (s *SOLScanner) scanVault(
	vaultAddr string,
	i int,
	queryLastSig string,
	minContextSlot uint64,
	setUnhealthy func(),
	txCounts []int,
	successfulQueries []bool,
	newLastSigs []string,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	var totalTxs int
	var newestSig string
	before := ""
	for {
		params := rpc.GetSignaturesForAddressUntilParams{
			Commitment: "finalized",
			Before:     before,
			Until:      queryLastSig,
			Limit:      transactionQueryLimit,
		}
		// Only pin minContextSlot when there is no until-cursor. With an until-cursor,
		// minContextSlot tracks the scanner's near-tip position, which for an idle
		// vault (no new tx) can exceed the cursor signature's slot. The RPC then
		// returns -32020 "Transaction not found" trying to resolve a cursor below the
		// required context, which marks the whole scanner unhealthy and halts signing
		// and observation for the chain. The until-cursor already bounds the query, so
		// minContextSlot is redundant once a cursor is set.
		if queryLastSig == "" {
			params.MinContextSlot = minContextSlot
		}
		res, err := s.solRpc.GetSignaturesForAddressUntil(vaultAddr, params)
		if err != nil {
			s.logger.Error().Err(err).Msgf("failed to get signatures for address %s", vaultAddr)
			setUnhealthy()
			return
		}

		if len(res) == 0 {
			if totalTxs == 0 {
				s.logger.Debug().Msgf("no new signatures for address %s", vaultAddr)
			}
			break
		}

		totalTxs += len(res)
		if newestSig == "" {
			newestSig = res[0].Signature
		}

		var eg errgroup.Group
		// limit the number of concurrent tx queries to the RPC node
		eg.SetLimit(maxConcurrentTxQueries)
		for _, sig := range res {
			eg.Go(func() error {
				tx, err := s.solRpc.GetTransaction(sig.Signature)
				if err != nil {
					setUnhealthy()
					return fmt.Errorf("failed to get transaction %s: %w", sig.Signature, err)
				}

				if tx.Meta.Err != nil {
					s.logger.Debug().Msgf("transaction %s failed: %s", sig.Signature, tx.Meta.Err)
					return nil
				}

				txIn, err := s.getTxIn(sig.Slot, []*rpc.TransactionResult{tx})
				if err != nil {
					// Leave the cursor where it is: the scan fails and this tx is re-read.
					return fmt.Errorf("failed to observe transaction %s: %w", sig.Signature, err)
				}
				s.globalTxsQueue <- txIn
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			setUnhealthy()
			s.logger.Error().Err(err).Msgf("failed to process transactions")
			return
		}

		if len(res) < transactionQueryLimit {
			break
		}
		before = res[len(res)-1].Signature
	}

	successfulQueries[i] = true
	txCounts[i] = totalTxs
	if totalTxs > 0 {
		newLastSigs[i] = newestSig
	}
}

func (s *SOLScanner) initializeNetworkFee(initialSlot uint64) error {
	interval := s.cfg.Solana.FeeUpdateInterval
	if interval == 0 {
		return errors.New("SOL fee update interval must be positive")
	}
	if initialSlot > interval {
		slot := ((initialSlot - 1) / interval) * interval
		s.netFeePendingSlot = &slot
	}
	return s.updateNetworkFee(initialSlot)
}

func (s *SOLScanner) updateNetworkFee(currentSlot uint64) error {
	interval := s.cfg.Solana.FeeUpdateInterval
	if interval == 0 {
		return errors.New("SOL fee update interval must be positive")
	}
	if currentSlot == 0 {
		return nil
	}
	latestMultiple := (currentSlot - 1) / interval
	var slot uint64
	if s.netFeePendingSlot != nil {
		slot = *s.netFeePendingSlot
		if currentSlot <= slot {
			return nil
		}
	} else {
		// Division first avoids overflow and waits until the boundary is crossed.
		if s.netFeeSlotMultiple >= latestMultiple {
			return nil
		}
		slot = (s.netFeeSlotMultiple + 1) * interval
	}
	// Retain only the latest crossed boundary and its predecessor. An archive
	// RPC must not cause a long outage to replay hours-old fee observations.
	if latestMultiple > 1 && slot/interval < latestMultiple-1 {
		nextSlot := (latestMultiple - 1) * interval
		s.logger.Warn().Uint64("first_skipped_slot", slot).
			Uint64("last_skipped_slot", nextSlot-interval).
			Uint64("next_slot", nextSlot).Msg("bounding SOL network fee catch-up")
		slot = nextSlot
		s.netFeePendingSlot = &nextSlot
	}
	if err := s.checkRecentBlocksAndUpdateNetworkFee(slot); err != nil {
		select {
		case <-s.stopChan:
			return err
		default:
		}
		latestSlot := latestMultiple * interval
		if slot < latestSlot && (errors.Is(err, errNoSOLFeeSampleBlocks) || errors.Is(err, errSOLFeeSampleUnavailable)) {
			// A retention floor is evidence of lost history; an RPC error or an
			// empty listing alone is not. Re-anchor in one step, but do not mark
			// the new boundary complete until its own sample succeeds.
			firstSlot := uint64(0)
			if slot > s.cfg.Solana.FeeSampleSlots {
				firstSlot = slot - s.cfg.Solana.FeeSampleSlots
			}
			floor, floorErr := s.solRpc.GetFirstAvailableBlock()
			if floorErr == nil && floor > firstSlot {
				s.netFeePendingSlot = &latestSlot
				s.logger.Warn().Uint64("slot", slot).Uint64("retention_floor", floor).
					Uint64("next_slot", latestSlot).Msg("re-anchoring SOL fee sampling past pruned history")
				return err
			}
		}
		if !errors.Is(err, errNoSOLFeeSampleBlocks) {
			return err
		}
		// A complete empty listing has no fee to report. It must not wedge
		// subsequent boundaries or relabel a previous sample as current.
	}
	s.netFeeSlotMultiple = slot / interval
	s.netFeePendingSlot = nil
	return nil
}

// Sample only this boundary's finalized lookback. GasCacheBlocks remains the SOL
// eligible-transaction target, not a rolling block-cache length. A fully fetched
// low-volume window is usable even when it does not reach that target.
func (s *SOLScanner) checkRecentBlocksAndUpdateNetworkFee(slot uint64) error {
	if s.cfg.Solana.FeeSampleSlots == 0 || s.cfg.GasCacheBlocks <= 0 || s.cfg.GasPriceResolution <= 0 {
		return errors.New("SOL fee sample slots, transaction target and price resolution must be positive")
	}
	if slot > math.MaxInt64 {
		return errors.New("SOL network fee slot exceeds int64")
	}
	select {
	case <-s.stopChan:
		return errors.New("SOL network fee sampling stopped")
	default:
	}

	var firstSlot uint64
	if slot > s.cfg.Solana.FeeSampleSlots {
		firstSlot = slot - s.cfg.Solana.FeeSampleSlots
	}

	// getSlot and getBlocks may hit different backends; requiring the boundary as
	// the minimum context slot rejects a listing clamped to a lagging root.
	blocks, err := s.solRpc.GetBlockHeightsWithMinContextSlot(firstSlot, slot, slot)
	if err != nil {
		return fmt.Errorf("%w: failed to get finalized blocks for slots %d-%d: %w", errSOLFeeSampleUnavailable, firstSlot, slot, err)
	}
	if blocks == nil {
		return fmt.Errorf("%w: missing finalized block listing for slots %d-%d", errSOLFeeSampleUnavailable, firstSlot, slot)
	}
	if len(blocks) == 0 {
		if err := s.checkNetworkFeeHistory(firstSlot); err != nil {
			return err
		}
		return fmt.Errorf("%w at slots %d-%d", errNoSOLFeeSampleBlocks, firstSlot, slot)
	}
	slices.Sort(blocks)
	for i, b := range blocks {
		if b < firstSlot || b > slot || (i > 0 && b == blocks[i-1]) {
			return fmt.Errorf("invalid finalized block listing at slot %d for range %d-%d", b, firstSlot, slot)
		}
	}
	feeRates := make([]uint64, 0, len(blocks))
	remainingTxs := s.cfg.GasCacheBlocks
	for i := len(blocks) - 1; i >= 0; i-- {
		select {
		case <-s.stopChan:
			return errors.New("SOL network fee sampling stopped")
		default:
		}
		b := blocks[i]
		block, err := s.solRpc.GetBlock(b)
		if err != nil {
			return fmt.Errorf("%w: failed to get finalized block %d: %w", errSOLFeeSampleUnavailable, b, err)
		}
		// Real skipped slots are absent from getBlocks. A listed block that is
		// missing cannot safely be dropped from this boundary's sample.
		if block == nil || block.Transactions == nil {
			return fmt.Errorf("%w: finalized block %d missing", errSOLFeeSampleUnavailable, b)
		}
		for _, txn := range block.Transactions {
			if txn == nil {
				return fmt.Errorf("null transaction in finalized block %d for SOL fee sample", b)
			}
		}
		feeRate, numTxs := s.calculateFeeRatesFromTransactions(block.Transactions)
		feeRates = append(feeRates, feeRate)
		if numTxs >= remainingTxs {
			remainingTxs = 0
			break
		}
		remainingTxs -= numTxs
	}
	if remainingTxs > 0 {
		// getBlocks may silently omit pruned blocks. A low-count fallback is
		// valid only when the complete requested window remains available.
		if err := s.checkNetworkFeeHistory(firstSlot); err != nil {
			return err
		}
	}
	return s.sendNetworkFeeIfNecessary(slot, feeRates)
}

func (s *SOLScanner) checkNetworkFeeHistory(firstSlot uint64) error {
	select {
	case <-s.stopChan:
		return errors.New("SOL network fee sampling stopped")
	default:
	}
	floor, err := s.solRpc.GetFirstAvailableBlock()
	if err != nil {
		return fmt.Errorf("%w: failed to check retention for slot %d: %w", errSOLFeeSampleUnavailable, firstSlot, err)
	}
	if floor > firstSlot {
		return fmt.Errorf("%w: window starts at slot %d below retention floor %d", errSOLFeeSampleUnavailable, firstSlot, floor)
	}
	return nil
}

// GetHeight returns the current block height of Solana
func (s *SOLScanner) GetHeight() (int64, error) {
	height, err := s.solRpc.GetSlot()
	if err != nil {
		return -1, err
	}
	return int64(height), nil
}

// CurrentScanSlot returns the slot the scan loop is currently processing, set
// just before the solvency reporter is invoked. ReportSolvency uses it to tell
// a scan-driven solvency call apart from a SolvencyCheckRunner call.
func (s *SOLScanner) CurrentScanSlot() int64 {
	return s.currentScanSlot.Load()
}

func (s *SOLScanner) getLastHeight() uint64 {
	s.lastHeightMu.RLock()
	defer s.lastHeightMu.RUnlock()
	return s.lastHeight
}

func (s *SOLScanner) setLastHeight(height uint64) {
	s.lastHeightMu.Lock()
	defer s.lastHeightMu.Unlock()
	s.lastHeight = height
}

// ScanHeight returns the current scanned height
func (s *SOLScanner) ScanHeight() (int64, error) {
	lastHeight := s.getLastHeight()

	if lastHeight > 0 {
		return int64(lastHeight), nil
	}

	// lastHeight not in memory, get from db

	lastSlot, err := s.db.GetScanPos()
	// if err is ErrNotFound, then we set to 0.
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return -1, err
	}
	return int64(lastSlot), nil
}

// ScanHeight returns the current scanned height
func (s *SOLScanner) IsHealthy() bool {
	return s.healthy.Load()
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (s *SOLScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, s.lastFeeRate.Load()
}

func (s *SOLScanner) sendNetworkFeeIfNecessary(height uint64, feeRates []uint64) error {
	if len(feeRates) == 0 || s.cfg.GasPriceResolution <= 0 || height > math.MaxInt64 {
		return errors.New("invalid SOL network fee sample, resolution or height")
	}
	medianFeeRate := calculateMedian(feeRates)
	resolution := uint64(s.cfg.GasPriceResolution)
	remainder := medianFeeRate % resolution
	roundedFeeRate := medianFeeRate - remainder
	if remainder >= resolution-resolution/2 {
		if roundedFeeRate > math.MaxUint64-resolution {
			return errors.New("SOL network fee rounding overflow")
		}
		roundedFeeRate += resolution
	}
	if roundedFeeRate == 0 {
		return errors.New("SOL network fee rounds to zero")
	}
	select {
	case <-s.stopChan:
		return errors.New("SOL network fee reporting stopped")
	default:
	}
	_, agreedFeeRate, err := s.bridge.GetNetworkFee(common.SOLChain)
	if err != nil {
		return fmt.Errorf("failed to get agreed SOL network fee: %w", err)
	}
	select {
	case <-s.stopChan:
		return errors.New("SOL network fee reporting stopped")
	default:
	}
	// Vote only when the sample leaves the deadband around a fresh agreed fee.
	var diff uint64
	if roundedFeeRate > agreedFeeRate {
		diff = roundedFeeRate - agreedFeeRate
	} else {
		diff = agreedFeeRate - roundedFeeRate
	}
	publish := agreedFeeRate == 0 || diff/resolution >= solFeeReportDeadbandSteps
	if !publish && roundedFeeRate == agreedFeeRate {
		// A node that reads this boundary's own quorum result must still attest:
		// THORNode charged it LackOfObservationPenalty as a non-signer and refunds
		// only late signers within ObservationDelayFlexibility. Observed txs also
		// set this height, so require an exact match.
		lastSlot, err := s.bridge.GetLastObservedInHeight(common.SOLChain)
		if err != nil {
			return fmt.Errorf("failed to get last agreed SOL height: %w", err)
		}
		publish = lastSlot == int64(height)
	}
	if publish {
		select {
		case <-s.stopChan:
			return errors.New("SOL network fee reporting stopped")
		case s.globalNetworkFeeQueue <- common.NetworkFee{
			Chain: common.SOLChain, Height: int64(height), TransactionSize: 1, TransactionRate: roundedFeeRate,
		}:
		}
		s.logger.Info().Uint64("fee", roundedFeeRate).Uint64("height", height).
			Msg("sent network fee to THORChain")
	}
	s.lastFeeRate.Store(roundedFeeRate)
	return nil
}

// --------------------------------- extraction ---------------------------------

// Gets the TxIn struct for a block
func (s *SOLScanner) getTxIn(slot uint64, transactions []*rpc.TransactionResult) (stypes.TxIn, error) {
	txIn := stypes.TxIn{
		Chain:                s.cfg.ChainID,
		TxArray:              make([]*stypes.TxInItem, 0),
		MemPool:              false,
		ConfirmationRequired: 1, // Assuming 1 confirmation is required
	}

	for _, txn := range transactions {
		// Skip transactions with no signatures or no fee
		if len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		txItem, err := s.txInItem(txn, int64(slot))
		if err != nil {
			return stypes.TxIn{}, err
		}
		if txItem == nil {
			continue
		}

		txIn.TxArray = append(txIn.TxArray, txItem)
	}

	return txIn, nil
}

// Calculates the median of a slice of uint64.
// Makes a copy to avoid mutating the original slice.
func calculateMedian(arr []uint64) uint64 {
	sorted := make([]uint64, len(arr))
	copy(sorted, arr)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	arr = sorted
	n := len(arr)
	if n == 0 {
		return 0
	}
	if n%2 == 0 {
		return arr[n/2-1] + (arr[n/2]-arr[n/2-1])/2
	}
	return arr[n/2]
}

// calculateFeeRatesFromTransactions extracts fee rates from transactions for network fee calculation.
// Unlike getTxIn, this does not validate transactions or filter for THORChain inbounds.
// It processes ALL transactions in the block to get an accurate network fee measurement.
func (s *SOLScanner) calculateFeeRatesFromTransactions(transactions []*rpc.TransactionResult) (uint64, int) {
	txFeeRates := []uint64{}

	for _, txn := range transactions {
		// skip transactions with no signatures or no fee
		if txn == nil || len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		// skip failed transactions
		if txn.Meta.Err != nil {
			continue
		}

		// calculate the fee rate (lamports per signature)
		numSigs := uint64(len(txn.Transaction.Signatures))
		feeRate := txn.Meta.Fee / numSigs

		// rate may underestimate in cases like multi-sig transactions, floor to base fee
		if feeRate < baseFeePerSigLamports {
			feeRate = baseFeePerSigLamports
		}

		txFeeRates = append(txFeeRates, feeRate)
	}

	if len(txFeeRates) == 0 {
		return baseFeePerSigLamports, 0
	}
	return calculateMedian(txFeeRates), len(txFeeRates)
}

// Create a TxInItem for a SOL transfer transaction. This function will only return a
// TxInItem if the transaction contains exactly one SOL transfer instruction to the vault.
// Otherwise, it will return nil.
func (s *SOLScanner) getTxInItem(txn *rpc.TransactionResult, blockHeight int64) *stypes.TxInItem {
	item, _ := s.txInItem(txn, blockHeight)
	return item
}

// txInItem is getTxInItem that also reports a transient RPC failure that must not
// drop the observation: the caller fails the scan so the cursor is not advanced
// and the transaction is re-read on the next pass.
func (s *SOLScanner) txInItem(txn *rpc.TransactionResult, blockHeight int64) (*stypes.TxInItem, error) {
	memo := ""
	var transferCount int
	var fromAccount, toAccount string
	// advancedNonceAcct is the account an AdvanceNonceAccount instruction advanced,
	// if any; attested below when it is the sender vault's own nonce account.
	var advancedNonceAcct string
	var amountMoved uint64
	fee := txn.Meta.Fee

	instructions := txn.Transaction.GetInstructions()

	// Resolve instruction account indices against the full account list: the
	// static account keys followed by any addresses loaded from address lookup
	// tables (versioned/v0+ transactions). Legacy transactions carry no loaded
	// addresses, so this is identical to the static account keys for them.
	accounts := txn.Transaction.ResolvedAccounts(txn.Meta.LoadedAddresses)
	accountByIndex := func(i int) string {
		if i < 0 || i >= len(accounts) {
			return ""
		}
		return accounts[i]
	}

	// A bare durable-nonce account creation (rent into the vault's fresh nonce
	// account, no transfer, no memo) is observed as its own shape so THORChain can book
	// the rent and fee as vault gas and commit the vault's initial nonce. The classifier
	// is strict enough that a real outbound (which always carries a transfer) can never
	// match, so it can never swallow a genuine outbound observation.
	if isDurableNonceCreateTx(instructions, accountByIndex) {
		return s.nonceCreateTxInItem(txn, blockHeight, instructions, accountByIndex)
	}

	for _, instruction := range instructions {
		programID := accountByIndex(instruction.ProgramIdIndex)

		// Handle memo
		if types.MemoProgramID.EqualsString(programID) {
			memoBytes, err := base58.Decode(instruction.Data)
			if err == nil {
				memo = string(memoBytes)
			} else {
				s.logger.Error().Err(err).Str("memoData", instruction.Data).Msg("Failed to decode memo")
			}
			continue
		}

		// Skip non-SystemProgram instructions
		if !types.SystemProgramID.EqualsString(programID) {
			continue
		}

		// Decode instruction data
		data, err := base58.Decode(instruction.Data)
		if err != nil {
			s.logger.Error().Err(err).Str("data", instruction.Data).Msg("Failed to decode instruction data")
			continue
		}

		// AdvanceNonceAccount (u32 LE 4): accounts[0] is the nonce account.
		if len(data) >= 4 && binary.LittleEndian.Uint32(data[:4]) == solSysAdvanceNonce && len(instruction.Accounts) >= 1 {
			advancedNonceAcct = accountByIndex(instruction.Accounts[0])
			continue
		}

		// Check for transfer instruction
		if len(data) >= 1 && data[0] == 2 { // `2` indicates a System Program transfer instruction
			transferCount++
			if transferCount > 1 {
				s.logger.Warn().
					Int("transferCount", transferCount).
					Str("txID", txn.Transaction.Signatures[0]).
					Msg("Rejected transaction with multiple transfer instructions")
				return nil, nil
			}

			if len(instruction.Accounts) < 2 {
				s.logger.Debug().Msg("Instruction does not contain enough accounts for transfer")
				return nil, nil
			}

			fromIdx := instruction.Accounts[0]
			toIdx := instruction.Accounts[1]
			// Indices may reference addresses loaded from address lookup tables
			// (versioned txs); accounts (built above) already includes them.
			if fromIdx < 0 || toIdx < 0 || fromIdx >= len(accounts) || toIdx >= len(accounts) {
				s.logger.Debug().Msg("Account index out of range")
				return nil, nil
			}
			fromAccount = accounts[fromIdx]
			toAccount = accounts[toIdx]

			if len(data) < 12 {
				s.logger.Debug().Msg("Transfer instruction data too short")
				return nil, nil
			}
			amountMoved = binary.LittleEndian.Uint64(data[4:12])
		}
	}

	// Skip if no valid transfer was found or amount is zero
	if transferCount == 0 || amountMoved == 0 {
		s.logger.Debug().
			Str("txID", txn.Transaction.Signatures[0]).
			Msg("No valid transfer found or amount is zero")
		return nil, nil
	}

	solAmount := convertLamportsToTHORChain(amountMoved)
	if solAmount.LT(s.cfg.ChainID.DustThreshold()) {
		s.logger.Debug().
			Str("txID", txn.Transaction.Signatures[0]).
			Str("amount", solAmount.String()).
			Str("dustThreshold", s.cfg.ChainID.DustThreshold().String()).
			Msg("Transfer amount below dust threshold")
		return nil, nil
	}

	solCoin := common.NewCoin(common.SOLAsset, solAmount)
	gasCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(fee))
	solCoin.Decimals = 9
	gasCoin.Decimals = 9

	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txn.Transaction.Signatures[0],
		Memo:        memo,
		Sender:      fromAccount,
		To:          toAccount,
		Coins:       []common.Coin{solCoin},
		Gas:         common.Gas{gasCoin},
		SolNonce:    s.observedSolNonce(advancedNonceAcct, fromAccount, blockHeight, txn.Transaction.Signatures[0]),
	}, nil
}

// observedSolNonce returns the post-tx durable-nonce state to attest with this
// observation when the tx advanced the SENDING vault's own seed-derived nonce
// account (authority == sender), or nil. THORChain commits it as the vault's current
// nonce once a supermajority of final observations agree on it exactly.
//
// The read is deterministic across observers even though each reads its own RPC:
// only the vault's TSS can advance its nonce (the authority must sign), and the
// vault signs durable txs only with the value committed on THORChain, which changes
// only when THIS observation is committed. So between this tx's slot and that commit
// no other tx can advance the account, and every honest observer reading at a slot
// >= this one (minContextSlot) sees the same value.
func (s *SOLScanner) observedSolNonce(nonceAcct, sender string, slot int64, txID string) *common.SolNonceUpdate {
	nonce, err := s.readSolNonce(nonceAcct, sender, slot)
	if err != nil {
		s.logger.Warn().Err(err).Str("txID", txID).Str("nonce_account", nonceAcct).
			Msg("fail to read advanced durable nonce; observing without it")
		return nil
	}
	return nonce
}

// readSolNonce reads sender's own seed-derived nonce account at slot. It returns
// (nil, nil) when the account is not sender's own nonce account (wrong address or
// authority), and an error only when the RPC read itself failed.
func (s *SOLScanner) readSolNonce(nonceAcct, sender string, slot int64) (*common.SolNonceUpdate, error) {
	if nonceAcct == "" || sender == "" || slot <= 0 || s.solRpc == nil {
		return nil, nil
	}
	expected, err := common.SolNonceAccountAddress(sender)
	if err != nil || expected != nonceAcct {
		return nil, nil
	}
	nonce, authority, err := s.solRpc.GetNonceAccount(nonceAcct, uint64(slot))
	if err != nil {
		return nil, err
	}
	if authority != sender {
		return nil, nil
	}
	return &common.SolNonceUpdate{NonceAccount: nonceAcct, Nonce: nonce, Slot: slot}, nil
}

// SystemProgram instruction discriminators (u32 little-endian, first 4 data bytes).
const (
	solSysCreateAccount         = 0
	solSysTransfer              = 2
	solSysCreateAccountWithSeed = 3
	solSysAdvanceNonce          = 4
	solSysInitializeNonce       = 6
	solSysAllocateWithSeed      = 9
)

// nonceCreateTxInItem observes a vault's durable-nonce account creation (see
// isDurableNonceCreateTx): the funding vault sends the rent into its nonce account.
// THORChain books the rent and fee as vault gas and commits the vault's initial
// durable nonce from it. It is observed ONLY when the created account is the funding
// vault's own seed-derived nonce account and its post-creation nonce (authority ==
// funder) can be read; otherwise it is not observed at all, because without an
// attested nonce it would reach THORChain as an unmatched vault outflow.
func (s *SOLScanner) nonceCreateTxInItem(txn *rpc.TransactionResult, blockHeight int64, instructions []rpc.RPCInstruction, accountByIndex func(int) string) (*stypes.TxInItem, error) {
	var funder, created string
	var rent uint64
	creates := 0
	for _, ins := range instructions {
		data, err := base58.Decode(ins.Data)
		if err != nil || len(data) < 4 || len(ins.Accounts) < 2 {
			continue
		}
		switch binary.LittleEndian.Uint32(data[:4]) {
		case solSysCreateAccountWithSeed:
			// disc | base(32) | seed len(u64) | seed | lamports(u64) | space(u64) | owner(32)
			if len(data) < 44 {
				return nil, nil
			}
			seedLen := binary.LittleEndian.Uint64(data[36:44])
			if seedLen > types.MaxSeedLength || len(data) < 44+int(seedLen)+8 {
				return nil, nil
			}
			off := 44 + int(seedLen)
			rent = binary.LittleEndian.Uint64(data[off : off+8])
		case solSysCreateAccount:
			// disc | lamports(u64) | space(u64) | owner(32)
			if len(data) < 12 {
				return nil, nil
			}
			rent = binary.LittleEndian.Uint64(data[4:12])
		case solSysTransfer:
			// the adopt shape's funding transfer: disc | lamports(u64)
			if len(data) < 12 {
				return nil, nil
			}
			rent = binary.LittleEndian.Uint64(data[4:12])
		default:
			continue
		}
		funder = accountByIndex(ins.Accounts[0])
		created = accountByIndex(ins.Accounts[1])
		creates++
	}
	// Exactly one funding instruction (the create, or the adopt shape's transfer): a
	// second would move rent this item does not book.
	if creates != 1 || funder == "" || created == "" || rent == 0 || len(txn.Transaction.Signatures) == 0 {
		return nil, nil
	}
	txID := txn.Transaction.Signatures[0]
	nonce, err := s.readSolNonce(created, funder, blockHeight)
	if err != nil {
		// A transient read failure must not drop the creation: without it the vault's
		// nonce never commits and its rent is never booked. Fail the scan instead.
		return nil, fmt.Errorf("fail to read durable nonce of created account %s: %w", created, err)
	}
	if nonce == nil {
		s.logger.Warn().Str("txID", txID).Str("nonce_account", created).
			Msg("not observing durable-nonce account creation: not the funder's own nonce account, or its nonce is unreadable")
		return nil, nil
	}
	rentCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(rent))
	gasCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(txn.Meta.Fee))
	rentCoin.Decimals = 9
	gasCoin.Decimals = 9
	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txID,
		Memo:        "",
		Sender:      funder,
		To:          created,
		Coins:       []common.Coin{rentCoin},
		Gas:         common.Gas{gasCoin},
		SolNonce:    nonce,
	}, nil
}

// isDurableNonceCreateTx reports whether the transaction is EXACTLY a durable-nonce
// account creation: only SystemProgram CreateAccount(WithSeed) + InitializeNonceAccount
// instructions, where the created account IS the initialized nonce account, with NO
// transfer, NO memo, and no other program's instructions. Such a tx moves only rent
// into a fresh network-owned nonce account and carries no observable outbound.
//
// STRICT by construction so it can NEVER match a real outbound: any SystemProgram
// Transfer (disc 2), any memo, any non-SystemProgram instruction, any other system
// instruction, a missing create or initialize, or a mismatch between the created and
// initialized account each make it return false — the tx is then observed normally.
func isDurableNonceCreateTx(instructions []rpc.RPCInstruction, accountByIndex func(int) string) bool {
	createdAccount, nonceAccount, allocated, transferTo := -1, -1, -1, -1
	creates, allocates, transfers, inits := 0, 0, 0, 0
	for _, ins := range instructions {
		programID := accountByIndex(ins.ProgramIdIndex)
		if types.MemoProgramID.EqualsString(programID) {
			return false // a memo -> not a bare create-nonce tx
		}
		if !types.SystemProgramID.EqualsString(programID) {
			return false // any non-SystemProgram instruction
		}
		data, err := base58.Decode(ins.Data)
		if err != nil || len(data) < 4 {
			return false
		}
		switch binary.LittleEndian.Uint32(data[:4]) {
		case solSysCreateAccount, solSysCreateAccountWithSeed:
			// accounts: [funder, created, (base)] — the created account is index 1.
			if len(ins.Accounts) < 2 {
				return false
			}
			createdAccount = ins.Accounts[1]
			creates++
		case solSysAllocateWithSeed:
			// accounts: [allocated, base] — a pre-funded address adopted in place.
			if len(ins.Accounts) < 1 {
				return false
			}
			allocated = ins.Accounts[0]
			allocates++
		case solSysTransfer:
			// Only the adopt shape funds the account with a transfer.
			if len(ins.Accounts) < 2 {
				return false
			}
			transferTo = ins.Accounts[1]
			transfers++
		case solSysInitializeNonce:
			// accounts: [nonce, recentblockhashes sysvar, rent sysvar].
			if len(ins.Accounts) < 1 {
				return false
			}
			nonceAccount = ins.Accounts[0]
			inits++
		default:
			return false // any other system instruction -> not this shape
		}
	}
	if inits != 1 || nonceAccount < 0 {
		return false
	}
	switch {
	case creates == 1 && allocates == 0 && transfers == 0:
		return createdAccount == nonceAccount
	case creates == 0 && allocates == 1 && transfers == 1:
		// the transfer must fund the adopted account, nothing else
		return allocated == nonceAccount && transferTo == nonceAccount
	default:
		return false
	}
}

// --------------------------------- helpers ---------------------------------

// Function to convert lamports to sdk.Uint
func convertLamportsToTHORChain(lamports uint64) sdk.Uint {
	lamportsBig := new(big.Int).SetUint64(lamports)
	return sdk.NewUintFromBigInt(lamportsBig.Quo(lamportsBig, big.NewInt(10)))
}
