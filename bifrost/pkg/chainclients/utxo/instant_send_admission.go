package utxo

import (
	"errors"
	"slices"
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (h *instantSendHandler) workLimit() int { return max(1, h.client.cfg.UTXO.TransactionBatchSize) }

// verifyPending reads current daemon state before new early admission. The core
// ledger and its deadline survive restarts; a local RPC result never releases it.
func (h *instantSendHandler) verifyPending(state *ttypes.QueryInstantSendResponse) error {
	for _, vault := range state.Vaults {
		for _, entry := range vault.State.Pending {
			id := strings.ToLower(entry.Id.String())
			tx, err := h.client.rpc.GetRawTransactionVerbose(id)
			if err != nil {
				if isUTXOTxNotFoundErr(err) {
					h.recordDeferral("lost_lock")
				}
				return err
			}
			if !strings.EqualFold(tx.Txid, id) {
				return errors.New("pending transaction ID mismatch")
			}
			if !tx.ChainLock && !tx.InstantSendLocked() {
				h.recordDeferral("lost_lock")
				return errInstantSendPaused
			}
		}
	}
	return nil
}

func (h *instantSendHandler) claimBatch(candidates []*types.TxInItem) ([]*types.TxInItem, error) {
	c := h.client
	h.setPaused(true)
	if c.cfg.BlockScanner.DBPath == "" || !h.enabled || c.blockScanner == nil || !c.IsBlockScannerHealthy() || h.isPaused() {
		h.recordDeferral("health")
		return nil, errInstantSendPaused
	}
	status, err := c.bridge.FetchNodeStatus()
	if err != nil {
		return nil, err
	}
	if status != ttypes.NodeStatus_Active {
		return nil, errInstantSendPaused
	}
	state, err := c.getInstantSendAccounting()
	if err != nil {
		return nil, err
	}
	if state.Status.Disabled {
		return nil, errInstantSendPaused
	}
	err = h.verifyPending(state)
	if err != nil {
		return nil, err
	}
	total := cosmos.ZeroUint()
	count := 0
	for _, vault := range state.Vaults {
		total = total.Add(vault.State.PendingAmount())
		count += len(vault.State.Pending)
	}
	h.publishExposure(total, state.MaxAmount, count)
	if state.MaxAmount == 0 || total.GTE(cosmos.NewUint(state.MaxAmount)) {
		return nil, nil
	}
	mempool, err := c.rpc.GetRawMempoolVerbose()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(candidates))
	items := make(map[string]*types.TxInItem)
	minHeight := state.Status.MinimumHeight(state.ObservationHeight)
	for _, item := range candidates {
		id := strings.ToLower(item.Tx)
		if _, found := items[id]; found {
			continue
		}
		amount := item.Coins.GetCoin(c.GetChain().GetGasAsset()).Amount
		if amount.IsZero() || amount.LT(cosmos.NewUint(state.MinAmount)) || total.Add(amount).GT(cosmos.NewUint(state.MaxAmount)) {
			continue
		}
		if item.BlockHeight != common.InstantSendAnchor(item.BlockHeight) ||
			item.BlockHeight < minHeight || item.BlockHeight > state.ObservationHeight {
			continue
		}
		if item.InstantSend == nil || !instantSendSwapMemo(item.Memo) || instantSendAdmissionState(c.GetChain(), state, item.To) == nil {
			continue
		}
		tx, txErr := c.rpc.GetRawTransactionVerbose(id)
		if txErr != nil {
			continue
		}
		if !strings.EqualFold(tx.Txid, id) || tx.ChainLock || !tx.InstantSendLocked() || c.isAsgardAddress(item.Sender) {
			continue
		}
		if !sufficientInstantSendFee(mempool[id], h.minFeeRate) {
			continue
		}
		inputs, inputErr := c.verifiedInstantSendInputs(tx)
		if inputErr != nil || !slices.Equal(inputs, common.OutputRefsFromPointers(item.SpentOutputRefs)) {
			continue
		}
		ids = append(ids, id)
		items[id] = item
		total = total.Add(amount) // Advisory only; core arbitrates concurrent node claims.
	}
	if !c.IsBlockScannerHealthy() || h.isPaused() {
		return nil, errInstantSendPaused
	}
	h.setPaused(false)
	accepted, err := c.temporalStorage.ClaimInstantSendTxs(ids)
	if err != nil {
		return nil, err
	}
	result := make([]*types.TxInItem, 0, len(accepted))
	for _, id := range accepted {
		result = append(result, items[id])
	}
	return result, nil
}

func (h *instantSendHandler) recordDeferral(reason string) {
	if h.client.m == nil {
		return
	}
	counter := h.client.m.GetCounterVec(metrics.InstantSendDeferred(h.client.GetChain()))
	if counter != nil {
		counter.WithLabelValues(reason).Inc()
	}
}

func (h *instantSendHandler) publishExposure(total cosmos.Uint, limit uint64, count int) {
	if h.client.m == nil {
		return
	}
	remaining := common.SafeSub(cosmos.NewUint(limit), total)
	for metric, value := range map[metrics.MetricName]float64{
		metrics.InstantSendExposure(h.client.GetChain()):  float64(total.Uint64()),
		metrics.InstantSendAvailable(h.client.GetChain()): float64(remaining.Uint64()),
		metrics.InstantSendPending(h.client.GetChain()):   float64(count),
	} {
		gauge := h.client.m.GetGauge(metric)
		if gauge != nil {
			gauge.Set(value)
		}
	}
}

func (h *instantSendHandler) setPaused(paused bool) {
	if h.client.m == nil {
		return
	}
	gauge := h.client.m.GetGauge(metrics.InstantSendPaused(h.client.GetChain()))
	if gauge == nil {
		return
	}
	value := 0.0
	if paused {
		value = 1
	}
	gauge.Set(value)
}
