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

// instantSendAdmission belongs to one scan, so a previous success cannot hide a
// failed settings read or preparation error on the next scan.
type instantSendAdmission struct {
	allowed bool
	reason  string // deferral metric label when customer admission is blocked
}

// instantSendRecovery tracks the reservation checks for this process. It does
// not own the durable reservations or require them all to settle before completion.
// All access is protected by instantSendAllowanceMu.
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

// currentInstantSendAdmission rechecks recovery immediately before reserving
// allowance or reporting availability. Call with instantSendAllowanceMu held.
func (c *Client) currentInstantSendAdmission(admission instantSendAdmission) instantSendAdmission {
	if !admission.allowed {
		if admission.reason == "" {
			admission.reason = "not_ready"
		}
		return admission
	}
	if !c.cfg.InstantSend.Enabled || c.instantSendMaxAmount == 0 || c.cfg.BlockScanner.DBPath == "" || !c.instantSendRecovery.Complete() {
		return instantSendAdmission{reason: "not_ready"}
	}
	return admission
}

// prepareInstantSendAllowance resumes durable state before admitting customers.
// Only a missing journal needs a snapshot. Existing reservations survive restarts,
// disabled InstantSend, Mimir changes, mempool eviction and lost lock flags.
func (c *Client) prepareInstantSendAllowance(err error) (instantSendAdmission, error) {
	c.instantSendAllowanceMu.Lock()
	defer c.instantSendAllowanceMu.Unlock()
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
	if c.cfg.InstantSend.Enabled && err == nil && c.instantSendMaxAmount > 0 && c.bridge != nil {
		var status ttypes.NodeStatus
		status, statusErr = c.bridge.FetchNodeStatus()
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
		height, err = c.GetHeight()
		if err != nil {
			return blocked, err
		}
		// Scanner health allows a small lag; a new snapshot requires full catch-up.
		if !c.IsBlockScannerHealthy() || c.blockScanner.PreviousHeight() < height {
			return blocked, nil
		}
		var mempool map[string]rpc.GetRawMempoolVerboseResult
		mempool, err = c.rpc.GetRawMempoolVerbose()
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
			return blocked, err
		}
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
			tx, err = c.rpc.GetRawTransactionVerbose(id)
			var rpcErr *btcjson.RPCError
			if err != nil && !(errors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCInvalidAddressOrKey) {
				return blocked, err
			}
			// An unknown or evicted transaction remains charged. It is not proof
			// that a previously reported deposit is safe to forget.
			if err == nil {
				var settled bool
				settled, err = c.settleInstantSendTransaction(id, tx)
				if err != nil {
					return blocked, err
				}
				if settled {
					// One block can settle many pending IDs. Avoid fetching the
					// same full block for each of those transactions.
					state, err = c.temporalStorage.GetInstantSendState()
					if err != nil {
						return blocked, err
					}
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
// Call with instantSendAllowanceMu held, so settlement cannot race a snapshot reservation.
func (c *Client) classifyInstantSendSnapshot(state sharedutxo.InstantSendState) error {
	if len(state.SnapshotPending) == 0 {
		return nil
	}
	// A failed vault-list refresh must not classify a deposit as irrelevant.
	if _, err := c.getAsgardAddress(); err != nil {
		return err
	}
	var classified []string
	var reservations []sharedutxo.InstantSendReservation
	var failures []error
	pending := make(map[string]bool, len(state.SnapshotPending))
	for _, id := range state.SnapshotPending {
		pending[id] = true
	}
	ids := state.SnapshotPending[:min(len(state.SnapshotPending), c.instantSendWorkLimit())]
	txs, txErrors, err := c.rpc.BatchGetRawTransactionVerbose(ids)
	if err != nil {
		return err
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
		var settled bool
		settled, err = c.settleInstantSendTransaction(id, tx)
		if err != nil {
			failures = append(failures, err)
			break // do not repeat slow failed RPCs while blocking settlement
		}
		if settled {
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
		var amount uint64
		amount, err = c.instantSendSnapshotAmount(tx)
		if err != nil {
			failures = append(failures, err)
			break
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

// settleInstantSendTransaction requires actual inclusion in a canonical
// ChainLocked block. A height advance or a transaction flag alone cannot refill
// the allowance. Call with instantSendAllowanceMu held.
func (c *Client) settleInstantSendTransaction(id string, tx *rpc.TxResult) (bool, error) {
	if !strings.EqualFold(id, tx.Txid) {
		return false, errors.New("InstantSend transaction ID mismatch")
	}
	if !tx.ChainLock {
		return false, nil
	}
	if tx.BlockHash == "" {
		return false, errors.New("ChainLocked transaction has no block hash")
	}
	block, err := c.rpc.GetBlockVerboseTxs(tx.BlockHash)
	if err != nil {
		return false, err
	}
	if !block.ChainLock || block.Height < 0 || !strings.EqualFold(block.Hash, tx.BlockHash) {
		return false, errors.New("invalid ChainLocked transaction block")
	}
	hash, err := c.rpc.GetBlockHash(block.Height)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(hash, block.Hash) {
		return false, errors.New("ChainLocked transaction is not in the active chain")
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
		return false, errors.New("transaction missing from ChainLocked block")
	}
	return true, c.temporalStorage.ReleaseInstantSendReservations(ids)
}

// reconcileInstantSendBlock releases reservations for transactions in an already
// verified ChainLocked block. A settlement failure resets recovery so customer
// admission stays paused until the outstanding reservations are checked again.
func (c *Client) reconcileInstantSendBlock(block *btcjson.GetBlockVerboseTxResult) error {
	c.instantSendAllowanceMu.Lock()
	defer c.instantSendAllowanceMu.Unlock()
	ids := make([]string, 0, len(block.Tx))
	for _, tx := range block.Tx {
		ids = append(ids, tx.Txid)
	}
	err := c.temporalStorage.ReleaseInstantSendReservations(ids)
	if err != nil {
		// Retry inclusion checks before any further customer admission.
		c.instantSendRecovery.Reset()
	}
	return err
}

// claimInstantSendBatch rechecks locks and atomically claims allowance and observation
// markers while excluding concurrent block settlement. Vault senders bypass the allowance.
func (c *Client) claimInstantSendBatch(items []*types.TxInItem, admission instantSendAdmission) ([]*types.TxInItem, error) {
	c.instantSendAllowanceMu.Lock()
	defer c.instantSendAllowanceMu.Unlock()
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
	// Queued batches may outlive a block scan. Recheck under the same lock as
	// settlement. One bounded RPC batch avoids a timeout for each candidate.
	txs, txErrors, err := c.rpc.BatchGetRawTransactionVerbose(ids)
	if err != nil {
		c.deferInstantSend("finality")
		return nil, err
	}
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
	admission = c.currentInstantSendAdmission(admission)
	// Check after the finality and sender lookups, immediately before any writes.
	// Vault senders bypass the amount allowance, but must still respect halts.
	if c.isInstantSendPaused() {
		return nil, errInstantSendPaused
	}
	accepted, err := c.temporalStorage.ClaimInstantSendTxs(claims, c.instantSendMinAmount, c.instantSendMaxAmount, admission.allowed, time.Now().Unix())
	if err != nil {
		c.deferInstantSend("storage")
		c.log.Err(err).Msg("fail to reserve InstantSend allowance")
		if len(exempt) == 0 {
			return nil, err
		}
		// A damaged allowance journal must not stop vault transactions. They use
		// the existing observation-marker store without touching the allowance.
		if c.isInstantSendPaused() {
			return nil, errInstantSendPaused
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
	c.instantSendAllowanceMu.Lock()
	defer c.instantSendAllowanceMu.Unlock()
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
