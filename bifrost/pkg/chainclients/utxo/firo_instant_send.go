package utxo

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"golang.org/x/sync/errgroup"
)

const (
	FiroInstantSendEnabledMimirKey   = "FiroInstantSendEnabled"
	FiroInstantSendMinAmountMimirKey = "FiroInstantSendMinAmount"
	FiroInstantSendMaxAmountMimirKey = "FiroInstantSendMaxAmount"
)

type firoInstantSendBackend struct{ h *instantSendHandler }

func (b firoInstantSendBackend) mempool() (map[string]instantSendMempoolEntry, error) {
	entries, err := b.h.client.rpc.GetRawMempoolVerboseFIRO()
	if err != nil {
		return nil, err
	}
	result := make(map[string]instantSendMempoolEntry, len(entries))
	for id, entry := range entries {
		status := instantSendUnknown
		if entry.InstantLock != nil {
			status = instantSendUnlocked
			if *entry.InstantLock {
				status = instantSendLocked
			}
		}
		result[id] = instantSendMempoolEntry{time: entry.Time, lock: status}
	}
	return result, nil
}

func (b firoInstantSendBackend) transaction(id string) (*rpc.TxResult, error) {
	tx, err := b.h.client.rpc.GetRawTransactionVerboseFIRO(id)
	if err != nil {
		return nil, err
	}
	if tx.Type != 0 {
		// Keep inclusion metadata for recovery, but never observe privacy inputs.
		tx.InstantLock = false
		tx.Vin = nil
	} else if !tx.InstantLockKnown {
		return nil, fmt.Errorf("missing FIRO InstantSend status for %s", id)
	}
	return &tx.TxResult, nil
}

func (b firoInstantSendBackend) transactions(ids []string) ([]*rpc.TxResult, []error, error) {
	txs, failures := make([]*rpc.TxResult, len(ids)), make([]error, len(ids))
	var group errgroup.Group
	workers := b.h.client.cfg.ParallelMempoolScan
	if workers <= 0 {
		workers = 5
	}
	group.SetLimit(workers)
	for i, id := range ids {
		group.Go(func() error {
			txs[i], failures[i] = b.transaction(id)
			return nil
		})
	}
	return txs, failures, group.Wait()
}

// firoInstantSendAnchor uses only immutable direct-parent locations under
// ChainLocks. It is a reference height, not a claim about where this tx was mined.
// Zero means ineligible; errors mean unavailable or inconsistent evidence.
// No recursion through unconfirmed ancestors or dependence on the local tip.
func (c *Client) firoInstantSendAnchor(tx *rpc.TxResult, cache map[string]*rpc.FiroTxResult) (int64, error) {
	if tx == nil || !tx.InstantLock || tx.ChainLock {
		return 0, nil
	}
	ok, err := normalizeFiroOutputs(&tx.TxRawResult)
	if err != nil || !ok {
		return 0, err
	}
	var anchor int64
	seen := make(map[string]bool, len(tx.Vin))
	for _, input := range tx.Vin {
		key := formatUtxoKey(input.Txid, input.Vout)
		if seen[key] {
			return 0, fmt.Errorf("duplicate FIRO InstantSend input")
		}
		seen[key] = true
		_, err := c.getPreviousOutputFIRO(input.Txid, input.Vout, cache)
		if err != nil {
			return 0, err
		}
		parent := cache[input.Txid]
		if parent.Confirmations <= 0 || parent.BlockHash == "" {
			return 0, nil
		}
		err = c.requireFiroTransactionFinal(parent)
		if err != nil {
			return 0, err
		}
		anchor = max(anchor, parent.Height)
	}
	return anchor, nil
}

// deposit shares FIRO's exact script, memo, sender and fee rules with the block
// scanner. It deliberately excludes payments from vaults to customers.
func (b firoInstantSendBackend) deposit(tx *rpc.TxResult) (*types.TxInItem, bool, error) {
	c := b.h.client
	if !tx.InstantLock || tx.ChainLock || len(tx.Vin) == 0 {
		return nil, false, nil
	}
	vaults, err := c.getAsgardAddress()
	if err != nil {
		return nil, false, err
	}
	relevant := false
	for _, out := range tx.Vout {
		var address *firoutil.Address
		address, err = firoScriptAddress(out.ScriptPubKey.Hex)
		if err == nil && slices.Contains(vaults, common.Address(address.String())) {
			relevant = true
			break
		}
	}
	if !relevant {
		return nil, false, nil
	}
	cache := make(map[string]*rpc.FiroTxResult)
	item, err := c.getTxInFIRO(&tx.TxRawResult, 1, false, vaults, cache)
	if err != nil || item == nil {
		return nil, false, err
	}
	if !slices.Contains(vaults, common.Address(item.To)) {
		return nil, false, nil
	}
	anchor, err := c.firoInstantSendAnchor(tx, cache)
	if err != nil || anchor <= 0 {
		return nil, false, err
	}
	item.BlockHeight = anchor
	return item, slices.Contains(vaults, common.Address(item.Sender)), nil
}

func (b firoInstantSendBackend) scan() ([]types.TxIn, error) {
	if !b.h.client.IsBlockScannerHealthy() {
		return nil, nil
	}
	entries, err := b.mempool()
	if err != nil {
		return nil, err
	}
	ids, err := b.h.pendingCandidates(entries)
	if err != nil {
		return nil, err
	}
	txs, failures, err := b.transactions(ids)
	if err != nil {
		return nil, err
	}
	byHeight := make(map[int64][]*types.TxInItem)
	for i, id := range ids {
		if failures[i] != nil {
			continue
		}
		entry := entries[id]
		var age time.Duration
		if entry.time > 0 {
			age = time.Since(time.Unix(entry.time, 0))
		}
		if allowed, _ := b.h.allowAge(age); !allowed {
			continue
		}
		item, _, err := b.deposit(txs[i])
		if err != nil {
			b.h.client.log.Debug().Err(err).Str("txid", id).Msg("retry FIRO InstantSend deposit")
			continue
		}
		if item != nil {
			byHeight[item.BlockHeight] = append(byHeight[item.BlockHeight], item)
		}
	}
	return instantSendObservationGroups(common.FIROChain, byHeight), nil
}

func (b firoInstantSendBackend) outbounds(height int64) (types.TxIn, error) {
	return b.h.client.fetchMemPoolFIRO(height)
}

func (b firoInstantSendBackend) snapshotAmount(tx *rpc.TxResult) (uint64, error) {
	item, fromVault, err := b.deposit(tx)
	if err != nil || item == nil || fromVault {
		return 0, err
	}
	return item.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), nil
}

func (b firoInstantSendBackend) checkClaimFinality(item *types.TxInItem, tx *rpc.TxResult) (valid, fromVault bool, err error) {
	if !b.h.client.IsBlockScannerHealthy() {
		return false, false, nil
	}
	anchor, err := b.h.client.firoInstantSendAnchor(tx, make(map[string]*rpc.FiroTxResult))
	if err != nil || anchor <= 0 || anchor != item.BlockHeight {
		return false, false, err
	}
	vaults, err := b.h.client.getAsgardAddress()
	if err != nil {
		return false, false, err
	}
	return true, slices.Contains(vaults, common.Address(item.Sender)), nil
}

func (b firoInstantSendBackend) getChainLockedBlockTxIDs(id string, tx *rpc.TxResult) ([]string, error) {
	if tx == nil || !strings.EqualFold(id, tx.Txid) {
		return nil, fmt.Errorf("FIRO settlement transaction ID mismatch")
	}
	if !tx.ChainLock {
		return nil, nil
	}
	if !isHash256Hex(tx.BlockHash) {
		return nil, fmt.Errorf("FIRO settlement missing block hash")
	}
	header, err := b.h.client.rpc.GetBlockHeaderFIRO(tx.BlockHash)
	if err != nil {
		return nil, err
	}
	block, err := b.h.client.getBlockFIRO(header.Height)
	if err != nil {
		return nil, err
	}
	if block.Hash != tx.BlockHash {
		return nil, fmt.Errorf("FIRO settlement block is not active")
	}
	ids := make([]string, 0, len(block.Tx))
	found := false
	for _, entry := range block.Tx {
		ids = append(ids, entry.Txid)
		found = found || entry.Txid == id
	}
	if !found {
		return nil, fmt.Errorf("FIRO settlement transaction missing from block")
	}
	return ids, nil
}
