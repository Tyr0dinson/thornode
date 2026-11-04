package utxo

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const (
	DashInstantSendEnabledMimirKey   = "DashInstantSendEnabled"
	DashInstantSendMinAmountMimirKey = "DashInstantSendMinAmount"
	DashInstantSendMaxAmountMimirKey = "DashInstantSendMaxAmount"
)

const defaultInstantSendMinAmount uint64 = common.One / 100 // 0.01 DASH, in duffs

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
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal"} {
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
		enabled, err = h.client.bridge.GetMimir(DashInstantSendEnabledMimirKey)
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
	minAmount, err := h.client.bridge.GetMimir(DashInstantSendMinAmountMimirKey)
	if err != nil {
		return fmt.Errorf("fail to read InstantSend minimum amount: %w", err)
	}
	maxAmount, err := h.client.bridge.GetMimir(DashInstantSendMaxAmountMimirKey)
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

// observationHeight uses the lock's network-agreed cycle, independent
// of scanner lag and reorg retention settings. Core validates quorum eligibility.
func (h *instantSendHandler) observationHeight(lock *rpc.ISLockResult) (int64, error) {
	if lock == nil || strings.TrimSpace(lock.CycleHash) == "" {
		return 0, errors.New("missing InstantSend cycle hash")
	}
	block, err := h.client.rpc.GetBlockVerbose(lock.CycleHash)
	if err != nil {
		return 0, fmt.Errorf("fail to fetch InstantSend cycle block: %w", err)
	}
	if block.Height <= 0 || block.Confirmations <= 0 || !strings.EqualFold(block.Hash, lock.CycleHash) {
		return 0, errors.New("InstantSend cycle block is not in the active chain")
	}
	return block.Height, nil
}

// candidates rotates through the mempool so a bounded scan cannot starve
// later txids behind candidates whose locks or policy checks are still pending.
func (h *instantSendHandler) candidates(mempool map[string]rpc.GetRawMempoolVerboseResult) ([]string, error) {
	ids := make([]string, 0, len(mempool))
	for id, entry := range mempool {
		if entry.InstantLock != "false" {
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

// scan collects a bounded set of locked mempool candidates, grouped by
// lock cycle height. Allowance checks and observation claims happen before delivery.
func (h *instantSendHandler) scan() ([]types.TxIn, error) {
	// Only Dash currently supports the lock RPCs used by this scan.
	if !h.client.GetChain().Equals(common.DASHChain) {
		return nil, nil
	}
	mempool, err := h.client.rpc.GetRawMempoolVerbose()
	if err != nil {
		return nil, fmt.Errorf("fail to get %s mempool: %w", h.client.GetChain(), err)
	}
	h.refreshVaults(mempool)
	ids, err := h.candidates(mempool)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	workers := h.client.cfg.ParallelMempoolScan
	if workers <= 0 {
		workers = 5
	}
	// Indexed results avoid shared slice mutation and retain candidate order.
	raw := make([]*rpc.TxResult, len(ids))
	var group errgroup.Group
	group.SetLimit(workers)
	for i, id := range ids {
		group.Go(func() error {
			tx, rpcErr := h.client.rpc.GetRawTransactionVerbose(id)
			if rpcErr != nil {
				h.client.log.Debug().Err(rpcErr).Str("txid", id).Msg("retry InstantSend mempool transaction later")
			} else if strings.EqualFold(tx.Txid, id) && tx.InstantSendLocked() {
				raw[i] = tx
			}
			return nil
		})
	}
	err = group.Wait()
	if err != nil {
		return nil, err
	}
	lockedIDs := make([]string, 0, len(ids))
	lockedTxs := make([]*rpc.TxResult, 0, len(ids))
	for i, tx := range raw {
		if tx == nil {
			continue
		}
		if !h.mayPayVault(tx) {
			if len(h.ignored) < h.client.cfg.MempoolTxIDCacheSize {
				if h.ignored == nil {
					h.ignored = make(map[string]struct{})
				}
				h.ignored[ids[i]] = struct{}{}
			}
			continue
		}
		lockedIDs = append(lockedIDs, ids[i])
		lockedTxs = append(lockedTxs, tx)
	}
	locks, err := h.client.rpc.GetISLocks(lockedIDs)
	if err != nil {
		// Successful chunks can still progress. Failed chunks have nil entries.
		h.client.log.Debug().Err(err).Msg("retry missing InstantSend lock batches later")
	}
	cycleHeights := make(map[string]int64)
	items := make([]*types.TxInItem, len(locks))
	for i, lock := range locks {
		if lock == nil {
			continue
		}
		anchor, found := cycleHeights[lock.CycleHash]
		if !found {
			anchor, err = h.observationHeight(lock)
			if err != nil {
				h.client.log.Debug().Err(err).Str("txid", lock.TxID).Msg("retry InstantSend cycle lookup later")
				continue
			}
			cycleHeights[lock.CycleHash] = anchor
		}
		group.Go(func() error {
			items[i] = h.buildTxInItem(lockedTxs[i], anchor, mempool[lockedIDs[i]])
			return nil
		})
	}
	err = group.Wait()
	if err != nil {
		return nil, err
	}
	byHeight := make(map[int64][]*types.TxInItem)
	for _, item := range items {
		if item != nil {
			byHeight[item.BlockHeight] = append(byHeight[item.BlockHeight], item)
		}
	}
	return instantSendObservationGroups(h.client.GetChain(), byHeight), nil
}
