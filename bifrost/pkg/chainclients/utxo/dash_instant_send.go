package utxo

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"golang.org/x/sync/errgroup"
)

const (
	DashInstantSendEnabledMimirKey   = "DashInstantSendEnabled"
	DashInstantSendMinAmountMimirKey = "DashInstantSendMinAmount"
	DashInstantSendMaxAmountMimirKey = "DashInstantSendMaxAmount"
)

// dashInstantSendBackend retains Dash's native RPC shapes and cycle proof.
type dashInstantSendBackend struct{ h *instantSendHandler }

// observationHeight uses the lock's network-agreed cycle, independent
// of scanner lag and reorg retention settings. Core validates quorum eligibility.
func (b dashInstantSendBackend) observationHeight(lock *rpc.ISLockResult) (int64, error) {
	if lock == nil || strings.TrimSpace(lock.CycleHash) == "" {
		return 0, errors.New("missing InstantSend cycle hash")
	}
	block, err := b.h.client.rpc.GetBlockVerbose(lock.CycleHash)
	if err != nil {
		return 0, fmt.Errorf("fail to fetch InstantSend cycle block: %w", err)
	}
	if block.Height <= 0 || block.Confirmations <= 0 || !strings.EqualFold(block.Hash, lock.CycleHash) {
		return 0, errors.New("InstantSend cycle block is not in the active chain")
	}
	return block.Height, nil
}

// refreshVaults invalidates unrelated transaction IDs when vaults change
// and prunes IDs that left the mempool. Only the mempool scanner owns this cache.
func (b dashInstantSendBackend) refreshVaults(mempool map[string]rpc.GetRawMempoolVerboseResult) {
	addresses, err := b.h.client.getAsgardAddress()
	if err != nil {
		b.h.client.log.Debug().Err(err).Msg("skip InstantSend output filter while vault addresses are unavailable")
		clear(b.h.ignored)
		b.h.vaults = nil
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
	if !slices.Equal(vaults, b.h.vaults) || b.h.client.cfg.MempoolTxIDCacheSize <= 0 {
		clear(b.h.ignored)
	}
	b.h.vaults = vaults
	for id := range b.h.ignored {
		if _, present := mempool[id]; !present {
			delete(b.h.ignored, id)
		}
	}
}

// mayPayVault excludes only transactions with clearly unrelated outputs.
// Missing vault data or uncertain script decoding leaves the normal scan in charge.
func (b dashInstantSendBackend) mayPayVault(tx *rpc.TxResult) bool {
	if len(b.h.vaults) == 0 || len(tx.Vout) == 0 {
		return true
	}
	for _, out := range tx.Vout {
		addresses := b.h.client.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 0 {
			// OP_RETURN cannot pay a vault; other undecoded scripts remain retryable.
			script, err := hex.DecodeString(out.ScriptPubKey.Hex)
			if err != nil || len(script) == 0 || script[0] != txscript.OP_RETURN {
				return true
			}
		}
		for _, address := range addresses {
			if _, found := slices.BinarySearch(b.h.vaults, strings.ToLower(address)); found {
				return true
			}
		}
	}
	return false
}

func (b dashInstantSendBackend) candidates(entries map[string]rpc.GetRawMempoolVerboseResult) ([]string, error) {
	return b.h.pendingCandidates(normalizeDashMempool(entries))
}

// scan collects a bounded set of locked mempool candidates, grouped by
// lock cycle height. Allowance checks and observation claims happen before delivery.
func (b dashInstantSendBackend) scan() ([]types.TxIn, error) {
	// Only Dash currently supports the lock RPCs used by this scan.
	if !b.h.client.GetChain().Equals(common.DASHChain) {
		return nil, nil
	}
	mempool, err := b.h.client.rpc.GetRawMempoolVerboseDASH()
	if err != nil {
		return nil, fmt.Errorf("fail to get %s mempool: %w", b.h.client.GetChain(), err)
	}
	b.refreshVaults(mempool)
	ids, err := b.candidates(mempool)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	workers := b.h.client.cfg.ParallelMempoolScan
	if workers <= 0 {
		workers = 5
	}
	// Indexed results avoid shared slice mutation and retain candidate order.
	raw := make([]*rpc.TxResult, len(ids))
	var group errgroup.Group
	group.SetLimit(workers)
	for i, id := range ids {
		group.Go(func() error {
			tx, rpcErr := b.h.client.rpc.GetRawTransactionVerbose(id)
			if rpcErr != nil {
				b.h.client.log.Debug().Err(rpcErr).Str("txid", id).Msg("retry InstantSend mempool transaction later")
			} else if strings.EqualFold(tx.Txid, id) && tx.InstantSendLockedDASH() {
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
		if !b.mayPayVault(tx) {
			if len(b.h.ignored) < b.h.client.cfg.MempoolTxIDCacheSize {
				if b.h.ignored == nil {
					b.h.ignored = make(map[string]struct{})
				}
				b.h.ignored[ids[i]] = struct{}{}
			}
			continue
		}
		lockedIDs = append(lockedIDs, ids[i])
		lockedTxs = append(lockedTxs, tx)
	}
	locks, err := b.h.client.rpc.GetISLocksDASH(lockedIDs)
	if err != nil {
		// Successful chunks can still progress. Failed chunks have nil entries.
		b.h.client.log.Debug().Err(err).Msg("retry missing InstantSend lock batches later")
	}
	cycleHeights := make(map[string]int64)
	items := make([]*types.TxInItem, len(locks))
	for i, lock := range locks {
		if lock == nil {
			continue
		}
		anchor, found := cycleHeights[lock.CycleHash]
		if !found {
			anchor, err = b.observationHeight(lock)
			if err != nil {
				b.h.client.log.Debug().Err(err).Str("txid", lock.TxID).Msg("retry InstantSend cycle lookup later")
				continue
			}
			cycleHeights[lock.CycleHash] = anchor
		}
		group.Go(func() error {
			items[i] = b.buildTxInItem(lockedTxs[i], anchor, mempool[lockedIDs[i]])
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
	return instantSendObservationGroups(b.h.client.GetChain(), byHeight), nil
}

// buildTxInItem extracts a vault inbound at the lock cycle height.
// It returns nil if extraction fails or the recipient, dust, or age checks reject it.
func (b dashInstantSendBackend) buildTxInItem(tx *rpc.TxResult, anchor int64, entry rpc.GetRawMempoolVerboseResult) *types.TxInItem {
	item, err := b.h.client.getTxIn(&tx.TxRawResult, anchor, true, nil)
	if err != nil {
		b.h.client.log.Debug().Err(err).Str("txid", tx.Txid).Msg("retry InstantSend inbound extraction later")
		return nil
	}
	if item.IsEmpty() || item.Coins.IsEmpty() {
		return nil
	}
	if !b.h.client.isAsgardAddress(item.To) {
		return nil
	}
	if item.Coins[0].Amount.LT(b.h.client.cfg.ChainID.DustThreshold()) {
		return nil
	}
	var age time.Duration
	if entry.Time > 0 {
		age = time.Since(time.Unix(entry.Time, 0))
	}
	allow, reason := b.h.allowAge(age)
	if !allow {
		b.h.client.log.Debug().Str("txid", item.Tx).Str("reason", reason).Msg("InstantSend transaction waiting for block observation")
		return nil
	}
	return &item
}

// snapshotAmount returns the customer deposit amount to reserve during
// startup, in duffs. Vault senders are exempt; current minimum and age limits do not apply.
func (b dashInstantSendBackend) snapshotAmount(tx *rpc.TxResult) (uint64, error) {
	if !tx.InstantSendLockedDASH() || len(tx.Vin) == 0 {
		return 0, nil
	}
	// Avoid fetching parents for unrelated mempool traffic.
	relevant := false
	for _, out := range tx.Vout {
		addresses := b.h.client.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 1 && b.h.client.isAsgardAddress(addresses[0]) {
			relevant = true
			break
		}
	}
	if !relevant {
		return 0, nil
	}
	sender, err := b.h.client.getSender(&tx.TxRawResult, nil)
	if errors.Is(err, errInvalidSenderUTXO) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if b.h.client.isAsgardAddress(sender) {
		return 0, nil
	}
	out, err := b.h.client.getOutput(sender, &tx.TxRawResult, false)
	if errors.Is(err, btypes.ErrFailOutputMatchCriteria) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !b.h.client.isValidUTXO(out.ScriptPubKey.Hex) {
		return 0, nil
	}
	amount, err := btcutil.NewAmount(out.Value)
	if err != nil {
		return 0, err
	}
	if amount <= 0 || uint64(amount) < b.h.client.cfg.ChainID.DustThreshold().Uint64() {
		return 0, nil
	}
	return uint64(amount), nil
}

// getChainLockedBlockTxIDs verifies inclusion in a canonical ChainLocked block
// and returns its transaction IDs. It performs RPCs without changing the journal;
// call without stateMu held.
func (b dashInstantSendBackend) getChainLockedBlockTxIDs(id string, tx *rpc.TxResult) ([]string, error) {
	if tx == nil || !strings.EqualFold(id, tx.Txid) {
		return nil, errors.New("InstantSend transaction ID mismatch")
	}
	if !tx.ChainLock {
		return nil, nil
	}
	if tx.BlockHash == "" {
		return nil, errors.New("ChainLocked transaction has no block hash")
	}
	block, err := b.h.client.rpc.GetBlockVerboseTxs(tx.BlockHash)
	if err != nil {
		return nil, err
	}
	if !block.ChainLock || block.Height < 0 || !strings.EqualFold(block.Hash, tx.BlockHash) {
		return nil, errors.New("invalid ChainLocked transaction block")
	}
	hash, err := b.h.client.rpc.GetBlockHash(block.Height)
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

func (b dashInstantSendBackend) mempool() (map[string]instantSendMempoolEntry, error) {
	entries, err := b.h.client.rpc.GetRawMempoolVerboseDASH()
	if err != nil || entries == nil {
		return nil, err
	}
	return normalizeDashMempool(entries), nil
}

func normalizeDashMempool(entries map[string]rpc.GetRawMempoolVerboseResult) map[string]instantSendMempoolEntry {
	result := make(map[string]instantSendMempoolEntry, len(entries))
	for id, entry := range entries {
		status := instantSendUnknown
		switch entry.InstantLock {
		case "true":
			status = instantSendLocked
		case "false":
			status = instantSendUnlocked
		}
		result[id] = instantSendMempoolEntry{time: entry.Time, lock: status}
	}
	return result
}

func (b dashInstantSendBackend) transaction(id string) (*rpc.TxResult, error) {
	return b.h.client.rpc.GetRawTransactionVerbose(id)
}

func (b dashInstantSendBackend) transactions(ids []string) ([]*rpc.TxResult, []error, error) {
	return b.h.client.rpc.BatchGetRawTransactionVerbose(ids)
}
func (b dashInstantSendBackend) outbounds(height int64) (types.TxIn, error) {
	return b.h.client.fetchMemPoolTransactions(height, true)
}

func (b dashInstantSendBackend) checkClaimFinality(item *types.TxInItem, tx *rpc.TxResult) (valid, fromVault bool, err error) {
	if tx.ChainLock || !tx.InstantSendLockedDASH() {
		return false, false, nil
	}
	return true, b.h.client.isAsgardAddress(item.Sender), nil
}
