package evm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/syndtr/goleveldb/leveldb"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

var errOccurrenceWorkPending = stypes.ErrObservationFinalizationPending

// occurrenceFinalization resumes a bounded completion, not a new delivery.
// The canonical anchor, stored/skip outcome and transaction revision must all
// match before a later page can reuse the cursor.
type occurrenceFinalization struct {
	Height           int64                       `json:"height"`
	BlockHash        string                      `json:"block_hash"`
	Delivered        bool                        `json:"delivered"`
	ObserverHold     bool                        `json:"observer_hold,omitempty"`
	AttributionWait  *AttributionWait            `json:"attribution_wait,omitempty"`
	VaultAttribution []evmtypes.VaultAttribution `json:"vault_attribution,omitempty"`
	Revision         uint64                      `json:"revision"`
	After            []byte                      `json:"after,omitempty"`
	Done             bool                        `json:"done"`
}

// A large same-transaction or same-height history completes in bounded pages.
// Progress, source retirement and record updates are atomic. A changed obligation
// invalidates the continuation; a restart resumes only a matching canonical attempt.
func (j *OccurrenceJournal) finalizePageLocked(height int64, blockHash string, txIn stypes.TxIn, delivered bool) (bool, error) {
	batch := new(leveldb.Batch)
	seen := make(map[string]bool)
	removed := make(map[string]evmtypes.TransactionMeta)
	pending := false
	budget := occurrenceWorkPage
	progressKeys := make([][]byte, 0, len(txIn.TxArray))
	hashes := make(map[ecommon.Hash]bool)
	waiting := make(map[ecommon.Hash]*AttributionWait)
	proofs := make(map[ecommon.Hash][]evmtypes.VaultAttribution)
	for _, item := range txIn.TxArray {
		if item != nil && !item.EVMFailedSynthetic {
			hash := ecommon.HexToHash(item.Tx)
			proofs[hash] = mergeVaultAttribution(proofs[hash], item.VaultAttribution)
		}
		if item != nil && item.EVMAttributionPending {
			waiting[ecommon.HexToHash(item.Tx)] = &AttributionWait{Height: item.BlockHeight, BlockHash: item.BlockHash, Sender: item.Sender, To: item.To, RequiredMigration: item.RequiredMigration, LookupAddresses: append([]string(nil), item.EVMAttributionAddresses...), LookupActiveOnly: maps.Clone(item.EVMAttributionActiveOnly), LookupContracts: append([]string(nil), item.EVMAttributionContracts...)}
		}
	}
	process := func(record OccurrenceRequest, found bool, held bool) error {
		id := string(j.key(record.Original))
		if seen[id] {
			return nil
		}
		seen[id] = true
		if !record.Cleaned {
			return errors.New("recovery completion waits for cleanup")
		}
		if err := j.mutableLocked(record.Original); err != nil {
			return err
		}
		wait := waiting[ecommon.HexToHash(record.Original.Hash)]
		if !found && wait == nil && record.AttributionWait != nil && record.NeedsRescan && record.RescanHeight == height {
			// A newly located block can produce no applicable payload yet; keep
			// the prior lookup hints without treating them as a delivered view.
			copy := *record.AttributionWait
			copy.Height, copy.BlockHash = height, blockHash
			wait = &copy
		}
		if wait != nil {
			record.Original.Vaults = mergeVaultAttribution(record.Original.Vaults, proofs[ecommon.HexToHash(record.Original.Hash)])
			copy := *wait
			record.AttributionWait = &copy
			record.NeedsRescan = false
			record.RescanHeight = height
			record.CompletedRescanHash = blockHash
			record.SkippedCanonical = false
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			batch.Put([]byte(id), data)
			return nil
		}
		observerHeld := found && txIn.ObservationHeld != nil && txIn.ObservationHeld(record.Original.Hash)
		// A later inactive attempt cannot settle a prior signature-history hold.
		observerHeld = observerHeld || (!delivered && record.ObserverHold)
		if observerHeld {
			record.ObserverHold = true
			record.NeedsRescan = false
			record.RescanHeight = height
			record.CompletedRescanHash = blockHash
			record.SkippedCanonical = false
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			batch.Put([]byte(id), data)
			return nil
		}
		if record.Original.Hash == "" || (found && delivered && !held) {
			batch.Delete([]byte(id))
			if record.Original.BlockHeight != height || !strings.EqualFold(record.Original.BlockHash, blockHash) {
				removed[id] = record.Original
			}
			return nil
		}
		if (record.NeedsRescan && record.RescanHeight == height) || (found && !delivered && !held) {
			record.Original.Vaults = mergeVaultAttribution(record.Original.Vaults, proofs[ecommon.HexToHash(record.Original.Hash)])
			record.NeedsRescan = false
			record.RescanHeight = height
			record.CompletedRescanHash = blockHash
			record.SkippedCanonical = found && !delivered && !held
			if found && !held {
				record.AttributionWait = nil
			}
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			batch.Put([]byte(id), data)
		}
		return nil
	}
	for _, item := range txIn.TxArray {
		if item == nil || item.EVMFailedSynthetic || item.BlockHash == "" {
			continue
		}
		hash := ecommon.HexToHash(item.Tx)
		if hashes[hash] {
			continue
		}
		hashes[hash] = true
		held, err := j.hasTxFact("held", item.Tx)
		if err != nil {
			return false, err
		}
		if held && !item.EVMAttributionPending {
			continue
		}
		if budget == 0 {
			pending = true
			break
		}
		key := j.txIndex("finalization", item.Tx)
		revision, err := j.txRevision(item.Tx)
		if err != nil {
			return false, err
		}
		progress := occurrenceFinalization{Height: height, BlockHash: blockHash, Delivered: delivered, ObserverHold: txIn.ObservationHeld != nil && txIn.ObservationHeld(item.Tx), Revision: revision, AttributionWait: waiting[hash], VaultAttribution: append([]evmtypes.VaultAttribution(nil), proofs[hash]...)}
		data, err := j.accessor.db.Get(key, nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return false, err
		}
		if err == nil {
			var previous occurrenceFinalization
			if err = json.Unmarshal(data, &previous); err != nil {
				return false, err
			}
			if previous.Height == height && strings.EqualFold(previous.BlockHash, blockHash) && previous.Delivered == delivered && previous.ObserverHold == progress.ObserverHold && previous.Revision == revision && reflect.DeepEqual(previous.AttributionWait, progress.AttributionWait) && reflect.DeepEqual(previous.VaultAttribution, progress.VaultAttribution) {
				progress = previous
			}
		}
		progressKeys = append(progressKeys, key)
		if progress.Done {
			continue
		}
		records, last, err := j.indexedRecordsLocked(j.txIndex("tx", item.Tx), budget, progress.After)
		if err != nil {
			return false, err
		}
		progress.Done = len(records) < budget
		if len(last) > 0 {
			progress.After = last
		}
		budget -= len(records)
		for _, record := range records {
			if err = process(record, true, false); err != nil {
				return false, err
			}
		}
		data, err = json.Marshal(progress)
		if err != nil {
			return false, err
		}
		batch.Put(key, data)
		pending = pending || !progress.Done
	}
	if budget > 0 {
		records, _, err := j.indexedRecordsLocked(append(j.indexPrefix("rescan"), fmt.Sprintf("%020d/", height)...), budget, nil)
		if err != nil {
			return false, err
		}
		pending = pending || len(records) == budget
		for _, record := range records {
			held, heldErr := j.hasTxFact("held", record.Original.Hash)
			if heldErr != nil {
				return false, heldErr
			}
			if err = process(record, false, held); err != nil {
				return false, err
			}
		}
	} else {
		pending = true
	}
	if err := j.pruneFinalizedSourcesLocked(batch, removed); err != nil {
		return false, err
	}
	if !pending {
		for _, key := range progressKeys {
			batch.Delete(key)
		}
		if err := j.advanceMatcherTargetLocked(batch, height); err != nil {
			return false, err
		}
	}
	if batch.Len() > 0 {
		if err := j.write(batch); err != nil {
			return false, err
		}
	}
	return pending, nil
}

// pruneFinalizedSourcesLocked removes exact old metadata entries with their journal
// completion. Leaving the source behind would recreate finished work on reconciliation.
func (j *OccurrenceJournal) pruneFinalizedSourcesLocked(batch *leveldb.Batch, removed map[string]evmtypes.TransactionMeta) error {
	metas := make(map[int64]*evmtypes.BlockMeta)
	for _, original := range removed {
		if _, loaded := metas[original.BlockHeight]; loaded {
			continue
		}
		meta, err := j.accessor.GetBlockMeta(original.BlockHeight)
		if err != nil {
			return err
		}
		metas[original.BlockHeight] = meta
	}
	for height, meta := range metas {
		if meta == nil {
			continue
		}
		kept := meta.Transactions[:0]
		for _, tx := range meta.Transactions {
			tx.BlockHeight = height
			if _, done := removed[string(j.key(tx))]; !done {
				kept = append(kept, tx)
			}
		}
		if len(kept) == len(meta.Transactions) {
			continue
		}
		meta.Transactions = kept
		data, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		batch.Put([]byte(j.accessor.getBlockMetaKey(height)), data)
	}
	return nil
}

// Reorg classification admits all displaced evidence atomically. There is no
// overflow store and no capacity-dependent barrier on replacement extraction.
func (j *OccurrenceJournal) retainClassification(metas []*evmtypes.BlockMeta, targets map[int64]string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	batch := new(leveldb.Batch)
	for _, meta := range metas {
		current, err := j.accessor.GetBlockMeta(meta.Height)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(meta, current) {
			return errors.New("metadata changed during reorg classification")
		}
		if target, changed := targets[meta.Height]; changed {
			if err = j.retainReplacedMetaLocked(batch, meta, &evmtypes.BlockMeta{Height: meta.Height, BlockHash: target}); err != nil {
				return err
			}
		}
	}
	if batch.Len() == 0 {
		return nil
	}
	// Attach the actual classification fork before registration can move the
	// matcher; a previous unrelated fork must not send this recovery to old history.
	fork := int64(^uint64(0) >> 1)
	for height := range targets {
		fork = min(fork, height)
	}
	changes := make(occurrenceBatchChanges)
	if err := batch.Replay(changes); err != nil {
		return err
	}
	for key, data := range changes {
		var record OccurrenceRequest
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.SearchStartHeight == nil || fork < *record.SearchStartHeight {
			v := fork
			record.SearchStartHeight = &v
		}
		value, err := json.Marshal(record)
		if err != nil {
			return err
		}
		batch.Put([]byte(key), value)
	}
	return j.write(batch)
}

// pruneTerminalSourceLocked stages removal of only the resolved source identity.
// A replacement at the same height survives, and source retirement shares the
// terminal journal write so a restart cannot resurrect the old obligation.
func (j *OccurrenceJournal) pruneTerminalSourceLocked(batch *leveldb.Batch, original evmtypes.TransactionMeta) error {
	meta, err := j.accessor.GetBlockMeta(original.BlockHeight)
	if err != nil || meta == nil {
		return err
	}
	kept := make([]evmtypes.TransactionMeta, 0, len(meta.Transactions))
	for _, tx := range meta.Transactions {
		tx.BlockHeight = meta.Height
		if string(j.key(tx)) != string(j.key(original)) {
			kept = append(kept, tx)
		}
	}
	if len(kept) == len(meta.Transactions) {
		return nil
	}
	meta.Transactions = kept
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	batch.Put([]byte(j.accessor.getBlockMetaKey(meta.Height)), data)
	return nil
}
