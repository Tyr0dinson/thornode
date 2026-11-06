package utxo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// instantSendHandler owns InstantSend scanning, allowance and recovery state.
// It shares the client's dependencies and must not be copied after first use.
type instantSendHandler struct {
	client  *Client
	backend instantSendBackend
	policy  instantSendPolicy

	// Owned by the single mempool scanner; rebuilt after restart.
	mempoolCursor string
	// Transactions with no vault output, cached only for this vault-address set.
	ignored map[string]struct{}
	vaults  []string
	// Transaction batches awaiting final checks and delivery.
	txQueue []types.TxIn
	// Amount settings are owned by the mempool scanner.
	// Both amounts and their Mimir values use base units (common.One per coin).
	minAmount uint64
	maxAmount uint64

	// Serializes allowance writes, recovery progress, block height, revision and switch updates.
	// Release during RPCs so ChainLock settlement can continue. After relocking,
	// check the revision and keep the lock through the resulting state updates.
	stateMu sync.Mutex
	// Effective switch after applying Mimir; cfg retains the startup value.
	enabled bool
	// Incremented on settlement attempts to reject stale RPC results.
	revision uint64
	recovery instantSendRecovery
	// Last processed block; zero before scanning. Protected by stateMu.
	lastBlockHeight int64
}

const defaultInstantSendMinAmount uint64 = common.One / 100 // 0.01 coin, in base units

var errInstantSendPaused = errors.New("InstantSend observations paused")

// isPaused follows blockscanner.IsChainPaused on successful reads.
// Unlike the shared scanner, it defers final observations if any read fails.
func (h *instantSendHandler) isPaused() bool {
	if h.client.bridge == nil {
		h.client.log.Error().Msg("InstantSend halt bridge unavailable")
		return true
	}
	thorHeight, err := h.client.bridge.GetBlockHeight()
	if err != nil {
		h.client.log.Err(err).Msg("fail to get THORChain height for InstantSend halt check")
		return true
	}
	var height int64
	for _, key := range []string{"Halt" + h.client.GetChain().String() + "Chain", "SolvencyHalt" + h.client.GetChain().String() + "Chain", "HaltChainGlobal"} {
		height, err = h.client.bridge.GetMimir(key)
		if err != nil {
			h.client.log.Err(err).Str("key", key).Msg("fail to read InstantSend halt setting")
			return true
		}
		if height > 0 && thorHeight >= height {
			return true
		}
	}
	height, err = h.client.bridge.GetMimir("NodePauseChainGlobal")
	if err != nil {
		h.client.log.Err(err).Msg("fail to read InstantSend node pause setting")
		return true
	}
	return height > 0 && thorHeight <= height
}

// refreshSettings reads the InstantSend switch and amount policy.
// An unset switch restores the startup default; a failed read disables InstantSend.
func (h *instantSendHandler) refreshSettings() error {
	var enabled int64
	var err error
	if h.client.bridge == nil {
		err = errors.New("InstantSend settings bridge unavailable")
	} else {
		enabled, err = h.client.bridge.GetMimir(h.policy.enabled)
	}

	effectiveEnabled := false
	if err == nil {
		effectiveEnabled = h.client.cfg.InstantSend.Enabled
		if enabled >= 0 {
			effectiveEnabled = enabled > 0
		}
	}
	h.stateMu.Lock()
	h.enabled = effectiveEnabled
	h.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("fail to read InstantSend enabled switch: %w", err)
	}
	minAmount, err := h.client.bridge.GetMimir(h.policy.minimum)
	if err != nil {
		return fmt.Errorf("fail to read InstantSend minimum amount: %w", err)
	}
	maxAmount, err := h.client.bridge.GetMimir(h.policy.maximum)
	if err != nil {
		return fmt.Errorf("fail to read InstantSend maximum amount: %w", err)
	}
	h.minAmount = defaultInstantSendMinAmount
	if minAmount >= 0 {
		h.minAmount = uint64(minAmount)
	}
	h.maxAmount = 0
	if maxAmount > 0 {
		h.maxAmount = uint64(maxAmount)
	}
	return nil
}

// allowAge applies our InstantSend policy for mempool observations.
//
// When this returns false, we intentionally do NOT emit the tx via the
// InstantSend mempool path and instead wait for the normal block-based flow
// (which checks the fetched block's ChainLock).
func (h *instantSendHandler) allowAge(age time.Duration) (bool, string) {
	if h.client.cfg.InstantSend.MaxMempoolAge > 0 && age > h.client.cfg.InstantSend.MaxMempoolAge {
		return false, fmt.Sprintf("mempool age too high: %v > %v", age, h.client.cfg.InstantSend.MaxMempoolAge)
	}
	return true, ""
}

// scan delegates only the native candidate extraction, never allowance ownership.
func (h *instantSendHandler) scan() ([]types.TxIn, error) {
	if h.backend == nil {
		return nil, nil
	}
	return h.backend.scan()
}

// fetchMemPool alternates early vault outbounds and lock-verified inbound batches.
func (h *instantSendHandler) fetchMemPool(height int64) (types.TxIn, error) {
	previousEnabled := h.enabled
	err := h.refreshSettings()
	if err != nil {
		h.client.log.Err(err).Msg("fail to refresh InstantSend settings")
		h.publishAllowance(instantSendAdmission{reason: "settings"})
	}
	if !h.enabled {
		h.txQueue = nil
		h.client.nextMempoolScan = scanOutbounds
		var admission instantSendAdmission
		admission, err = h.prepareAllowance(err)
		if err != nil {
			h.client.log.Err(err).Msg("fail to maintain InstantSend allowance while disabled")
		}
		h.publishAllowance(admission)
		return h.backend.outbounds(height)
	}
	if h.enabled != previousEnabled {
		// Rebuild queued batches after the enabled switch changes.
		h.txQueue = nil
	}
	if h.isPaused() {
		h.txQueue = nil
		h.client.nextMempoolScan = scanOutbounds
		// Keep existing recovery moving, without starting a new allowance snapshot.
		if _, err = h.prepareAllowance(errInstantSendPaused); err != nil {
			h.client.log.Err(err).Msg("fail to maintain InstantSend allowance while halted")
		}
		h.publishAllowance(instantSendAdmission{reason: "halt"})
		return h.backend.outbounds(height)
	}

	// Each batch has one MemPool flag, so alternate ordinary vault outbounds
	// and lock-verified InstantSend observations, starting with outbounds.
	// Select the next scan before fetching so errors cannot stall either path.
	switch h.client.nextMempoolScan {
	case scanInstantSend:
		h.client.nextMempoolScan = scanOutbounds
		var admission instantSendAdmission
		admission, err = h.prepareAllowance(err)
		if err != nil {
			h.client.log.Err(err).Msg("customer InstantSend observations paused")
		}
		// Publish after this scan's preparation and claims, using its own decision.
		defer func() { h.publishAllowance(admission) }()
		var batch types.TxIn
		batch, err = h.fetchBatch(admission)
		if errors.Is(err, errInstantSendPaused) {
			// The halt became visible during the scan. No claims were written.
			h.txQueue = nil
			h.client.nextMempoolScan = scanOutbounds
			admission = instantSendAdmission{reason: "halt"}
			return h.backend.outbounds(height)
		}
		return batch, err
	default: // scanOutbounds is the initial scan and the fallback.
		h.client.nextMempoolScan = scanInstantSend
		return h.backend.outbounds(height)
	}
}

// fetchBatch claims one bounded batch and attaches Observer acknowledgements.
// Lost handoffs remain pending for the ChainLock scan instead of being retried here.
func (h *instantSendHandler) fetchBatch(admission instantSendAdmission) (types.TxIn, error) {
	if h.client.cfg.BlockScanner.DBPath == "" {
		return types.TxIn{Chain: h.client.GetChain()}, nil
	}
	if len(h.txQueue) == 0 {
		groups, err := h.scan()
		if err != nil {
			return types.TxIn{}, err
		}
		h.txQueue = groups
	}
	if len(h.txQueue) == 0 {
		return types.TxIn{Chain: h.client.GetChain()}, nil
	}
	batch := h.txQueue[0]
	// A reference-height group can span several mempool batches. Bound the fresh finality
	// RPC and allowance write batch, keeping the remainder queued.
	count := min(len(batch.TxArray), h.workLimit())
	batch.TxArray = batch.TxArray[:count]
	var err error
	batch.TxArray, err = h.claimBatch(batch.TxArray, admission)
	if err != nil {
		return types.TxIn{}, err
	}
	if count == len(h.txQueue[0].TxArray) {
		h.txQueue[0] = types.TxIn{}
		h.txQueue = h.txQueue[1:]
	} else {
		h.txQueue[0].TxArray = h.txQueue[0].TxArray[count:]
	}
	if h.client.m != nil {
		if counter := h.client.m.GetCounter(h.policy.metrics.observed); counter != nil {
			counter.Add(float64(len(batch.TxArray)))
		}
	}
	h.watchDelivery(batch.TxArray)
	return batch, nil
}

// instantSendObservationGroups builds final batches ordered by reference height.
// Observer uses a single finalization height for each batch. Mixing reference heights
// would make the signed observation depend on which worker happened to finish first.
func instantSendObservationGroups(chain common.Chain, byHeight map[int64][]*types.TxInItem) []types.TxIn {
	heights := make([]int64, 0, len(byHeight))
	for height := range byHeight {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	groups := make([]types.TxIn, 0, len(heights))
	for _, height := range heights {
		groups = append(groups, types.TxIn{
			Chain: chain,
			// Compatibility with Observer's finalized-inbound path. These items
			// have Core-validated finality despite originating in the mempool.
			MemPool:              false,
			ConfirmationRequired: 0,
			TxArray:              byHeight[height],
		})
	}
	return groups
}

// pendingCandidates rotates through the mempool so a bounded scan cannot starve
// later txids behind candidates whose locks or policy checks are still pending.
func (h *instantSendHandler) pendingCandidates(mempool map[string]instantSendMempoolEntry) ([]string, error) {
	ids := make([]string, 0, len(mempool))
	for id, entry := range mempool {
		if entry.lock != instantSendUnlocked {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	limit := len(ids)
	if h.client.cfg.UTXO.TransactionBatchSize > 0 && h.client.cfg.UTXO.MaxMempoolBatches > 0 {
		limit = min(limit, h.client.cfg.UTXO.TransactionBatchSize*h.client.cfg.UTXO.MaxMempoolBatches)
	}
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > h.mempoolCursor })
	candidates := make([]string, 0, limit)
	for offset := 0; offset < len(ids) && len(candidates) < limit; offset++ {
		id := ids[(start+offset)%len(ids)]
		h.mempoolCursor = id
		if _, ignored := h.ignored[id]; ignored {
			continue
		}
		observed, err := h.client.temporalStorage.HasObservedTx(strings.ToLower(id))
		if err != nil {
			return nil, fmt.Errorf("fail to check observation marker for %s: %w", id, err)
		}
		if !observed {
			candidates = append(candidates, id)
		}
	}
	return candidates, nil
}

// watchDelivery completes accepted markers in one write after the batch's
// callbacks finish. Rejected or lost handoffs remain pending for the block scan.
// Observer resolves the whole batch in one pass. A missing callback leaves even
// stored siblings pending, so the ChainLock scan can deliver them again.
// This collector belongs to the batch; callbacks do not take the handler state lock.
func (h *instantSendHandler) watchDelivery(items []*types.TxInItem) {
	store := h.client.temporalStorage
	var mu sync.Mutex
	remaining := len(items)
	var stored []string
	for _, item := range items {
		id := item.Tx
		var once sync.Once
		complete := func(accepted bool) {
			once.Do(func() {
				mu.Lock()
				if accepted {
					stored = append(stored, id)
				}
				remaining--
				finished := remaining == 0
				mu.Unlock()
				if finished {
					if err := store.CompleteInstantSendObservations(stored); err != nil {
						h.client.log.Err(err).Msg("fail to complete InstantSend markers; pending observations wait for ChainLock")
					}
				}
			})
		}
		item.OnObservedTxStored = func() { complete(true) }
		item.OnObservedTxRejected = func(error) { complete(false) }
	}
}
