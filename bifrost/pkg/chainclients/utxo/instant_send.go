package utxo

import (
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/btcsuite/btcd/txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// instantSendHandler owns InstantSend scanning and delivery state.
// It shares the client's dependencies and must not be copied after first use.
type instantSendHandler struct {
	client *Client

	// Owned by the single mempool scanner; rebuilt after restart.
	mempoolCursor string
	// Transactions with no vault output, cached only for this vault-address set.
	ignored map[string]struct{}
	vaults  []string
	// Transaction batches awaiting final checks and delivery.
	txQueue [][]*types.TxInItem
	// Owned by the mempool scanner. Financial limits live in THORChain.
	enabled    bool
	minFeeRate uint64
}

var errInstantSendPaused = errors.New("InstantSend observations paused")

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
func (h *instantSendHandler) fetchBatch() (types.TxIn, error) {
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
		_, err := h.claimBatch(nil)
		return types.TxIn{Chain: h.client.GetChain()}, err
	}
	candidates := h.txQueue[0]
	// A height group can span several mempool batches. Bound the fresh finality
	// RPC and delivery marker batch, keeping the remainder queued.
	count := min(len(candidates), h.workLimit())
	items, err := h.claimBatch(candidates[:count])
	if err != nil {
		return types.TxIn{}, err
	}
	if count == len(candidates) {
		h.txQueue[0] = nil
		h.txQueue = h.txQueue[1:]
	} else {
		clear(candidates[:count])
		h.txQueue[0] = candidates[count:]
	}
	if h.client.m != nil {
		if counter := h.client.m.GetCounter(metrics.InstantSendObserved(h.client.GetChain())); counter != nil {
			counter.Add(float64(len(items)))
		}
	}
	h.watchDelivery(items)
	// The existing finalized-inbound path requires MemPool=false. Dash Core
	// supplies finality; the network supplies the shared positive height.
	return types.TxIn{Chain: h.client.GetChain(), TxArray: items}, nil
}

// buildTxInItem binds a customer deposit to its network's positive observation anchor.
func (h *instantSendHandler) buildTxInItem(tx *rpc.TxResult, accounting *ttypes.QueryInstantSendResponse) *types.TxInItem {
	// Resolve the recipient before assigning its height. Unanchored items never
	// leave this function or enter the observation queue.
	item, err := h.client.getTxIn(&tx.TxRawResult, 0, true, nil)
	if err != nil {
		h.client.log.Debug().Err(err).Str("txid", tx.Txid).Msg("retry InstantSend inbound extraction later")
		return nil
	}
	if item.IsEmpty() || item.Coins.IsEmpty() {
		return nil
	}
	if !h.client.isAsgardAddress(item.To) || h.client.isAsgardAddress(item.Sender) {
		return nil
	}
	if item.Coins[0].Amount.LT(h.client.cfg.ChainID.DustThreshold()) {
		return nil
	}
	state := instantSendAdmissionState(h.client.GetChain(), accounting, item.To)
	if state == nil {
		return nil
	}
	if !instantSendSwapMemo(item.Memo) {
		return nil
	}
	item.BlockHeight = common.InstantSendAnchor(accounting.ObservationHeight)
	item.InstantSend = &common.InstantSend{}
	return &item
}

// Only inspect the memo type locally; core resolves THORNames and validates the swap.
func instantSendSwapMemo(value string) bool {
	name, _, _ := strings.Cut(value, ":")
	txType, err := memo.StringToTxType(name)
	return err == nil && txType == memo.TxSwap
}

// groupInstantSendTxs orders batches by their agreed observation height.
// Observer uses a single finalization height for each batch. Mixing heights
// would make the signed observation depend on which worker happened to finish first.
func groupInstantSendTxs(byHeight map[int64][]*types.TxInItem) [][]*types.TxInItem {
	heights := make([]int64, 0, len(byHeight))
	for height := range byHeight {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	groups := make([][]*types.TxInItem, 0, len(heights))
	for _, height := range heights {
		groups = append(groups, byHeight[height])
	}
	return groups
}

// watchDelivery completes accepted markers in one write after the batch's
// callbacks finish. Rejected or lost handoffs remain pending for the block scan.
// Observer resolves the whole batch in one pass. A missing callback leaves even
// stored siblings pending, so the ChainLock scan can deliver them again.
// This collector belongs to the batch; callbacks do not mutate the scanner.
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
					err := store.CompleteInstantSendObservations(stored)
					if err != nil {
						h.client.log.Err(err).Msg("fail to complete InstantSend markers; pending observations wait for ChainLock")
					}
				}
			})
		}
		item.OnObservedTxStored = func() { complete(true) }
		item.OnObservedTxRejected = func(error) { complete(false) }
	}
}
