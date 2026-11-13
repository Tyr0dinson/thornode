package utxo

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

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
	var haltHeight int64
	for _, key := range []string{
		"Halt" + h.client.GetChain().String() + "Chain", "SolvencyHalt" + h.client.GetChain().String() + "Chain",
		"HaltChainGlobal", fmt.Sprintf(constants.MimirTemplateHaltTrading, h.client.GetChain()),
	} {
		haltHeight, err = h.client.bridge.GetMimir(key)
		if err != nil {
			h.client.log.Err(err).Str("key", key).Msg("fail to read InstantSend halt setting")
			return true
		}
		if haltHeight > 0 && thorHeight >= haltHeight {
			return true
		}
	}
	pauseHeight, err := h.client.bridge.GetMimir("NodePauseChainGlobal")
	if err != nil {
		h.client.log.Err(err).Msg("fail to read InstantSend node pause setting")
		return true
	}
	return pauseHeight > 0 && thorHeight <= pauseHeight
}

// refreshSettings fails closed. Core also requires an explicit enabled Mimir.
func (h *instantSendHandler) refreshSettings() error {
	h.enabled = false
	if h.client.bridge == nil {
		h.setPauseMetric(true)
		return errors.New("InstantSend bridge unavailable")
	}
	enabled, err := h.client.bridge.GetMimir(fmt.Sprintf(constants.MimirTemplateInstantSendEnabled, h.client.GetChain()))
	if err != nil {
		h.setPauseMetric(true)
		return err
	}
	rate, err := h.client.bridge.GetMimir(fmt.Sprintf(constants.MimirTemplateInstantSendMinFeeRate, h.client.GetChain()))
	if err != nil {
		h.setPauseMetric(true)
		return err
	}
	h.minFeeRate = h.client.cfg.InstantSend.MinFeeRate
	if h.minFeeRate == 0 {
		h.minFeeRate = 8
	}
	if rate > 0 {
		h.minFeeRate = uint64(rate)
	}
	if h.client.m != nil {
		gauge := h.client.m.GetGauge(metrics.InstantSendMinFeeRate(h.client.GetChain()))
		if gauge != nil {
			gauge.Set(float64(h.minFeeRate))
		}
	}
	h.enabled = enabled > 0
	if !h.enabled {
		h.setPauseMetric(true)
	}
	return nil
}

// candidates rotates through the mempool so a bounded scan cannot starve
// later txids behind candidates whose locks or policy checks are still pending.
func (h *instantSendHandler) candidates(mempool map[string]rpc.GetRawMempoolVerboseResult) ([]string, error) {
	ids := make([]string, 0, len(mempool))
	for id, entry := range mempool {
		if entry.InstantLock != "false" && sufficientInstantSendFee(entry, h.minFeeRate) {
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
		marked, err := h.client.temporalStorage.HasTxMarker(strings.ToLower(id))
		if err != nil {
			return nil, fmt.Errorf("fail to check observation marker for %s: %w", id, err)
		}
		if !marked {
			candidates = append(candidates, id)
		}
	}
	return candidates, nil
}

// scan collects a bounded set of customer deposits, grouped by
// committed observation height. Allowance checks and observation claims happen before delivery.
func (h *instantSendHandler) scan() ([][]*types.TxInItem, error) {
	// Only opted-in chains may use these lock RPCs.
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
	accounting, err := h.client.getInstantSendAccounting()
	if err != nil {
		return nil, err
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
	items := make([]*types.TxInItem, len(ids))
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
		group.Go(func() error {
			items[i] = h.buildTxInItem(tx, accounting)
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
	return sortTxBatches(byHeight), nil
}
