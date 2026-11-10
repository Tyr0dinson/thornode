package evm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const occurrenceWorkPage = 256

func (j *OccurrenceJournal) indexPrefix(kind string) []byte {
	return []byte("evm-index-v1/" + strings.ToLower(j.chain.String()) + "/" + kind + "/")
}

// recordIndexes derives scheduling and lookup keys from one authoritative record.
// Fixed-width heights preserve numeric order; cleanup, rescan and watch eligibility
// are encoded here so their readers do not scan the whole journal.
func (j *OccurrenceJournal) recordIndexes(record OccurrenceRequest) [][]byte {
	id := string(j.key(record.Original))
	order := fmt.Sprintf("%020d/%s/%s", record.Original.BlockHeight, strings.ToLower(record.Original.BlockHash), id)
	keys := [][]byte{append(j.indexPrefix("age"), fmt.Sprintf("%020d/%s", record.CreatedAt, id)...)}
	start := record.Original.BlockHeight
	if record.SearchStartHeight != nil {
		start = min(start, *record.SearchStartHeight)
	}
	keys = append(keys, append(j.indexPrefix("search-start"), fmt.Sprintf("%020d/%s", start, id)...))
	if record.Original.Hash != "" {
		keys = append(keys, append(j.indexPrefix("tx"), ecommon.HexToHash(record.Original.Hash).Hex()+"/"+id...))
		if record.Original.Sender != "" && record.Original.Nonce != nil {
			keys = append(keys, append(j.indexPrefix("nonce"), fmt.Sprintf("%s/%020d/%s", strings.ToLower(record.Original.Sender), *record.Original.Nonce, id)...))
		}
	}
	if !record.Cleaned {
		keys = append(keys, append(j.indexPrefix("cleanup"), order...))
	}
	if record.Cleaned && record.NeedsRescan {
		keys = append(keys, append(j.indexPrefix("rescan"), fmt.Sprintf("%020d/%s", record.RescanHeight, id)...))
	}
	if record.Cleaned && !record.NeedsRescan && record.Original.Hash != "" {
		keys = append(keys, append(j.indexPrefix("watch"), order...))
	}
	if record.Cleaned && !record.NeedsRescan && record.AttributionWait != nil {
		keys = append(keys, append(j.indexPrefix("attribution-wait"), order...))
	}

	return append(keys, j.factIndexes(record)...)
}

type occurrenceBatchChanges map[string][]byte

func (c occurrenceBatchChanges) Put(key, value []byte) { c[string(key)] = bytes.Clone(value) }

func (c occurrenceBatchChanges) Delete(key []byte) { c[string(key)] = nil }

// All journal and metadata transitions pass through this writer. Derived keys
// and authoritative records share the same synchronous LevelDB batch.
func (j *OccurrenceJournal) writeIndexed(batch *leveldb.Batch) error {
	changes := make(occurrenceBatchChanges)
	if err := batch.Replay(changes); err != nil {
		return err
	}
	var registration *int64
	for key, value := range changes {
		if !strings.HasPrefix(key, j.prefix) && !strings.HasPrefix(key, j.accessor.PrefixBlockMeta) {
			continue
		}
		old, err := j.accessor.db.Get([]byte(key), nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return err
		}
		if strings.HasPrefix(key, j.prefix) && len(value) > 0 {
			var after OccurrenceRequest
			if err = json.Unmarshal(value, &after); err != nil {
				return err
			}
			if after.CreatedAt == 0 {
				var before OccurrenceRequest
				if len(old) > 0 {
					if err = json.Unmarshal(old, &before); err != nil {
						return err
					}
				}
				after.CreatedAt = before.CreatedAt
				if after.CreatedAt == 0 {
					after.CreatedAt = time.Now().Unix()
				}
				value, err = json.Marshal(after)
				if err != nil {
					return err
				}
				changes[key] = value
				batch.Put([]byte(key), value)
			}
		}
		if err = j.updateIndexes(batch, key, old, false); err != nil {
			return err
		}
		if err = j.updateIndexes(batch, key, value, true); err != nil {
			return err
		}
		if strings.HasPrefix(key, j.prefix) && len(value) > 0 {
			var before, after OccurrenceRequest
			if len(old) > 0 {
				if err = json.Unmarshal(old, &before); err != nil {
					return err
				}
			}
			if err = json.Unmarshal(value, &after); err != nil {
				return err
			}
			if len(old) == 0 || (before.Original.Sender == "" && after.Original.Sender != "") {
				height := after.Original.BlockHeight
				if after.SearchStartHeight != nil {
					height = min(height, *after.SearchStartHeight)
				}
				if len(old) > 0 {
					state, stateErr := j.matcherLocked()
					if stateErr != nil {
						return stateErr
					}
					if state.HasFork {
						height = min(height, state.ForkHeight)
					}
				}
				if registration == nil || height < *registration {
					v := height
					registration = &v
				}
			}
		}
	}
	if registration != nil {
		if err := j.registerMatcherLocked(batch, *registration); err != nil {
			return err
		}
	}
	if err := j.updateFactsLocked(batch, changes); err != nil {
		return err
	}
	if err := j.updateStatistics(batch, changes); err != nil {
		return err
	}
	if err := j.accessor.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return err
	}
	return nil
}

// updateIndexes adds or removes the derived view of one authoritative value.
// Metadata-only evidence also stays visible until classification gives it a journal owner.
func (j *OccurrenceJournal) updateIndexes(batch *leveldb.Batch, key string, data []byte, put bool) error {
	if len(data) == 0 {
		return nil
	}
	apply := func(index, value []byte) {
		if put {
			batch.Put(index, value)
		} else {
			batch.Delete(index)
		}
	}
	if strings.HasPrefix(key, j.prefix) {
		var record OccurrenceRequest
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if key != string(j.key(record.Original)) {
			return errors.New("occurrence index identity mismatch")
		}
		for _, index := range j.recordIndexes(record) {
			apply(index, []byte(key))
		}
		return j.updateVaultIndexes(batch, record.Original, "record/"+key, put)
	}
	if strings.HasPrefix(key, j.accessor.PrefixBlockMeta) {
		var meta evmtypes.BlockMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			return err
		}
		for _, tx := range meta.Transactions {
			tx.BlockHeight = meta.Height
			if err := j.updateVaultIndexes(batch, tx, "meta/"+key, put); err != nil {
				return err
			}
			value, err := json.Marshal(tx)
			if err != nil {
				return err
			}
			index := append(j.indexPrefix("source"), ecommon.HexToHash(tx.Hash).Hex()+"/"+string(j.key(tx))...)
			apply(index, value)
			owned, err := j.accessor.db.Has(j.key(tx), nil)
			if err != nil {
				return err
			}
			unclassified := append(j.txIndex("unclassified", tx.Hash), string(j.key(tx))...)
			if put && !owned {
				batch.Put(unclassified, j.key(tx))
			} else if !put {
				batch.Delete(unclassified)
			}
		}
	}
	return nil
}

// First use upgrades existing local records in restartable bounded batches.
// Once initialized, reopening reads only the completed marker.
func (j *OccurrenceJournal) initializeIndexes() error {
	marker := j.indexPrefix("initialized")
	exists, err := j.accessor.db.Has(marker, nil)
	if err != nil || exists {
		return err
	}
	for _, prefix := range []string{j.prefix, j.accessor.PrefixBlockMeta} {
		checkpoint := append(j.indexPrefix("initializing"), prefix...)
		after, err := j.accessor.db.Get(checkpoint, nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return err
		}
		for {
			iter := j.accessor.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
			valid := iter.First()
			if len(after) > 0 {
				valid = iter.Seek(after)
				if valid && bytes.Equal(iter.Key(), after) {
					valid = iter.Next()
				}
			}
			batch := new(leveldb.Batch)
			state, stateErr := j.matcherLocked()
			if stateErr != nil {
				iter.Release()
				return stateErr
			}
			stats, statsErr := j.statisticsLocked()
			if statsErr != nil {
				iter.Release()
				return statsErr
			}
			count := 0
			for ; valid && count < occurrenceWorkPage; valid = iter.Next() {
				data := bytes.Clone(iter.Value())
				if prefix == j.prefix {
					var record OccurrenceRequest
					if err = json.Unmarshal(data, &record); err != nil {
						iter.Release()
						return err
					}
					if record.CreatedAt == 0 {
						record.CreatedAt = time.Now().Unix()
						data, err = json.Marshal(record)
						if err != nil {
							iter.Release()
							return err
						}
						batch.Put(iter.Key(), data)
					}
					stats.Records++
					stats.Bytes += int64(len(iter.Key()) + len(data))
				}
				if err = j.updateIndexes(batch, string(iter.Key()), data, true); err != nil {
					iter.Release()
					return err
				}
				if prefix == j.prefix {
					var record OccurrenceRequest
					if err = json.Unmarshal(iter.Value(), &record); err != nil {
						iter.Release()
						return err
					}
					height := record.Original.BlockHeight
					if record.SearchStartHeight != nil {
						height = min(height, *record.SearchStartHeight)
					}
					if !state.Initialized || height < state.Height {
						if err = rewindMatcher(&state, height); err != nil {
							iter.Release()
							return err
						}
						if err = j.saveMatcher(batch, state); err != nil {
							iter.Release()
							return err
						}
					}
				}
				after = bytes.Clone(iter.Key())
				count++
			}
			readErr := iter.Error()
			iter.Release()
			if readErr != nil {
				return readErr
			}
			if count == 0 {
				break
			}
			if prefix == j.prefix {
				if err = j.saveStatistics(batch, stats); err != nil {
					return err
				}
			}
			batch.Put(checkpoint, after)
			if err = j.accessor.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
				return err
			}
		}
	}
	return j.accessor.db.Put(marker, []byte{1}, &opt.WriteOptions{Sync: true})
}

// indexedRecordsLocked resumes strictly after an index key and verifies its identity.
// The returned cursor names the last visited key, not a position in a mutable array.
func (j *OccurrenceJournal) indexedRecordsLocked(prefix []byte, limit int, after []byte) ([]OccurrenceRequest, []byte, error) {
	iter := j.accessor.db.NewIterator(util.BytesPrefix(prefix), nil)
	defer iter.Release()
	valid := iter.First()
	if len(after) > 0 {
		valid = iter.Seek(after)
		if valid && bytes.Equal(iter.Key(), after) {
			valid = iter.Next()
		}
	}
	var records []OccurrenceRequest
	var last []byte
	for ; valid && (limit <= 0 || len(records) < limit); valid = iter.Next() {
		data, err := j.accessor.db.Get(iter.Value(), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("load indexed occurrence: %w", err)
		}
		var record OccurrenceRequest
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(iter.Value(), j.key(record.Original)) {
			return nil, nil, errors.New("indexed occurrence identity mismatch")
		}
		records = append(records, record)
		last = bytes.Clone(iter.Key())
	}
	return records, last, iter.Error()
}

// watchPage persists a rotating page and rotates its first RPC recipient each round.
// Unresolved early records therefore cannot monopolize the bounded probe budget.
func (j *OccurrenceJournal) watchPage() ([]OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cursorKey := j.indexPrefix("probe-cursor")
	var cursor struct {
		After []byte
		Round uint64
	}
	data, err := j.accessor.db.Get(cursorKey, nil)
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(data, &cursor); err != nil {
			return nil, err
		}
	}
	records, last, err := j.indexedRecordsLocked(j.indexPrefix("watch"), 16, cursor.After)
	if err == nil && len(records) == 0 && len(cursor.After) > 0 {
		cursor.Round++
		records, last, err = j.indexedRecordsLocked(j.indexPrefix("watch"), 16, nil)
	}
	if err != nil {
		return nil, err
	}
	if len(last) > 0 {
		cursor.After = last
		data, err = json.Marshal(cursor)
		if err != nil {
			return nil, err
		}
		batch := new(leveldb.Batch)
		batch.Put(cursorKey, data)
		if err = j.write(batch); err != nil {
			return nil, err
		}
		priority := int(cursor.Round % uint64(len(records)))
		records = append(records[priority:], records[:priority]...)
	}
	return records, nil
}

func (j *OccurrenceJournal) txIndex(kind, hash string) []byte {
	return append(j.indexPrefix(kind), ecommon.HexToHash(hash).Hex()+"/"...)
}

// OwnsAnyTransaction reports whether the journal holds an obligation for any of
// hashes. Such a transaction is extracted only through the receipt path, whose
// recovery attribution restores the vaults and routers the registry has lost.
func (j *OccurrenceJournal) OwnsAnyTransaction(hashes []string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	// Most blocks are scanned with no obligation at all: one seek answers them.
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.indexPrefix("tx")), nil)
	found := iter.First()
	err := iter.Error()
	iter.Release()
	if err != nil || !found {
		return false, err
	}
	for _, hash := range hashes {
		if owned, ownedErr := j.hasTxFact("tx", hash); ownedErr != nil || owned {
			return owned, ownedErr
		}
	}
	return false, nil
}

// OwnsAnyBlockTransaction reports whether a request owns any of block's
// transactions, see OwnsAnyTransaction.
func (j *OccurrenceJournal) OwnsAnyBlockTransaction(block *etypes.Block) (bool, error) {
	hashes := make([]string, 0, block.Transactions().Len())
	for _, tx := range block.Transactions() {
		if tx != nil {
			hashes = append(hashes, tx.Hash().Hex())
		}
	}
	return j.OwnsAnyTransaction(hashes)
}

// hasTxFact checks whether any occurrence carries this transaction-wide condition.
// A clean sibling cannot hide another occurrence's pending work or accounting hold.
func (j *OccurrenceJournal) hasTxFact(kind, hash string) (bool, error) {
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.txIndex(kind, hash)), nil)
	defer iter.Release()
	found := iter.First()
	return found, iter.Error()
}

func (j *OccurrenceJournal) firstTxRecord(kind, hash string) (*OccurrenceRequest, error) {
	records, _, err := j.indexedRecordsLocked(j.txIndex(kind, hash), 1, nil)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return &records[0], nil
}

// txRevision fences paged completion across all occurrences of a transaction.
// A new obligation can invalidate a continuation even when its own key is different.
func (j *OccurrenceJournal) txRevision(hash string) (uint64, error) {
	var revision uint64
	data, err := j.accessor.db.Get(j.txIndex("revision", hash), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	err = json.Unmarshal(data, &revision)
	return revision, err
}

// Source-only evidence is a blocker even before classification. Recompute its
// membership from the final authoritative values of this same atomic batch.
func (j *OccurrenceJournal) updateFactsLocked(batch *leveldb.Batch, changes occurrenceBatchChanges) error {
	affected := make(map[string]evmtypes.TransactionMeta)
	sources := make(map[string]bool)
	floors := make(map[string]int64)
	retainFloor := func(tx evmtypes.TransactionMeta) {
		if tx.Hash != "" && tx.ConfirmationRequired != nil && *tx.ConfirmationRequired >= 0 {
			hash := ecommon.HexToHash(tx.Hash).Hex()
			floors[hash] = max(floors[hash], *tx.ConfirmationRequired)
		}
	}
	revisions := make(map[string]bool)
	for key, value := range changes {
		if !strings.HasPrefix(key, j.prefix) && !strings.HasPrefix(key, j.accessor.PrefixBlockMeta) {
			continue
		}
		old, err := j.accessor.db.Get([]byte(key), nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return err
		}
		if bytes.Equal(old, value) {
			continue
		}
		if strings.HasPrefix(key, j.prefix) {
			changed, compareErr := finalizationChanged(old, value)
			if compareErr != nil {
				return compareErr
			}
			for _, data := range [][]byte{old, value} {
				if len(data) == 0 {
					continue
				}
				var record OccurrenceRequest
				if err = json.Unmarshal(data, &record); err != nil {
					return err
				}
				affected[string(j.key(record.Original))] = record.Original
				retainFloor(record.Original)
				if changed && record.Original.Hash != "" {
					revisions[ecommon.HexToHash(record.Original.Hash).Hex()] = true
				}
			}
		} else {
			for i, data := range [][]byte{old, value} {
				if len(data) == 0 {
					continue
				}
				var meta evmtypes.BlockMeta
				if err = json.Unmarshal(data, &meta); err != nil {
					return err
				}
				for _, tx := range meta.Transactions {
					tx.BlockHeight = meta.Height
					retainFloor(tx)
					id := string(j.key(tx))
					affected[id] = tx
					sources[id] = i == 1
				}
			}
		}
	}
	// Keep the strongest admitted floor beside the per-tx revision. Source
	// metadata can be pruned before an inactive skip becomes mature. One scalar
	// avoids rewriting unbounded same-tx occurrence lists when a floor rises.
	for hash, floor := range floors {
		if !revisions[hash] {
			owned, err := j.accessor.db.Has(j.txIndex("revision", hash), nil)
			if err != nil {
				return err
			}
			if !owned {
				owned, err = j.hasTxFact("tx", hash)
				if err != nil {
					return err
				}
			}
			if !owned {
				continue
			} // ordinary observations do not acquire permanent recovery keys
		}
		old, err := j.retainedFloor(hash)
		if err != nil {
			return err
		}
		if floor > old {
			data, marshalErr := json.Marshal(floor)
			if marshalErr != nil {
				return marshalErr
			}
			batch.Put(j.txIndex("retained-floor", hash), data)
		}
	}
	for id, original := range affected {
		source, known := sources[id]
		if !known {
			var err error
			source, err = j.accessor.db.Has(append(j.txIndex("source", original.Hash), id...), nil)
			if err != nil {
				return err
			}
		}
		value, changed := changes[id]
		record := len(value) > 0
		if !changed {
			var err error
			record, err = j.accessor.db.Has([]byte(id), nil)
			if err != nil {
				return err
			}
		}
		key := append(j.txIndex("unclassified", original.Hash), id...)
		if source && !record {
			batch.Put(key, []byte(id))
		} else {
			batch.Delete(key)
		}
	}
	for hash := range revisions {
		revision, err := j.txRevision(hash)
		if err != nil {
			return err
		}
		if revision == ^uint64(0) {
			return errors.New("recovery revision exhausted")
		}
		revision++
		data, err := json.Marshal(revision)
		if err != nil {
			return err
		}
		batch.Put(j.txIndex("revision", hash), data)
		// Own partial-finalization writes advance the continuation's fence together.
		progressKey := string(j.txIndex("finalization", hash))
		if progressData, ok := changes[progressKey]; ok && len(progressData) > 0 {
			var progress occurrenceFinalization
			if err = json.Unmarshal(progressData, &progress); err != nil {
				return err
			}
			progress.Revision = revision
			data, err = json.Marshal(progress)
			if err != nil {
				return err
			}
			batch.Put([]byte(progressKey), data)
		}
	}
	return nil
}

// terminalTxReady combines every occurrence's blockers and strongest retained floor.
// The optional canonical source exempts only that exact metadata identity from the
// unclassified-evidence hold; unrelated old evidence still prevents retirement.
func (j *OccurrenceJournal) terminalTxReady(hash string, proof CanonicalEvidence, required int64, canonicalSource ...string) (bool, error) {
	retained, floorErr := j.retainedFloor(hash)
	if floorErr != nil {
		return false, floorErr
	}
	required = max(required, retained)

	if len(canonicalSource) > 0 {
		it := j.accessor.db.NewIterator(util.BytesPrefix(j.txIndex("unclassified", hash)), nil)
		for it.Next() {
			if string(it.Value()) != canonicalSource[0] {
				it.Release()
				return false, nil
			}
		}
		err := it.Error()
		it.Release()
		if err != nil {
			return false, err
		}
	} else {
		found, err := j.hasTxFact("unclassified", hash)
		if err != nil || found {
			return false, err
		}
	}
	for _, kind := range []string{"unclean", "pending", "held", "unknown-floor"} {
		found, err := j.hasTxFact(kind, hash)
		if err != nil || found {
			return false, err
		}
	}
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.txIndex("floor", hash)), nil)
	defer iter.Release()
	if !iter.Last() {
		return false, iter.Error()
	}
	data, err := j.accessor.db.Get(iter.Value(), nil)
	if err != nil {
		return false, err
	}
	var record OccurrenceRequest
	if err = json.Unmarshal(data, &record); err != nil {
		return false, err
	}
	return ConflictConfirmed(record.Original, proof.Height, proof.TipHeight, required), iter.Error()
}

// factIndexes makes transaction-wide safety predicates queryable without loading
// all siblings. Unknown policy remains a blocker, distinct from a known zero floor.
func (j *OccurrenceJournal) factIndexes(record OccurrenceRequest) [][]byte {
	if record.Original.Hash == "" {
		return nil
	}
	id := string(j.key(record.Original))
	hash := record.Original.Hash
	var keys [][]byte
	add := func(kind string) { keys = append(keys, append(j.txIndex(kind, hash), id...)) }
	if !record.Cleaned {
		add("unclean")
	}
	if record.NeedsRescan {
		add("pending")
	}
	if record.HeldFailedReplay != nil {
		add("held")
	}
	if record.Original.ConfirmationRequired == nil || *record.Original.ConfirmationRequired < 0 {
		add("unknown-floor")
	} else {
		keys = append(keys, append(j.txIndex("floor", hash), fmt.Sprintf("%020d/%s", *record.Original.ConfirmationRequired, id)...))
	}
	return keys
}

// Background search does not change what a delivery may retire. Only changes
// to the obligation, its accounting/floor or its completion state reset paging.
func finalizationChanged(before, after []byte) (bool, error) {
	if len(before) == 0 || len(after) == 0 {
		return true, nil
	}
	var old, current OccurrenceRequest
	if err := json.Unmarshal(before, &old); err != nil {
		return false, err
	}
	if err := json.Unmarshal(after, &current); err != nil {
		return false, err
	}
	clearSearch := func(record *OccurrenceRequest) {
		record.SearchHeight = 0
		record.SearchStartHeight = nil
		record.Original.Sender = ""
		record.Original.Nonce = nil
	}
	clearSearch(&old)
	clearSearch(&current)
	return !reflect.DeepEqual(old, current), nil
}

// This scalar is monotone like the already retained transaction revision. It
// never proves an unknown original floor, and does not mark delivery complete.
func (j *OccurrenceJournal) retainedFloor(hash string) (int64, error) {
	data, err := j.accessor.db.Get(j.txIndex("retained-floor", hash), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var floor int64
	if err = json.Unmarshal(data, &floor); err != nil {
		return 0, err
	}
	if floor < 0 {
		return 0, errors.New("negative retained recovery floor")
	}
	return floor, nil
}

type occurrenceStatistics struct {
	ObserverHolds int64 `json:"observer_holds,omitempty"`
	Records       int64 `json:"records"`
	Bytes         int64 `json:"bytes"`
}

func (j *OccurrenceJournal) statisticsLocked() (occurrenceStatistics, error) {
	var stats occurrenceStatistics
	data, err := j.accessor.db.Get(j.indexPrefix("statistics"), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return stats, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &stats)
	}
	return stats, err
}

func (j *OccurrenceJournal) saveStatistics(batch *leveldb.Batch, stats occurrenceStatistics) error {
	if stats.Records < 0 || stats.Bytes < 0 || stats.ObserverHolds < 0 {
		return errors.New("invalid recovery statistics")
	}
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	batch.Put(j.indexPrefix("statistics"), data)
	return nil
}

// updateStatistics applies only primary-record deltas in the same write as the data.
// Metrics must not add a full journal scan to every recovery pass.
func (j *OccurrenceJournal) updateStatistics(batch *leveldb.Batch, changes occurrenceBatchChanges) error {
	var count, bytes, holds int64
	for key, value := range changes {
		if !strings.HasPrefix(key, j.prefix) {
			continue
		}
		old, err := j.accessor.db.Get([]byte(key), nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return err
		}
		if len(old) > 0 {
			var record OccurrenceRequest
			if err := json.Unmarshal(old, &record); err != nil {
				return err
			}
			if record.ObserverHold || record.HeldFailedReplay != nil {
				holds--
			}
			count--
			bytes -= int64(len(key) + len(old))
		}
		if len(value) > 0 {
			var record OccurrenceRequest
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.ObserverHold || record.HeldFailedReplay != nil {
				holds++
			}
			count++
			bytes += int64(len(key) + len(value))
		}
	}
	if count == 0 && bytes == 0 && holds == 0 {
		return nil
	}
	stats, err := j.statisticsLocked()
	if err != nil {
		return err
	}
	stats.ObserverHolds += holds
	stats.Records += count
	stats.Bytes += bytes
	return j.saveStatistics(batch, stats)
}

// Metrics read a counter and the first age index entry, never the backlog.
func (j *OccurrenceJournal) ReportStorage() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	stats, err := j.statisticsLocked()
	if err != nil {
		return err
	}
	records, _, err := j.indexedRecordsLocked(j.indexPrefix("age"), 1, nil)
	if err != nil {
		return err
	}
	oldest := int64(0)
	if len(records) > 0 {
		oldest = records[0].CreatedAt
	}
	metrics.SetRecoveryStorage(j.chain.String(), "evm", stats.Records, stats.Bytes)
	metrics.SetEVMRecoveryOldest(j.chain.String(), oldest)
	metrics.SetRecoveryObserverHolds(j.chain.String(), "evm", stats.ObserverHolds)
	return nil
}

// Attribution indexes are maintained alongside their source record. Metadata and
// journal references use distinct suffixes so retiring one cannot erase the other.
func (j *OccurrenceJournal) updateVaultIndexes(batch *leveldb.Batch, tx evmtypes.TransactionMeta, source string, put bool) error {
	for _, vault := range tx.Vaults {
		address, err := vault.PubKey.GetAddress(j.chain)
		if err != nil || address.IsEmpty() {
			return errors.New("invalid retained vault attribution")
		}
		key := append(j.txIndex("vault", tx.Hash), strings.ToLower(address.String())+"/"+source...)
		if !put {
			batch.Delete(key)
			if !vault.Router.IsEmpty() {
				batch.Delete(append(j.txIndex("vault-router", tx.Hash), strings.ToLower(vault.Router.String())+"/"+source...))
			}
			continue
		}
		value, err := json.Marshal(vault)
		if err != nil {
			return err
		}
		batch.Put(key, value)
		if !vault.Router.IsEmpty() {
			routerKey := append(j.txIndex("vault-router", tx.Hash), strings.ToLower(vault.Router.String())+"/"+source...)
			batch.Put(routerKey, value)
		}
	}
	return nil
}

// RecoveryVault restores only a recovery-owned transaction's known endpoint.
// Seeking the address index reads one reference regardless of backlog size; old
// occurrences and metadata keep it alive until their durable evidence retires.
func (j *OccurrenceJournal) RecoveryVault(hash, address string) (*evmtypes.VaultAttribution, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	owned, err := j.hasTxFact("tx", hash)
	if err != nil || !owned {
		return nil, err
	}
	prefix := append(j.txIndex("vault", hash), strings.ToLower(address)+"/"...)
	iterator := j.accessor.db.NewIterator(util.BytesPrefix(prefix), nil)
	defer iterator.Release()
	if !iterator.First() {
		return nil, iterator.Error()
	}
	var vault evmtypes.VaultAttribution
	if err = json.Unmarshal(iterator.Value(), &vault); err != nil {
		return nil, err
	}
	derived, err := vault.PubKey.GetAddress(j.chain)
	if err != nil || derived.IsEmpty() || !strings.EqualFold(derived.String(), address) {
		return nil, errors.New("retained vault attribution does not match endpoint")
	}
	return &vault, nil
}

// RecoveryObservationVaults exposes local attribution, never a signing proof or
// completion decision. Observer independently checks each derived address.
func (j *OccurrenceJournal) RecoveryObservationVaults(item stypes.TxInItem) (common.PubKeys, error) {
	var result common.PubKeys
	for _, address := range []string{item.Sender, item.To} {
		vault, err := j.RecoveryVault(item.Tx, address)
		if err != nil {
			return nil, err
		}
		if vault != nil && !result.Contains(vault.PubKey) {
			result = append(result, vault.PubKey)
		}
	}
	return result, nil
}

// recoveryRouter tests a specific log-emitting contract without loading the
// transaction's retained history. The source-specific index has the same lifetime
// and atomic deletion rules as the address attribution index.
func (j *OccurrenceJournal) recoveryRouter(hash, address string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	owned, err := j.hasTxFact("tx", hash)
	if err != nil || !owned {
		return false, err
	}
	iterator := j.accessor.db.NewIterator(util.BytesPrefix(append(j.txIndex("vault-router", hash), strings.ToLower(address)+"/"...)), nil)
	defer iterator.Release()
	return iterator.First(), iterator.Error()
}

// attributionPage persists a fair iterator position without materializing the
// backlog. A permanently unknown early vault cannot starve later ready records.
func (j *OccurrenceJournal) attributionPage() ([]OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	key := j.indexPrefix("attribution-cursor")
	after, err := j.accessor.db.Get(key, nil)
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return nil, err
	}
	records, last, err := j.indexedRecordsLocked(j.indexPrefix("attribution-wait"), 16, after)
	if err == nil && len(records) == 0 && len(after) > 0 {
		records, last, err = j.indexedRecordsLocked(j.indexPrefix("attribution-wait"), 16, nil)
	}
	if err != nil {
		return nil, err
	}
	if len(last) > 0 {
		batch := new(leveldb.Batch)
		batch.Put(key, last)
		if err = j.write(batch); err != nil {
			return nil, err
		}
	}
	return records, nil
}
