package utxo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const DashInstantSendEnabledMimirKey = "DashInstantSendEnabled"

// refreshInstantSendSettings applies the Dash InstantSend enable/disable Mimir override.
func (c *Client) refreshInstantSendSettings() {
	if c.bridge == nil {
		return
	}

	// Load InstantSend Enabled mimir override.
	enabled, err := c.bridge.GetMimir(DashInstantSendEnabledMimirKey)
	if err != nil {
		c.log.Err(err).Msg("fail to get DashInstantSendEnabled mimir")
	} else if enabled >= 0 {
		c.instantSendSettings.Enabled = enabled > 0
	}
}

// allowInstantSend applies our InstantSend policy for mempool observations.
//
// When this returns false, we intentionally do NOT emit the tx via the
// InstantSend mempool path and instead wait for the normal block-based flow
// (which checks the fetched block's ChainLock).
func (c *Client) allowInstantSend(age time.Duration) (bool, string) {
	if c.instantSendSettings.MaxMempoolAge > 0 && age > c.instantSendSettings.MaxMempoolAge {
		return false, fmt.Sprintf("mempool age too high: %v > %v", age, c.instantSendSettings.MaxMempoolAge)
	}
	return true, ""
}

// instantSendObservationHeight uses the lock's network-agreed cycle, independent
// of scanner lag and reorg retention settings. Core validates quorum eligibility.
func (c *Client) instantSendObservationHeight(lock *rpc.ISLockResult, _ int64) (int64, error) {
	if lock == nil || strings.TrimSpace(lock.CycleHash) == "" {
		return 0, errors.New("missing InstantSend cycle hash")
	}
	block, err := c.rpc.GetBlockVerbose(lock.CycleHash)
	if err != nil {
		return 0, fmt.Errorf("fail to fetch InstantSend cycle block: %w", err)
	}
	if block.Height <= 0 || block.Confirmations <= 0 || !strings.EqualFold(block.Hash, lock.CycleHash) {
		return 0, errors.New("InstantSend cycle block is not in the active chain")
	}
	return block.Height, nil
}

// instantSendCandidates rotates through the mempool so a bounded scan cannot starve
// later txids behind candidates whose locks or policy checks are still pending.
func (c *Client) instantSendCandidates(mempool map[string]rpc.GetRawMempoolVerboseResult) ([]string, error) {
	ids := make([]string, 0, len(mempool))
	for id, entry := range mempool {
		if entry.InstantLock != "false" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	limit := len(ids)
	if c.cfg.UTXO.TransactionBatchSize > 0 && c.cfg.UTXO.MaxMempoolBatches > 0 {
		limit = min(limit, c.cfg.UTXO.TransactionBatchSize*c.cfg.UTXO.MaxMempoolBatches)
	}
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > c.instantSendMempoolCursor })
	candidates := make([]string, 0, limit)
	for offset := 0; offset < len(ids) && len(candidates) < limit; offset++ {
		id := ids[(start+offset)%len(ids)]
		c.instantSendMempoolCursor = id
		observed, err := c.temporalStorage.HasObservedTx(strings.ToLower(id))
		if err != nil {
			return nil, fmt.Errorf("fail to check observation marker for %s: %w", id, err)
		}
		if !observed {
			candidates = append(candidates, id)
		}
	}
	return candidates, nil
}

// fetchInstantSendBatch returns one queued observation batch, scanning for more
// when the queue is empty. Only the returned batch is marked as observed; the
// remaining batches can still be retried or discarded after a settings change.
func (c *Client) fetchInstantSendBatch(height int64) (types.TxIn, error) {
	if len(c.instantSendQueuedBatches) == 0 {
		groups, err := c.scanInstantSend(height)
		if err != nil {
			return types.TxIn{}, err
		}
		c.instantSendQueuedBatches = groups
	}
	if len(c.instantSendQueuedBatches) == 0 {
		return types.TxIn{Chain: c.GetChain()}, nil
	}
	batch := c.instantSendQueuedBatches[0]
	var err error
	batch.TxArray, err = c.trackObservedTxs(batch.TxArray)
	if err != nil {
		return types.TxIn{}, err
	}
	c.instantSendQueuedBatches[0] = types.TxIn{}
	c.instantSendQueuedBatches = c.instantSendQueuedBatches[1:]
	if c.m != nil {
		if counter := c.m.GetCounter(metrics.DashInstantSendObserved); counter != nil {
			counter.Add(float64(len(batch.TxArray)))
		}
	}
	return batch, nil
}

func (c *Client) scanInstantSend(height int64) ([]types.TxIn, error) {
	// Only Dash currently supports the lock RPCs used by this scan.
	if !c.GetChain().Equals(common.DASHChain) {
		return nil, nil
	}
	mempool, err := c.rpc.GetRawMempoolVerbose()
	if err != nil {
		return nil, fmt.Errorf("fail to get %s mempool: %w", c.GetChain(), err)
	}
	ids, err := c.instantSendCandidates(mempool)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	workers := c.cfg.ParallelMempoolScan
	if workers <= 0 {
		workers = 5
	}
	// Indexed results avoid shared slice mutation and retain candidate order.
	raw := make([]*rpc.TxResult, len(ids))
	var group errgroup.Group
	group.SetLimit(workers)
	for i, id := range ids {
		group.Go(func() error {
			tx, fetchErr := c.rpc.GetRawTransactionVerbose(id)
			if fetchErr != nil {
				c.log.Debug().Err(fetchErr).Str("txid", id).Msg("retry InstantSend mempool transaction later")
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
		if tx != nil {
			lockedIDs = append(lockedIDs, ids[i])
			lockedTxs = append(lockedTxs, tx)
		}
	}
	locks, err := c.rpc.GetISLocks(lockedIDs)
	if err != nil {
		// Successful chunks can still progress. Failed chunks have nil entries.
		c.log.Debug().Err(err).Msg("retry missing InstantSend lock batches later")
	}
	cycleHeights := make(map[string]int64)
	items := make([]*types.TxInItem, len(locks))
	for i, lock := range locks {
		if lock == nil {
			continue
		}
		anchor, found := cycleHeights[lock.CycleHash]
		if !found {
			anchor, err = c.instantSendObservationHeight(lock, height)
			if err != nil {
				c.log.Debug().Err(err).Str("txid", lock.TxID).Msg("retry InstantSend cycle lookup later")
				continue
			}
			cycleHeights[lock.CycleHash] = anchor
		}
		group.Go(func() error {
			items[i] = c.buildInstantSendTxInItem(lockedTxs[i], anchor, mempool[lockedIDs[i]])
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
	return instantSendObservationGroups(c.GetChain(), byHeight), nil
}

func (c *Client) buildInstantSendTxInItem(tx *rpc.TxResult, anchor int64, entry rpc.GetRawMempoolVerboseResult) *types.TxInItem {
	item, err := c.getTxIn(&tx.TxRawResult, anchor, true, nil)
	if err != nil {
		c.log.Debug().Err(err).Str("txid", tx.Txid).Msg("retry InstantSend inbound extraction later")
		return nil
	}
	if item.IsEmpty() || item.Coins.IsEmpty() {
		return nil
	}
	if !c.isAsgardAddress(item.To) {
		return nil
	}
	if item.Coins[0].Amount.LT(c.cfg.ChainID.DustThreshold()) {
		return nil
	}
	var age time.Duration
	if entry.Time > 0 {
		age = time.Since(time.Unix(entry.Time, 0))
	}
	allow, reason := c.allowInstantSend(age)
	if !allow {
		c.log.Debug().Str("txid", item.Tx).Str("reason", reason).Msg("InstantSend transaction waiting for block observation")
		return nil
	}
	return &item
}

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
