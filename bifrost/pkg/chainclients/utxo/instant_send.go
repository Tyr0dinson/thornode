package utxo

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// instantSendHandler owns InstantSend scanning, allowance and recovery state.
// It shares the client's dependencies and must not be copied after first use.
type instantSendHandler struct {
	client *Client

	// Owned by the single mempool scanner; rebuilt after restart.
	mempoolCursor string
	// Transactions with no vault output, cached only for this vault-address set.
	ignored map[string]struct{}
	vaults  []string
	// Transaction batches awaiting final checks and delivery.
	txQueue []types.TxIn
	// Amount settings are owned by the mempool scanner.
	// Both amounts and their Mimir values use duffs (common.One per DASH).
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

var errInstantSendPaused = errors.New("InstantSend observations paused")

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

// refreshVaults invalidates unrelated transaction IDs when vaults change
// and prunes IDs that left the mempool. Only the mempool scanner owns this cache.
func (h *instantSendHandler) refreshVaults(mempool map[string]rpc.GetRawMempoolVerboseResult) {
	addresses, err := h.client.getAsgardAddress()
	if err != nil {
		h.client.log.Debug().Err(err).Msg("skip InstantSend output filter while vault addresses are unavailable")
		clear(h.ignored)
		h.vaults = nil
		return
	}
	vaults := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if !address.IsEmpty() {
			vaults = append(vaults, strings.ToLower(address.String()))
		}
	}
	slices.Sort(vaults)
	vaults = slices.Compact(vaults)
	if !slices.Equal(vaults, h.vaults) || h.client.cfg.MempoolTxIDCacheSize <= 0 {
		clear(h.ignored)
	}
	h.vaults = vaults
	for id := range h.ignored {
		if _, present := mempool[id]; !present {
			delete(h.ignored, id)
		}
	}
}

// mayPayVault excludes only transactions with clearly unrelated outputs.
// Missing vault data or uncertain script decoding leaves the normal scan in charge.
func (h *instantSendHandler) mayPayVault(tx *rpc.TxResult) bool {
	if len(h.vaults) == 0 || len(tx.Vout) == 0 {
		return true
	}
	for _, out := range tx.Vout {
		addresses := h.client.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 0 {
			// OP_RETURN cannot pay a vault; other undecoded scripts remain retryable.
			script, err := hex.DecodeString(out.ScriptPubKey.Hex)
			if err != nil || len(script) == 0 || script[0] != txscript.OP_RETURN {
				return true
			}
		}
		for _, address := range addresses {
			if _, found := slices.BinarySearch(h.vaults, strings.ToLower(address)); found {
				return true
			}
		}
	}
	return false
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
	// A cycle group can span several mempool batches. Bound the fresh finality
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
		if counter := h.client.m.GetCounter(metrics.DashInstantSendObserved); counter != nil {
			counter.Add(float64(len(batch.TxArray)))
		}
	}
	h.watchDelivery(batch.TxArray)
	return batch, nil
}

// buildTxInItem extracts a vault inbound at the lock cycle height.
// It returns nil if extraction fails or the recipient, dust, or age checks reject it.
func (h *instantSendHandler) buildTxInItem(tx *rpc.TxResult, anchor int64, entry rpc.GetRawMempoolVerboseResult) *types.TxInItem {
	item, err := h.client.getTxIn(&tx.TxRawResult, anchor, true, nil)
	if err != nil {
		h.client.log.Debug().Err(err).Str("txid", tx.Txid).Msg("retry InstantSend inbound extraction later")
		return nil
	}
	if item.IsEmpty() || item.Coins.IsEmpty() {
		return nil
	}
	if !h.client.isAsgardAddress(item.To) {
		return nil
	}
	if item.Coins[0].Amount.LT(h.client.cfg.ChainID.DustThreshold()) {
		return nil
	}
	var age time.Duration
	if entry.Time > 0 {
		age = time.Since(time.Unix(entry.Time, 0))
	}
	allow, reason := h.allowAge(age)
	if !allow {
		h.client.log.Debug().Str("txid", item.Tx).Str("reason", reason).Msg("InstantSend transaction waiting for block observation")
		return nil
	}
	return &item
}

// instantSendObservationGroups builds final batches ordered by lock cycle height.
// Observer uses a single finalization height for each batch. Mixing cycle heights
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
