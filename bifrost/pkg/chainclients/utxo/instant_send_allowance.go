package utxo

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// errInstantSendStateChanged defers RPC results invalidated by block settlement.
var errInstantSendStateChanged = errors.New("InstantSend state changed during RPC checks")

// instantSendAdmission belongs to one scan, so a previous success cannot hide a
// failed settings read or preparation error on the next scan.
type instantSendAdmission struct {
	allowed bool
	reason  string // deferral metric label when customer admission is blocked
}

// instantSendRecovery tracks the reservation checks for this process. It does
// not own the durable reservations or require them all to settle before completion.
// All access is protected by instantSendStateMu.
type instantSendRecovery struct {
	loaded       bool
	pendingTxIDs []string
}

// Load captures reservation IDs in a stable order once per recovery pass.
func (r *instantSendRecovery) Load(reservations map[string]sharedutxo.InstantSendReservation) {
	if r.loaded {
		return
	}
	r.pendingTxIDs = make([]string, 0, len(reservations))
	for id := range reservations {
		r.pendingTxIDs = append(r.pendingTxIDs, id)
	}
	slices.Sort(r.pendingTxIDs)
	r.loaded = true
}

// Next returns the next reservation ID without advancing, or false when none remain.
func (r *instantSendRecovery) Next() (string, bool) {
	if len(r.pendingTxIDs) == 0 {
		return "", false
	}
	return r.pendingTxIDs[0], true
}

// Advance is called only after Next returned a transaction and its check succeeded.
func (r *instantSendRecovery) Advance() {
	r.pendingTxIDs[0] = ""
	r.pendingTxIDs = r.pendingTxIDs[1:]
}

// Complete reports whether reservations were loaded and every recovery check finished.
func (r *instantSendRecovery) Complete() bool {
	return r.loaded && len(r.pendingTxIDs) == 0
}

// Reset clears local recovery progress so the durable reservations are checked again.
func (r *instantSendRecovery) Reset() {
	*r = instantSendRecovery{}
}

// lockAndCheckInstantSendRevision locks state and checks whether the revision is unchanged.
// The mutex remains locked on both true and false returns.
func (c *Client) lockAndCheckInstantSendRevision(revision uint64) bool {
	c.instantSendStateMu.Lock()
	return revision == c.instantSendRevision
}

// currentInstantSendAdmission rechecks recovery immediately before reserving
// allowance or reporting availability. Call with instantSendStateMu held.
func (c *Client) currentInstantSendAdmission(admission instantSendAdmission) instantSendAdmission {
	if !admission.allowed {
		if admission.reason == "" {
			admission.reason = "not_ready"
		}
		return admission
	}
	if !c.instantSendEnabled || c.instantSendMaxAmount == 0 || c.cfg.BlockScanner.DBPath == "" || !c.instantSendRecovery.Complete() {
		return instantSendAdmission{reason: "not_ready"}
	}
	return admission
}

// prepareInstantSendAllowance resumes durable state before admitting customers.
// Only a missing journal needs a snapshot. Existing reservations survive restarts,
// disabled InstantSend, Mimir changes, mempool eviction and lost lock flags.
func (c *Client) prepareInstantSendAllowance(err error) (instantSendAdmission, error) {
	c.instantSendStateMu.Lock()
	// Every RPC section reacquires the lock before returning, so this stays balanced.
	defer c.instantSendStateMu.Unlock()
	revision := c.instantSendRevision
	blocked := instantSendAdmission{reason: "not_ready"}
	if err != nil {
		blocked.reason = "settings"
	}
	if c.cfg.BlockScanner.DBPath == "" {
		return blocked, nil
	}
	// Disabling admission must not prevent settlement of an existing journal.
	canAdmit := false
	var statusErr error
	if c.instantSendEnabled && err == nil && c.instantSendMaxAmount > 0 && c.bridge != nil {
		var status ttypes.NodeStatus
		// Allow settlement while waiting for THORNode's status response.
		c.instantSendStateMu.Unlock()
		status, statusErr = c.bridge.FetchNodeStatus()
		// Check for intervening settlement before using the response.
		if !c.lockAndCheckInstantSendRevision(revision) {
			return blocked, nil
		}
		canAdmit = statusErr == nil && status == ttypes.NodeStatus_Active
	}
	state, err := c.temporalStorage.GetInstantSendState()
	if err != nil {
		return blocked, err
	}
	if !state.Started {
		if !canAdmit {
			return blocked, statusErr
		}
		if c.blockScanner == nil || !c.IsBlockScannerHealthy() {
			return blocked, nil
		}
		var height int64
		// Allow settlement while reading the Dash height.
		c.instantSendStateMu.Unlock()
		height, err = c.GetHeight()
		if !c.lockAndCheckInstantSendRevision(revision) {
			return blocked, nil
		}
		if err != nil {
			return blocked, err
		}
		// Scanner health allows a small lag; a new snapshot requires full catch-up.
		if !c.IsBlockScannerHealthy() || c.blockScanner.PreviousHeight() < height {
			return blocked, nil
		}
		var mempool map[string]rpc.GetRawMempoolVerboseResult
		// Allow settlement while fetching the mempool snapshot.
		c.instantSendStateMu.Unlock()
		mempool, err = c.rpc.GetRawMempoolVerbose()
		if !c.lockAndCheckInstantSendRevision(revision) {
			return blocked, nil
		}
		if err != nil {
			return blocked, err
		}
		ids := make([]string, 0, len(mempool))
		for id, entry := range mempool {
			if entry.InstantLock != "false" {
				ids = append(ids, id)
			}
		}
		if err = c.temporalStorage.StartInstantSendWarmup(height, ids); err != nil {
			return blocked, err
		}
		state, err = c.temporalStorage.GetInstantSendState()
		if err != nil {
			return blocked, err
		}
	}
	if !state.Ready {
		if c.bridge == nil {
			return blocked, nil
		}
		if err = c.classifyInstantSendSnapshot(state); err != nil {
			if errors.Is(err, errInstantSendStateChanged) {
				return blocked, nil
			}
			return blocked, err
		}
		// Classification can itself settle a verified block while holding the lock.
		revision = c.instantSendRevision
		if c.blockScanner == nil {
			return blocked, nil
		}
		// PreviousHeight advances after scanner delivery; currentBlockHeight is
		// updated while fetching and cannot prove that catch-up has completed.
		if err = c.temporalStorage.CompleteInstantSendWarmup(c.blockScanner.PreviousHeight()); err != nil {
			return blocked, err
		}
		state, err = c.temporalStorage.GetInstantSendState()
		if err != nil || !state.Ready {
			return blocked, err
		}
	}
	// The block scanner may start beyond transactions mined while we were offline.
	// Check every existing reservation once before using the restored allowance.
	c.instantSendRecovery.Load(state.Reservations)
	for range c.instantSendWorkLimit() {
		id, pending := c.instantSendRecovery.Next()
		if !pending {
			break
		}
		if _, stillReserved := state.Reservations[id]; stillReserved {
			var tx *rpc.TxResult
			var settled []string
			// Inclusion checks use RPCs; block settlement must be able to run meanwhile.
			c.instantSendStateMu.Unlock()
			tx, err = c.rpc.GetRawTransactionVerbose(id)
			var rpcErr *btcjson.RPCError
			// An unknown or evicted transaction remains charged. It is not proof
			// that a previously reported deposit is safe to forget.
			if err == nil {
				settled, err = c.getInstantSendSettlement(id, tx)
			} else if errors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCInvalidAddressOrKey {
				err = nil
			}
			// Settlement may have reset recovery; check before advancing its cursor.
			if !c.lockAndCheckInstantSendRevision(revision) {
				return blocked, nil
			}
			if next, ok := c.instantSendRecovery.Next(); !ok || next != id {
				return blocked, nil
			}
			if err != nil {
				return blocked, err
			}
			if len(settled) > 0 {
				if err = c.releaseInstantSendReservations(settled); err != nil {
					return blocked, err
				}
				revision = c.instantSendRevision
				// One block can settle many pending IDs. Avoid fetching it again.
				state, err = c.temporalStorage.GetInstantSendState()
				if err != nil {
					return blocked, err
				}
			}
		}
		c.instantSendRecovery.Advance()
	}
	if !canAdmit || !c.instantSendRecovery.Complete() {
		return blocked, statusErr
	}
	return instantSendAdmission{allowed: true}, nil
}

// instantSendWorkLimit bounds each allowance work batch using the configured
// transaction batch size, with a default of 100.
func (c *Client) instantSendWorkLimit() int {
	if c.cfg.UTXO.TransactionBatchSize > 0 {
		return c.cfg.UTXO.TransactionBatchSize
	}
	return 100
}

// classifyInstantSendSnapshot ignores observed markers and the new minimum/age
// gates: these deposits might already have been credited before this startup.
// Call with instantSendStateMu held; it is held again on every return.
// Network work temporarily releases it so block settlement can continue;
// a changed revision prevents stale results from restoring settled reservations.
func (c *Client) classifyInstantSendSnapshot(state sharedutxo.InstantSendState) error {
	if len(state.SnapshotPending) == 0 {
		return nil
	}
	revision := c.instantSendRevision
	ids := state.SnapshotPending[:min(len(state.SnapshotPending), c.instantSendWorkLimit())]
	// A failed vault-list refresh must not classify a deposit as irrelevant.
	var txs []*rpc.TxResult
	var txErrors []error
	// Vault refresh and transaction lookups can both wait on the network.
	c.instantSendStateMu.Unlock()
	_, err := c.getAsgardAddress()
	if err == nil {
		txs, txErrors, err = c.rpc.BatchGetRawTransactionVerbose(ids)
	}
	if !c.lockAndCheckInstantSendRevision(revision) {
		return errInstantSendStateChanged
	}
	if err != nil {
		return err
	}
	var classified []string
	var reservations []sharedutxo.InstantSendReservation
	var failures []error
	pending := make(map[string]bool, len(state.SnapshotPending))
	for _, id := range state.SnapshotPending {
		pending[id] = true
	}
	for i, id := range ids {
		if !pending[id] {
			continue
		}
		if txErrors[i] != nil {
			failures = append(failures, txErrors[i])
			continue
		}
		tx := txs[i]
		var settled []string
		var amount uint64
		// Inclusion, sender and vault lookups run unlocked to allow settlement.
		c.instantSendStateMu.Unlock()
		settled, err = c.getInstantSendSettlement(id, tx)
		if err == nil && len(settled) == 0 {
			amount, err = c.instantSendSnapshotAmount(tx)
		}
		// Reject results if settlement changed the snapshot while we were waiting.
		if !c.lockAndCheckInstantSendRevision(revision) {
			return errInstantSendStateChanged
		}
		if err != nil {
			failures = append(failures, err)
			break // retry the remaining snapshot on a later scan
		}
		if len(settled) > 0 {
			if err = c.releaseInstantSendReservations(settled); err != nil {
				return err
			}
			revision = c.instantSendRevision
			// Inclusion also removes other snapshot IDs found in this block.
			var current sharedutxo.InstantSendState
			current, err = c.temporalStorage.GetInstantSendState()
			if err != nil {
				return err
			}
			clear(pending)
			for _, id := range current.SnapshotPending {
				pending[id] = true
			}
			continue
		}
		classified = append(classified, id)
		if amount > 0 {
			reservations = append(reservations, sharedutxo.InstantSendReservation{TxID: id, Amount: amount, CreatedAt: time.Now().Unix()})
		}
	}
	if err = c.temporalStorage.RecordInstantSendSnapshot(classified, reservations); err != nil {
		return err
	}
	return errors.Join(failures...)
}

// instantSendSnapshotAmount returns the customer deposit amount to reserve during
// startup, in duffs. Vault senders are exempt; current minimum and age limits do not apply.
func (c *Client) instantSendSnapshotAmount(tx *rpc.TxResult) (uint64, error) {
	if !tx.InstantSendLocked() || len(tx.Vin) == 0 {
		return 0, nil
	}
	// Avoid fetching parents for unrelated mempool traffic.
	relevant := false
	for _, out := range tx.Vout {
		addresses := c.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 1 && c.isAsgardAddress(addresses[0]) {
			relevant = true
			break
		}
	}
	if !relevant {
		return 0, nil
	}
	sender, err := c.getSender(&tx.TxRawResult, nil)
	if errors.Is(err, errInvalidSenderUTXO) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if c.isAsgardAddress(sender) {
		return 0, nil
	}
	out, err := c.getOutput(sender, &tx.TxRawResult, false)
	if errors.Is(err, btypes.ErrFailOutputMatchCriteria) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !c.isValidUTXO(out.ScriptPubKey.Hex) {
		return 0, nil
	}
	amount, err := btcutil.NewAmount(out.Value)
	if err != nil {
		return 0, err
	}
	if amount <= 0 || uint64(amount) < c.cfg.ChainID.DustThreshold().Uint64() {
		return 0, nil
	}
	return uint64(amount), nil
}

// getInstantSendSettlement verifies inclusion in a canonical ChainLocked block
// and returns its transaction IDs. It performs RPCs without changing the journal;
// call without instantSendStateMu held.
func (c *Client) getInstantSendSettlement(id string, tx *rpc.TxResult) ([]string, error) {
	if !strings.EqualFold(id, tx.Txid) {
		return nil, errors.New("InstantSend transaction ID mismatch")
	}
	if !tx.ChainLock {
		return nil, nil
	}
	if tx.BlockHash == "" {
		return nil, errors.New("ChainLocked transaction has no block hash")
	}
	block, err := c.rpc.GetBlockVerboseTxs(tx.BlockHash)
	if err != nil {
		return nil, err
	}
	if !block.ChainLock || block.Height < 0 || !strings.EqualFold(block.Hash, tx.BlockHash) {
		return nil, errors.New("invalid ChainLocked transaction block")
	}
	hash, err := c.rpc.GetBlockHash(block.Height)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hash, block.Hash) {
		return nil, errors.New("ChainLocked transaction is not in the active chain")
	}
	ids := make([]string, 0, len(block.Tx))
	found := false
	for _, item := range block.Tx {
		ids = append(ids, item.Txid)
		if strings.EqualFold(item.Txid, id) {
			found = true
		}
	}
	if !found {
		return nil, errors.New("transaction missing from ChainLocked block")
	}
	return ids, nil
}

// releaseInstantSendReservations invalidates outstanding RPC results even when
// settlement fails. A failure resets recovery before further customer admission.
// Call with instantSendStateMu held, after verifying ChainLock inclusion.
func (c *Client) releaseInstantSendReservations(ids []string) error {
	c.instantSendRevision++
	err := c.temporalStorage.ReleaseInstantSendReservations(ids)
	if err != nil {
		// Retry inclusion checks before any further customer admission.
		c.instantSendRecovery.Reset()
	}
	return err
}

// reconcileInstantSendBlock releases reservations for transactions in an already
// verified ChainLocked block without waiting for mempool network calls.
func (c *Client) reconcileInstantSendBlock(block *btcjson.GetBlockVerboseTxResult) error {
	ids := make([]string, 0, len(block.Tx))
	for _, tx := range block.Tx {
		ids = append(ids, tx.Txid)
	}
	// Keep the revision increment, reservation release and recovery reset together.
	c.instantSendStateMu.Lock()
	defer c.instantSendStateMu.Unlock()
	return c.releaseInstantSendReservations(ids)
}

// claimInstantSendBatch checks finality without the allowance lock, then atomically
// claims allowance and markers only if settlement has not invalidated those checks.
// Vault senders bypass the allowance.
func (c *Client) claimInstantSendBatch(items []*types.TxInItem, admission instantSendAdmission) ([]*types.TxInItem, error) {
	c.instantSendStateMu.Lock()
	// Every RPC section reacquires the lock before returning, so this stays balanced.
	defer c.instantSendStateMu.Unlock()
	revision := c.instantSendRevision
	claims := make([]sharedutxo.InstantSendClaim, 0, len(items))
	byID := make(map[string]*types.TxInItem, len(items))
	ids := make([]string, 0, len(items))
	var exempt []*types.TxInItem
	for _, item := range items {
		id := strings.ToLower(item.Tx)
		if _, duplicate := byID[id]; duplicate {
			continue
		}
		ids = append(ids, id)
		byID[id] = item
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// Queued batches may outlive a block scan. Recheck finality without holding
	// up settlement, then reject the results if settlement happened meanwhile.
	c.instantSendStateMu.Unlock()
	txs, txErrors, err := c.rpc.BatchGetRawTransactionVerbose(ids)
	// Recheck the revision under the lock before using these finality results.
	if !c.lockAndCheckInstantSendRevision(revision) {
		c.deferInstantSend("finality")
		return nil, nil
	}
	if err != nil {
		c.deferInstantSend("finality")
		return nil, err
	}
	// Vault-cache refreshes and the halt check below can make network calls.
	c.instantSendStateMu.Unlock()
	for i, id := range ids {
		tx := txs[i]
		if txErrors[i] != nil || !strings.EqualFold(tx.Txid, id) || tx.ChainLock || !tx.InstantSendLocked() {
			c.deferInstantSend("finality")
			continue
		}
		item := byID[id]
		vaultSender := c.isAsgardAddress(item.Sender)
		if vaultSender {
			exempt = append(exempt, item)
		}
		claims = append(claims, sharedutxo.InstantSendClaim{TxID: id, Amount: item.Coins.GetCoin(c.cfg.ChainID.GetGasAsset()).Amount.Uint64(), Exempt: vaultSender})
	}
	// Check after the finality and sender lookups, immediately before any writes.
	// Vault senders bypass the amount allowance, but must still respect halts.
	paused := c.isInstantSendPaused()
	// Hold through the revision check and claim so settlement cannot slip between them.
	c.instantSendStateMu.Lock()
	if paused {
		return nil, errInstantSendPaused
	}
	if revision != c.instantSendRevision {
		c.deferInstantSend("finality")
		return nil, nil
	}
	admission = c.currentInstantSendAdmission(admission)
	accepted, err := c.temporalStorage.ClaimInstantSendTxs(claims, c.instantSendMinAmount, c.instantSendMaxAmount, admission.allowed, time.Now().Unix())
	if err != nil {
		c.deferInstantSend("storage")
		c.log.Err(err).Msg("fail to reserve InstantSend allowance")
		if len(exempt) == 0 {
			return nil, err
		}
		// A damaged allowance journal must not stop vault transactions. They use
		// the existing observation-marker store without touching the allowance.
		// Release again for the fresh halt lookup needed by this fallback.
		c.instantSendStateMu.Unlock()
		paused = c.isInstantSendPaused()
		// Protect the final checks and fallback marker write together.
		c.instantSendStateMu.Lock()
		if paused {
			return nil, errInstantSendPaused
		}
		if revision != c.instantSendRevision {
			c.deferInstantSend("finality")
			return nil, nil
		}
		return c.trackObservedTxs(exempt)
	}
	result := make([]*types.TxInItem, 0, len(accepted))
	for _, id := range accepted {
		result = append(result, byID[id])
		delete(byID, id)
	}
	state, _ := c.temporalStorage.GetInstantSendState()
	for _, claim := range claims {
		if _, skipped := byID[claim.TxID]; !skipped || claim.Exempt {
			continue
		}
		var observed bool
		if observed, err = c.temporalStorage.HasObservedTx(claim.TxID); err == nil && observed {
			continue // another scanner already delivered this transaction
		}
		reason := "allowance"
		switch {
		case state.Reservations[claim.TxID].Origin == sharedutxo.InstantSendSnapshot:
			reason = "startup_snapshot"
		case !admission.allowed:
			reason = admission.reason
		case claim.Amount < c.instantSendMinAmount:
			reason = "minimum"
		}
		c.deferInstantSend(reason)
	}
	return result, nil
}

// deferInstantSend records an observation deferral in the metric for its reason.
func (c *Client) deferInstantSend(reason string) {
	if c.m != nil {
		c.m.GetCounterVec(metrics.InstantSendDeferred).WithLabelValues(reason).Inc()
	}
}

// publishInstantSendAllowance reports journal usage, available allowance, and warmup status.
// It is called by the mempool scanner, which owns the current Mimir settings.
func (c *Client) publishInstantSendAllowance(admission instantSendAdmission) {
	if c.m == nil {
		return
	}
	c.instantSendStateMu.Lock()
	defer c.instantSendStateMu.Unlock()
	state, err := c.temporalStorage.GetInstantSendState()
	if err != nil {
		c.m.GetGauge(metrics.InstantSendAvailable).Set(0)
		return
	}
	warming := float64(1)
	if state.Ready {
		warming = 0
	}
	available := state.Available(c.instantSendMaxAmount)
	if !c.currentInstantSendAdmission(admission).allowed {
		available = 0
	}
	for metric, value := range map[metrics.MetricName]float64{
		metrics.InstantSendReserved:  float64(state.Reserved),
		metrics.InstantSendAvailable: float64(available),
		metrics.InstantSendPending:   float64(state.Count),
		metrics.InstantSendWarmingUp: warming,
	} {
		c.m.GetGauge(metric).Set(value)
	}
}
