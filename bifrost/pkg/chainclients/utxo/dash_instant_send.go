package utxo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	// used only in regtests and simulations
	priceDashUSDForSimulationTesting = 50

	DashInstantSendEnabledMimirKey      = "DashInstantSendEnabled"
	DashInstantSendUSDThresholdMimirKey = "DashInstantSendUSDThreshold"
)

// updateInstantSendCfg refreshes the Dash InstantSend policy from mimir.
func (c *Client) updateInstantSendCfg() {
	if c.bridge == nil {
		return
	}

	// Load InstantSend Enabled mimir override.
	enabled, err := c.bridge.GetMimir(DashInstantSendEnabledMimirKey)
	if err != nil {
		c.log.Err(err).Msg("fail to get DashInstantSendEnabled mimir")
	} else if enabled >= 0 {
		c.instantSendCfg.Enabled = enabled > 0
	}

	if !c.instantSendCfg.Enabled {
		return
	}

	// Load InstantSend USD threshold mimir override.
	usdThreshold, err := c.bridge.GetMimir(DashInstantSendUSDThresholdMimirKey)
	if err != nil {
		c.log.Err(err).Msg("fail to get DashInstantSendUSDThreshold mimir")
	} else if usdThreshold >= 0 {
		c.instantSendCfg.USDThreshold = uint64(usdThreshold)
	}
}

func feeRateSatPerVByteToMilliSat(feeRate float64) uint64 {
	// analyze-ignore(float-comparison)
	if feeRate <= 0 || math.IsNaN(feeRate) || math.IsInf(feeRate, 0) {
		return 0
	}
	milliSatPerVByte := math.Ceil(feeRate * 1000)
	maxUint64 := ^uint64(0)
	// analyze-ignore(float-comparison)
	if milliSatPerVByte >= float64(maxUint64) {
		return maxUint64
	}
	return uint64(milliSatPerVByte)
}

func formatMilliSatPerVByte(feeRate uint64) string {
	return fmt.Sprintf("%d.%03d", feeRate/1000, feeRate%1000)
}

// allowInstantSend applies our InstantSend policy for mempool observations.
//
// When this returns false, we intentionally do NOT emit the tx via the
// InstantSend mempool path and instead wait for the normal block-based flow
// (which is already gated by `GetHeight()` using `getbestchainlock.height`).
func (c *Client) allowInstantSend(feeRateMilliSatPerVByte uint64, age time.Duration, fromAsgard bool, usdValue cosmos.Uint) (bool, string) {
	// Fee-rate gate: avoid treating low-fee txs as final via InstantSend.
	minFeeRateMilliSatPerVByte := feeRateSatPerVByteToMilliSat(c.instantSendCfg.MinFeeRateSatPerVByte)
	if minFeeRateMilliSatPerVByte > 0 && feeRateMilliSatPerVByte < minFeeRateMilliSatPerVByte {
		return false, fmt.Sprintf(
			"fee rate too low: %s sat/vB < %s sat/vB",
			formatMilliSatPerVByte(feeRateMilliSatPerVByte),
			formatMilliSatPerVByte(minFeeRateMilliSatPerVByte),
		)
	}
	// Mempool-age gate: require the tx to be "fresh" when using InstantSend.
	if c.instantSendCfg.MaxMempoolAge > 0 && age > c.instantSendCfg.MaxMempoolAge {
		return false, fmt.Sprintf("mempool age too high: %v > %v", age, c.instantSendCfg.MaxMempoolAge)
	}
	// Skip high-value checks when sender is Asgard.
	if fromAsgard {
		return true, ""
	}
	// If no threshold is configured, avoid InstantSend for inbound txs.
	if c.instantSendCfg.USDThreshold == 0 {
		return false, "no usd threshold configured"
	}
	// If we can't price the tx, fall back to block-based observation.
	if usdValue.IsZero() {
		return false, "unable to price usd value"
	}
	// Only allow InstantSend for txs strictly below the high-value threshold.
	usdThreshold := cosmos.NewUint(c.instantSendCfg.USDThreshold).MulUint64(common.One)
	if usdValue.GTE(usdThreshold) {
		return false, fmt.Sprintf("usd value %f >= threshold %d", cosmosAmountToFloat(usdValue), c.instantSendCfg.USDThreshold)
	}
	return true, ""
}

// usdValueForDash returns the USD value for the given DASH amount.
//
// TOR is Thornode's USD-denominated derived asset. Its pool is spawned from the
// configured TOR anchor median, so Bifrost should use it instead of selecting a
// single stablecoin pool locally.
func (c *Client) usdValueForDash(amount cosmos.Uint) (cosmos.Uint, error) {
	var priceErr error
	if c.poolMgr != nil {
		value, err := c.poolMgr.GetValue(common.DASHAsset, common.TOR, amount)
		if err == nil && !value.IsZero() {
			return value, nil
		}
		if err != nil {
			priceErr = err
		}
	}
	if common.CurrentChainNetwork == common.MockNet {
		// `amount` is denominated in 1e8 DASH units; return USD in 1e8 units.
		// Example: 1 DASH (1e8) at $50 => 50e8.
		return amount.MulUint64(priceDashUSDForSimulationTesting), nil
	}
	if priceErr != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to price DASH in TOR: %w", priceErr)
	}
	return cosmos.ZeroUint(), errors.New("no TOR pool value available for pricing DASH in USD")
}

// cosmosAmountToFloat converts a 1e8-based cosmos.Uint asset amount into a
// float64 whole-asset value for policy calculations and logging.
func cosmosAmountToFloat(u cosmos.Uint) float64 {
	if u.IsZero() {
		return 0
	}
	num := new(big.Float).SetInt(u.BigInt())
	den := new(big.Float).SetFloat64(float64(common.One))
	f, _ := num.Quo(num, den).Float64()
	return f
}

// instantSendObservationHeight returns the deterministic height used for a
// mempool InstantSend observation. The scanner height passed into
// getInstantSendLocks is node-local, so using it would split attestations across
// observers that saw the same lock at slightly different scan positions.
func (c *Client) instantSendObservationHeight(lock *rpc.DashISLockResult, scanHeight int64) (int64, error) {
	if lock == nil {
		return 0, errors.New("missing instantsend lock")
	}
	cycleHash := strings.TrimSpace(lock.CycleHash)
	if cycleHash == "" {
		return 0, errors.New("missing instantsend cycle hash")
	}
	cycleBlock, err := c.rpc.GetBlockVerbose(cycleHash)
	if err != nil {
		return 0, fmt.Errorf("fail to fetch instantsend cycle block: %w", err)
	}
	if cycleBlock.Height <= 0 {
		return 0, fmt.Errorf("instantsend cycle block has invalid height: %d", cycleBlock.Height)
	}
	// Reject cycle heights outside the scanner's current reorg window so a bad
	// lock payload cannot anchor an InstantSend observation far from local tip.
	currentHeight := c.getCurrentBlockHeight()
	if currentHeight <= 0 {
		currentHeight = scanHeight
	}
	if currentHeight > 0 {
		if cycleBlock.Height > currentHeight {
			return 0, fmt.Errorf("instantsend cycle block height %d is above current height %d", cycleBlock.Height, currentHeight)
		}
		if c.cfg.BlockScanner.MaxReorgRescanBlocks > 0 {
			minHeight := currentHeight - c.cfg.BlockScanner.MaxReorgRescanBlocks
			if minHeight > 0 && cycleBlock.Height < minHeight {
				return 0, fmt.Errorf("instantsend cycle block height %d is below reorg floor %d", cycleBlock.Height, minHeight)
			}
		}
	}
	// Use the lock's network-agreed cycle block, not the scanner's local
	// current height, so all observers sign the same InstantSend observation.
	return cycleBlock.Height, nil
}

// getInstantSendLocks surfaces InstantSend-locked mempool txs as finalised observations.
func (c *Client) getInstantSendLocks(height int64) (types.TxIn, error) {
	// Collect InstantSend-locked transactions from the mempool and surface them
	// as immediately usable observations so they can flow through the pipeline
	// without waiting for the later chainlocked block scan.
	txIn := types.TxIn{
		Chain:                c.GetChain(),
		MemPool:              false, // allow inbound observation even though not yet in a block
		ConfirmationRequired: 0,
	}

	usdPerDash := cosmos.ZeroUint() // remains 0 if USDThreshold is 0 -> we skip USD value checks
	if c.instantSendCfg.USDThreshold > 0 {
		price, err := c.usdValueForDash(cosmos.NewUint(common.One))
		if err != nil || price.IsZero() {
			c.log.Debug().Err(err).Msg("unable to fetch DASH/USD price, falling back to block observation")
		} else {
			usdPerDash = price
		}
	}

	mempool, err := c.rpc.GetRawMempoolVerbose()
	if err != nil {
		return txIn, fmt.Errorf("fail to get verbose mempool: %w", err)
	}

	if err := c.temporalStorage.PruneMempoolTx(func(txid string) bool {
		_, ok := mempool[txid]
		return !ok
	}); err != nil {
		// Drop any tracked mempool txids that are no longer present to avoid
		// skipping rebroadcasts or leaking cache entries indefinitely.
		c.log.Err(err).Msg("fail to prune stale mempool cache")
	}

	maxWorkers := c.cfg.ParallelMempoolScan
	if maxWorkers <= 0 {
		maxWorkers = 5
	}

	g, ctx := errgroup.WithContext(context.Background())
	// Bound concurrent RPC fanout so a large mempool does not overwhelm dashd.
	sem := semaphore.NewWeighted(int64(maxWorkers))
	var mu sync.Mutex

	maxEntries := len(mempool)
	if c.cfg.UTXO.TransactionBatchSize > 0 && c.cfg.UTXO.MaxMempoolBatches > 0 {
		if configuredMax := c.cfg.UTXO.TransactionBatchSize * c.cfg.UTXO.MaxMempoolBatches; configuredMax < maxEntries {
			maxEntries = configuredMax
		}
	}

	type instantSendCandidate struct {
		hash  string
		entry rpc.GetRawMempoolVerboseResult
	}
	// Keep the raw tx with its candidate once the cheap InstantLock flag says
	// it is worth joining the single batched getislocks call below.
	type lockedInstantSendCandidate struct {
		instantSendCandidate
		rawTx *rpc.DashTxRawResult
	}
	type resolvedInstantSendCandidate struct {
		lockedInstantSendCandidate
		observedHeight int64
	}
	candidates := make([]instantSendCandidate, 0, maxEntries)
	for hash, entry := range mempool {
		if len(candidates) >= maxEntries {
			break
		}
		// Reserve the txid before fetching the full raw transaction. This skips
		// already processed InstantSend txs before the expensive RPC and also
		// caps the number of workers spawned for a large mempool.
		if !c.tryAddToMemPoolCache(hash) {
			continue
		}
		candidates = append(candidates, instantSendCandidate{hash: hash, entry: entry})
	}

	// Phase 1 stays parallel: raw tx fetches are the expensive per-tx work, but
	// only txs that advertise InstantLock need the lock payload RPC.
	lockedByIndex := make([]lockedInstantSendCandidate, len(candidates))
	lockedIndexes := make([]bool, len(candidates))
	for idx, candidate := range candidates {
		g.Go(func() error {
			removeFromCache := true
			defer func() {
				if removeFromCache {
					c.removeFromMemPoolCache(candidate.hash)
				}
			}()

			if err := sem.Acquire(ctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			// Re-fetch the full raw tx because the verbose mempool listing only
			// gives summary fields; InstantLock flags live on the tx response.
			rawTx, err := c.rpc.GetRawTransactionVerboseDASH(candidate.hash)
			if err != nil {
				c.log.Err(err).Str("txid", candidate.hash).Msg("fail to fetch instantsend tx")
				return nil
			}

			// Only InstantSend-locked transactions are allowed onto the mempool
			// observation path. Plain mempool txs wait for the chainlocked block path.
			if !rawTx.InstantLock && !rawTx.InstantLockInternal {
				return nil
			}

			mu.Lock()
			lockedByIndex[idx] = lockedInstantSendCandidate{
				instantSendCandidate: candidate,
				rawTx:                rawTx,
			}
			lockedIndexes[idx] = true
			mu.Unlock()
			removeFromCache = false
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		c.log.Err(err).Msg("fail to fetch instantsend txs from mempool")
	}

	lockedCandidates := make([]lockedInstantSendCandidate, 0, len(candidates))
	for idx, locked := range lockedIndexes {
		if locked {
			lockedCandidates = append(lockedCandidates, lockedByIndex[idx])
		}
	}

	if len(lockedCandidates) == 0 {
		return txIn, nil
	}

	// Dash returns getislocks results in input order, so preserve the filtered
	// candidate order and use the same index when validating each lock result.
	lockTxIDs := make([]string, 0, len(lockedCandidates))
	for _, candidate := range lockedCandidates {
		lockTxIDs = append(lockTxIDs, candidate.hash)
	}
	isLocks, err := c.rpc.GetISLocks(lockTxIDs)
	if err != nil {
		c.log.Err(err).Int("tx_count", len(lockTxIDs)).Msg("fail to fetch instantsend locks")
		for _, candidate := range lockedCandidates {
			c.removeFromMemPoolCache(candidate.hash)
		}
		return txIn, nil
	}
	if len(isLocks) != len(lockedCandidates) {
		c.log.Debug().Int("tx_count", len(lockTxIDs)).Int("lock_count", len(isLocks)).Msg("instantsend lock response count mismatch")
		// A count mismatch breaks the input-order contract we rely on, so retry
		// the whole filtered set later rather than pairing locks with wrong txids.
		for _, candidate := range lockedCandidates {
			c.removeFromMemPoolCache(candidate.hash)
		}
		return txIn, nil
	}

	// Resolve each unique cycleHash once per scan; many IS locks can share the
	// same quorum cycle block, so repeating GetBlockVerbose is wasted RPC work.
	resolvedCandidates := make([]resolvedInstantSendCandidate, 0, len(lockedCandidates))
	cycleHeightByHash := make(map[string]int64)
	for idx, candidate := range lockedCandidates {
		lock := isLocks[idx]
		if lock == nil {
			c.log.Debug().Str("txid", candidate.hash).Msg("instantsend lock not available yet")
			c.removeFromMemPoolCache(candidate.hash)
			continue
		}
		if lock.TxID != "" && !strings.EqualFold(lock.TxID, candidate.hash) {
			c.log.Debug().Str("txid", candidate.hash).Str("lock_txid", lock.TxID).Msg("instantsend lock txid mismatch")
			c.removeFromMemPoolCache(candidate.hash)
			continue
		}
		cycleHash := strings.TrimSpace(lock.CycleHash)
		observedHeight, ok := cycleHeightByHash[cycleHash]
		if !ok {
			var err error
			observedHeight, err = c.instantSendObservationHeight(lock, height)
			if err != nil {
				c.log.Debug().Err(err).Str("txid", candidate.hash).Msg("skip instantsend tx without deterministic lock height")
				c.removeFromMemPoolCache(candidate.hash)
				continue
			}
			cycleHeightByHash[cycleHash] = observedHeight
		}
		resolvedCandidates = append(resolvedCandidates, resolvedInstantSendCandidate{
			lockedInstantSendCandidate: candidate,
			observedHeight:             observedHeight,
		})
	}

	if len(resolvedCandidates) == 0 {
		return txIn, nil
	}

	// Phase 2 resumes the normal per-candidate validation with the already
	// fetched raw tx and pre-resolved observation height.
	g, ctx = errgroup.WithContext(context.Background())
	sem = semaphore.NewWeighted(int64(maxWorkers))
	for _, candidate := range resolvedCandidates {
		g.Go(func() error {
			removeFromCache := true
			defer func() {
				if removeFromCache {
					c.removeFromMemPoolCache(candidate.hash)
				}
			}()

			if err := sem.Acquire(ctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			txInItem, err := c.getTxIn(&candidate.rawTx.TxRawResult, candidate.observedHeight, true, nil)
			if err != nil {
				c.log.Debug().Err(err).Str("txid", candidate.hash).Msg("fail to build TxInItem for instantsend")
				return nil
			}
			// Ignore txs that do not decode into an inbound coin movement.
			if txInItem.IsEmpty() || txInItem.Coins.IsEmpty() {
				removeFromCache = false
				return nil
			}
			// Skip if it's not an inbound tx - the destination is not an Asgard address.
			if !c.isAsgardAddress(txInItem.To) {
				removeFromCache = false
				return nil
			}

			// Derive policy inputs from the mempool entry: effective fee rate and
			// how long the tx has been sitting in the mempool.
			feeRateMilliSatPerVByte := candidate.entry.FeeRateMilliSatPerVByte()
			age := time.Duration(0)
			if candidate.entry.Time > 0 {
				age = time.Since(time.Unix(candidate.entry.Time, 0))
			}

			fromAsgard := c.isAsgardAddress(txInItem.Sender)
			usdValue := cosmos.ZeroUint()
			if !usdPerDash.IsZero() {
				// getTxIn() has already normalised the inbound amount into txInItem.Coins[0].
				// Convert that DASH amount into USD so the high-value threshold can be enforced.
				usdValue = txInItem.Coins[0].Amount.Mul(usdPerDash).QuoUint64(common.One)
			}

			// Enforce InstantSend policy: high-value / fee / age failures fall
			// back to the normal chainlocked block path instead.
			allow, reason := c.allowInstantSend(feeRateMilliSatPerVByte, age, fromAsgard, usdValue)
			if !allow {
				c.log.Info().Str("txid", candidate.hash).Str("reason", reason).Msg("instantsend tx did not meet policy; waiting for block")
				return nil
			}

			// Mark as observed to avoid duplicate reporting once it later lands
			// in a chainlocked block and the normal block scanner sees it.
			added, err := c.temporalStorage.TrackObservedTx(txInItem.Tx)
			if err != nil {
				c.log.Err(err).Str("txid", candidate.hash).Msg("fail to track observed instantsend tx")
				return nil
			}
			// Another path already recorded this tx, so do not emit it again.
			if !added {
				removeFromCache = false
				return nil
			}

			if c.m != nil {
				if counter := c.m.GetCounter(metrics.DashInstantSendObserved); counter != nil {
					counter.Inc()
				}
			}

			// Multiple workers can accept txs in parallel, so guard the shared output slice.
			mu.Lock()
			txIn.TxArray = append(txIn.TxArray, &txInItem)
			mu.Unlock()
			removeFromCache = false
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		c.log.Err(err).Msg("fail to process instantsend locks from mempool")
	}

	return txIn, nil
}
