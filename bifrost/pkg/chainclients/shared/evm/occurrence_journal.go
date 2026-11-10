package evm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// ErrRecoveryAttribution marks an unavailable local authority lookup. Unlike a
// rejected transaction payload, it must fail extraction and preserve the retry.
var ErrRecoveryAttribution = errors.New("recovery attribution unavailable")

// AttributionWait retains only the local lookup endpoints of a canonical replay
// whose vault membership is unavailable. It is neither stored delivery nor an
// accounting hold, and never supplies authority for a later observation.
type AttributionWait struct {
	Height            int64  `json:"height"`
	BlockHash         string `json:"block_hash"`
	Sender            string `json:"sender"`
	To                string `json:"to"`
	RequiredMigration bool   `json:"required_migration,omitempty"`
	// LookupAddresses are untrusted extraction hints, never observation authority.
	LookupAddresses []string `json:"lookup_addresses,omitempty"`
	// Missing modes from older waits conservatively require active membership.
	LookupActiveOnly map[string]bool `json:"lookup_active_only,omitempty"`
	LookupContracts  []string        `json:"lookup_contracts,omitempty"`
}

// OccurrenceRequest owns one original mined identity until cleanup and recovery
// reach their durable outcomes. A re-mined copy has a different key even when its
// transaction hash is unchanged. Completed extraction is distinct from delivery.
type OccurrenceRequest struct {
	Original  evmtypes.TransactionMeta `json:"original"`
	CreatedAt int64                    `json:"created_at,omitempty"`
	// StartupCleaned never overrides pending ordinary invalidation.
	CleanupGeneration   uint64 `json:"cleanup_generation,omitempty"`
	StartupCleaned      bool   `json:"startup_cleaned,omitempty"`
	Cleaned             bool   `json:"cleaned"`
	NeedsRescan         bool   `json:"needs_rescan"`
	RescanHeight        int64  `json:"rescan_height"`
	CompletedRescanHash string `json:"completed_rescan_hash,omitempty"`
	SkippedCanonical    bool   `json:"skipped_canonical,omitempty"`
	// ObserverHold records rejection by signature history at the completed anchor.
	// It retains a watch and cannot settle by maturity or an inactive skip.
	ObserverHold    bool             `json:"observer_hold,omitempty"`
	AttributionWait *AttributionWait `json:"attribution_wait,omitempty"`
	// HeldFailedReplay retains a failed replay's vault view before Observer admission.
	// Accounting is deferred; this payload and the original obligation have no TTL.
	HeldFailedReplay  *stypes.TxInItem `json:"held_failed_replay,omitempty"`
	SearchHeight      int64            `json:"search_height"`
	SearchStartHeight *int64           `json:"search_start_height,omitempty"`
}

// OccurrenceJournal serializes recovery transitions with block metadata updates.
// Its records are authoritative; indexes schedule work and must change in the same
// database batch. RPC and Observer waits happen outside mu.
type OccurrenceJournal struct {
	accessor   *LevelDBBlockMetaAccessor
	chain      common.Chain
	prefix     string
	mu         sync.Mutex
	deliveries map[string]bool
	write      func(*leveldb.Batch) error
}

// NewOccurrenceJournal requires metadata and recovery to share one database, so
// replacing metadata can preserve displaced evidence atomically.
func NewOccurrenceJournal(accessor BlockMetaAccessor, chain common.Chain) (*OccurrenceJournal, error) {
	storage, ok := accessor.(*LevelDBBlockMetaAccessor)
	if !ok || storage == nil || storage.db == nil {
		return nil, errors.New("durable occurrence journal requires LevelDB metadata storage")
	}
	journal := &OccurrenceJournal{accessor: storage, chain: chain, prefix: "evm-occurrence-" + strings.ToLower(chain.String()) + "-"}
	// A separate prefix must not overlap the block-meta iterator.
	if storage.PrefixBlockMeta == "" || strings.HasPrefix(journal.prefix, storage.PrefixBlockMeta) || strings.HasPrefix(storage.PrefixBlockMeta, journal.prefix) || strings.HasPrefix(string(journal.indexPrefix("")), storage.PrefixBlockMeta) || strings.HasPrefix(storage.PrefixBlockMeta, "evm-index-v1/") {
		return nil, errors.New("recovery storage prefixes overlap")
	}
	journal.write = journal.writeIndexed
	if err := journal.initializeIndexes(); err != nil {
		return nil, err
	}
	storage.journal = journal
	return journal, nil
}

// key separates block-only replay work from transaction occurrences. Hashless legacy
// identities stay hashless rather than inheriting the current block at that height.
func (j *OccurrenceJournal) key(original evmtypes.TransactionMeta) []byte {
	identity := "block"
	if original.Hash != "" {
		identity = "tx/" + ecommon.HexToHash(original.Hash).Hex()
	}
	id := fmt.Sprintf("%s/%d/%s", identity, original.BlockHeight, strings.ToLower(original.BlockHash))
	return []byte(fmt.Sprintf("%s%x", j.prefix, sha256.Sum256([]byte(id))))
}

func (j *OccurrenceJournal) requestsLocked() ([]OccurrenceRequest, error) {
	iterator := j.accessor.db.NewIterator(util.BytesPrefix([]byte(j.prefix)), nil)
	defer iterator.Release()
	requests := make([]OccurrenceRequest, 0)
	for iterator.Next() {
		var record OccurrenceRequest
		if err := json.Unmarshal(iterator.Value(), &record); err != nil {
			return nil, fmt.Errorf("decode occurrence request: %w", err)
		}
		if string(iterator.Key()) != string(j.key(record.Original)) {
			return nil, errors.New("occurrence key does not match stored identity")
		}
		requests = append(requests, record)

	}
	if err := iterator.Error(); err != nil {
		return nil, err
	}
	sort.Slice(requests, func(a, b int) bool {
		x, y := requests[a].Original, requests[b].Original
		if x.BlockHeight != y.BlockHeight {
			return x.BlockHeight < y.BlockHeight
		}
		if !strings.EqualFold(x.BlockHash, y.BlockHash) {
			return strings.ToLower(x.BlockHash) < strings.ToLower(y.BlockHash)
		}
		return ecommon.HexToHash(x.Hash).Hex() < ecommon.HexToHash(y.Hash).Hex()
	})
	return requests, nil
}

// Requests materializes the full journal for inspection. Runtime scheduling uses
// indexed pages instead; callers must not put this helper on a per-block hot path.
func (j *OccurrenceJournal) Requests() ([]OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.requestsLocked()
}

func (j *OccurrenceJournal) loadLocked(original evmtypes.TransactionMeta) (*OccurrenceRequest, error) {
	data, err := j.accessor.db.Get(j.key(original), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record OccurrenceRequest
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (j *OccurrenceJournal) putLocked(record OccurrenceRequest) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	batch.Put(j.key(record.Original), data)
	return j.write(batch)
}

// Request succeeds only after syncing the original identity and rescan request.
// Its cleanup flag refers to this exact old occurrence, never a later re-mine.
func (j *OccurrenceJournal) Request(occurrence stypes.ObservationOccurrence, cleaned bool) error {
	return j.RequestAll([]stypes.ObservationOccurrence{occurrence}, cleaned)
}

// RequestStartupCleaned imports work behind the client's pre-Start barrier. Existing
// ordinary cleanup retains priority over the import's already-cleaned status.
// Observer imports carry an actual saved requirement, including a valid zero.
func (j *OccurrenceJournal) RequestStartupCleaned(occurrences []stypes.ObservationOccurrence) error {
	return j.requestAll(occurrences, true, true, false, true)
}

// RequestAll merges classification and confirmation evidence idempotently. A new
// Observer detection must use RequestRecovery to reopen a completed attempt.
func (j *OccurrenceJournal) RequestAll(occurrences []stypes.ObservationOccurrence, cleaned bool) error {
	return j.requestAll(occurrences, cleaned, false, false, false)
}

// RequestRecovery reopens a distinct Observer request, unlike an idempotent
// scanner classification or a merge of newly removed confirmation requirements.
// The Observer derives every requirement from its deck/proof, including zero.
func (j *OccurrenceJournal) RequestRecovery(occurrences []stypes.ObservationOccurrence) error {
	return j.requestAll(occurrences, false, false, true, true)
}

// RetainCleanupRequirements merges exact deck/history requirements supplied by
// Observer before its cleanup acknowledgement. An included zero is authoritative;
// an occurrence omitted because its history is unavailable stays unknown.
func (j *OccurrenceJournal) RetainCleanupRequirements(occurrences []stypes.ObservationOccurrence) error {
	return j.requestAll(occurrences, false, false, false, true)
}

// requestAll merges repeated identities and floors before one durable write. Startup
// imports, repeated classification and new recovery requests have different re-arm
// semantics; none may let an old cleanup acknowledgement erase newer work.
// Only Observer-owned inputs establish that a supplied zero is a known floor;
// scanner classification uses the same scalar zero for missing legacy evidence.
func (j *OccurrenceJournal) requestAll(occurrences []stypes.ObservationOccurrence, cleaned, startup, rearm, requirementKnown bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var err error
	records := make(map[string]OccurrenceRequest)
	metas := make(map[int64]*evmtypes.BlockMeta)
	batch := new(leveldb.Batch)
	for _, occurrence := range occurrences {
		if !occurrence.Chain.Equals(j.chain) || occurrence.TxID.IsEmpty() || occurrence.Height < 0 || occurrence.ConfirmationRequired < 0 || len(occurrence.BlockHash) > 128 || len(occurrence.TxID) > 66 {
			return errors.New("invalid occurrence rescan request")
		}
		original := evmtypes.TransactionMeta{Hash: occurrence.TxID.String(), BlockHeight: occurrence.Height, BlockHash: occurrence.BlockHash}
		if err = j.mutableLocked(original); err != nil {
			return err
		}
		key := string(j.key(original))
		record, exists := records[key]
		if !exists {
			prior, readErr := j.loadLocked(original)
			if readErr != nil {
				return readErr
			}
			if prior != nil {
				record, exists = *prior, true
			}
		}
		if !exists {
			start := occurrence.Height
			record = OccurrenceRequest{Original: original, RescanHeight: occurrence.Height, SearchHeight: occurrence.Height, SearchStartHeight: &start, NeedsRescan: true}
		}
		previousRequired := int64(-1)
		if record.Original.ConfirmationRequired != nil {
			previousRequired = *record.Original.ConfirmationRequired
		}
		meta, read := metas[occurrence.Height]
		if !read {
			var readErr error
			meta, readErr = j.accessor.GetBlockMeta(occurrence.Height)
			if readErr != nil {
				return readErr
			}
			metas[occurrence.Height] = meta
		}
		if meta != nil && occurrence.BlockHash != "" {
			for _, tx := range meta.Transactions {
				if ecommon.HexToHash(tx.Hash) == ecommon.HexToHash(original.Hash) && (tx.BlockHash == "" || strings.EqualFold(tx.BlockHash, original.BlockHash)) {
					if !exists {
						record.Original.Inbound = tx.Inbound
					}
					record.Original.Vaults = mergeVaultAttribution(record.Original.Vaults, tx.Vaults)
					if record.Original.Sender == "" {
						record.Original.Sender = tx.Sender
						record.Original.Nonce = tx.Nonce
					}
					if tx.ConfirmationRequired != nil && (record.Original.ConfirmationRequired == nil || *tx.ConfirmationRequired > *record.Original.ConfirmationRequired) {
						record.Original.ConfirmationRequired = tx.ConfirmationRequired
					}
				}
			}
		}
		if (requirementKnown || occurrence.ConfirmationRequired > 0) && (record.Original.ConfirmationRequired == nil || *record.Original.ConfirmationRequired < occurrence.ConfirmationRequired) {
			required := occurrence.ConfirmationRequired
			record.Original.ConfirmationRequired = &required
		}
		raised := record.Original.ConfirmationRequired != nil && *record.Original.ConfirmationRequired > previousRequired
		if rearm || raised {
			if record.CleanupGeneration == ^uint64(0) {
				return errors.New("cleanup generation exhausted")
			}
			record.CleanupGeneration++
		}
		switch {
		case rearm:
			record.AttributionWait = nil
			record.Cleaned, record.StartupCleaned = false, false
			record.NeedsRescan, record.RescanHeight = true, occurrence.Height
			record.CompletedRescanHash = ""
			record.SkippedCanonical = false
			record.ObserverHold = false
		case startup:
			if !exists {
				record.Cleaned, record.StartupCleaned = true, true
			}
		case !cleaned && record.StartupCleaned:
			record.AttributionWait = nil
			record.Cleaned, record.StartupCleaned = false, false
			record.NeedsRescan, record.RescanHeight = true, occurrence.Height
			record.CompletedRescanHash = ""
			record.SkippedCanonical = false
			record.ObserverHold = false
		default:
			record.Cleaned = record.Cleaned || cleaned
		}
		records[key] = record
		data, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return marshalErr
		}
		batch.Put([]byte(key), data)
	}
	if batch.Len() > 0 {
		if err = j.write(batch); err != nil {
			return err
		}
	}
	return nil
}

// RequestBlock retains replay for a replaced block with no known transaction identity.
// The block-only obligation ends after extraction; it cannot prove an accounting correction.
func (j *OccurrenceJournal) RequestBlock(height int64, oldHash string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	original := evmtypes.TransactionMeta{BlockHeight: height, BlockHash: oldHash}
	record, err := j.loadLocked(original)
	if err != nil {
		return err
	}
	if record != nil {
		return nil
	}
	return j.putLocked(OccurrenceRequest{Original: original, Cleaned: true, NeedsRescan: true, RescanHeight: height, SearchHeight: height})
}

// cleanupRequests selects one bounded page of unacknowledged cleanup in height order.
// Selection does not mark it complete; only the later durable acknowledgement can.
func (j *OccurrenceJournal) cleanupRequests() ([]OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	records, _, err := j.indexedRecordsLocked(j.indexPrefix("cleanup"), occurrenceWorkPage, nil)
	return records, err
}

// NextRescan selects the earliest cleaned obligation from the ordered index.
// Unclean records are excluded even when their requested height is earlier.
func (j *OccurrenceJournal) NextRescan() (int64, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	records, _, err := j.indexedRecordsLocked(j.indexPrefix("rescan"), 1, nil)
	if err != nil || len(records) == 0 {
		return 0, false, err
	}
	return records[0].RescanHeight, true, nil
}

// NextRescanWithin selects work whose rewind can be honored when the scanner
// consumes this fetch height. The same height is used for validation by the
// ordered scanner, even when extraction was prefetched. Selection never changes
// an out-of-window record: its evidence and retry obligation survive restart,
// and an explicit rollback can make it eligible again. A forward reappearance
// is processed normally; maintenance alone does not release this hold.
// Seek directly to the lower bound so old holds cannot starve newer work or
// require a scan of the retained backlog on every block.
func (j *OccurrenceJournal) NextRescanWithin(height, floor int64) (int64, bool, error) {
	if height <= 0 {
		return 0, false, errors.New("invalid recovery fetch height")
	}
	minimum := minimumRecoveryHeight(height, floor)
	j.mu.Lock()
	defer j.mu.Unlock()
	prefix := j.indexPrefix("rescan")
	after := append(append([]byte(nil), prefix...), fmt.Sprintf("%020d/", minimum)...)
	records, _, err := j.indexedRecordsLocked(prefix, 1, after)
	if err != nil || len(records) == 0 {
		return 0, false, err
	}
	return records[0].RescanHeight, true, nil
}

// ScheduleRescan requests extraction of a freshly located transaction. Repeated
// lookup of the same completed anchor is a hold, not a reason to rewind forever.
func (j *OccurrenceJournal) ScheduleRescan(record OccurrenceRequest, location TransactionLocation, view *CanonicalValidator) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current, err := j.loadLocked(record.Original)
	if err != nil || current == nil {
		return err
	}
	if location.Height < 0 || location.BlockHash == "" || view == nil {
		return errors.New("invalid canonical rescan location")
	}
	if waiting := current.AttributionWait; waiting != nil && waiting.Height == location.Height && strings.EqualFold(waiting.BlockHash, location.BlockHash) {
		return nil
	}
	// An acknowledged empty extraction is not evidence for terminal errata.
	// Hold its watch without replaying the same unchanged canonical block.
	if !current.NeedsRescan && current.RescanHeight == location.Height &&
		current.CompletedRescanHash != "" && strings.EqualFold(current.CompletedRescanHash, location.BlockHash) {
		return nil
	}
	if err = j.mutableLocked(record.Original); err != nil {
		return err
	}
	view.Invalidate()
	if current.AttributionWait != nil {
		current.AttributionWait.Height = location.Height
		current.AttributionWait.BlockHash = location.BlockHash
	}
	current.NeedsRescan = true
	current.RescanHeight = location.Height
	current.CompletedRescanHash = ""
	current.SkippedCanonical = false
	current.ObserverHold = false
	return j.putLocked(*current)
}

// forgetCompletedRescan reopens a remembered location only after fresh canonical
// evidence displaced its block. An inconsistent transaction index is insufficient.
func (j *OccurrenceJournal) forgetCompletedRescan(record OccurrenceRequest) (OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	current, err := j.loadLocked(record.Original)
	if err != nil || current == nil {
		return record, err
	}
	if current.RescanHeight != record.RescanHeight || current.CompletedRescanHash != record.CompletedRescanHash {
		return *current, nil
	}
	if err = j.mutableLocked(record.Original); err != nil {
		return record, err
	}
	current.CompletedRescanHash = ""
	current.SkippedCanonical = false
	current.ObserverHold = false
	current.SearchHeight = min(current.SearchHeight, current.RescanHeight)
	if err = j.putLocked(*current); err != nil {
		return record, err
	}
	return *current, nil
}

// UpdateSearch saves bounded probe progress without replaying a stale record snapshot
// over newer cleanup or confirmation state.
func (j *OccurrenceJournal) UpdateSearch(record OccurrenceRequest, height int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.mutableLocked(record.Original); err != nil {
		return err
	}
	current, err := j.loadLocked(record.Original)
	if err != nil || current == nil {
		return err
	}
	current.SearchHeight = height
	return j.putLocked(*current)
}

// CompleteRescan runs after a successful durable Observer handoff and fresh
// canonical validation by the scanner. Request writers invalidate the view
// before publishing recovery work; checking generation under the journal lock
// protects newer requests from late completion. Empty replays retain watches.
func (j *OccurrenceJournal) CompleteRescan(height int64, blockHash string, txIn stypes.TxIn, view *CanonicalValidator, generation uint64) error {
	return j.completeRescan(height, blockHash, txIn, view, generation, true)
}

// SkipRescan finishes this canonical delivery attempt without retiring any
// transaction's confirmation, accounting or conflict evidence.
func (j *OccurrenceJournal) SkipRescan(height int64, blockHash string, txIn stypes.TxIn, view *CanonicalValidator, generation uint64) error {
	return j.completeRescan(height, blockHash, txIn, view, generation, false)
}

func (j *OccurrenceJournal) completeRescan(height int64, blockHash string, txIn stypes.TxIn, view *CanonicalValidator, generation uint64, delivered bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if view == nil {
		return errors.New("stale observation finalizer generation")
	}
	// Request writers invalidate before taking the journal lock. Keep that
	// generation fence until the completion batch is durable, without RPC/ACK.
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.generation != generation {
		return errors.New("stale observation finalizer generation")
	}
	pending, err := j.finalizePageLocked(height, blockHash, txIn, delivered)
	if err != nil {
		return err
	}
	if pending {
		return errOccurrenceWorkPending
	}
	return nil
}

// RecordRequirement derives each floor from the batch's common finalise height.
// It updates only the exact mined occurrence and never lowers retained requirements.
func (j *OccurrenceJournal) RecordRequirement(txIn stypes.TxIn, required int64) error {
	if len(txIn.TxArray) == 0 {
		return nil
	}
	txIn.ConfirmationRequired = required
	deadline, err := ObservationFinaliseHeight(txIn)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	metas := make(map[int64]*evmtypes.BlockMeta)
	dirty := make(map[int64]bool)
	batch := new(leveldb.Batch)
	for _, item := range txIn.TxArray {
		if item == nil {
			return errors.New("missing observation item")
		}
		if item.BlockHash == "" {
			continue
		} // Unknown legacy anchors remain unknown.
		if item.BlockHeight < 0 || item.BlockHeight > deadline {
			return errors.New("invalid item confirmation deadline")
		}
		value := deadline - item.BlockHeight
		original := evmtypes.TransactionMeta{Hash: item.Tx, BlockHeight: item.BlockHeight, BlockHash: item.BlockHash}
		if err = j.mutableLocked(original); err != nil {
			return err
		}
		record, readErr := j.loadLocked(original)
		if readErr != nil {
			return readErr
		}
		meta, loaded := metas[item.BlockHeight]
		if !loaded {
			meta, err = j.accessor.GetBlockMeta(item.BlockHeight)
			if err != nil {
				return err
			}
			metas[item.BlockHeight] = meta
		}
		matched := false
		if meta != nil && strings.EqualFold(meta.BlockHash, item.BlockHash) {
			for i := range meta.Transactions {
				tx := &meta.Transactions[i]
				if ecommon.HexToHash(tx.Hash) == ecommon.HexToHash(item.Tx) && strings.EqualFold(tx.BlockHash, item.BlockHash) {
					matched = true
					if tx.ConfirmationRequired != nil {
						value = max(value, *tx.ConfirmationRequired)
					}
					if record != nil && record.Original.ConfirmationRequired != nil {
						value = max(value, *record.Original.ConfirmationRequired)
					}
					if tx.ConfirmationRequired == nil || *tx.ConfirmationRequired < value {
						v := value
						tx.ConfirmationRequired = &v
						dirty[item.BlockHeight] = true
					}
				}
			}
		}
		if !matched && record == nil {
			return errors.New("original observation metadata is unavailable")
		}
		if record != nil && (record.Original.ConfirmationRequired == nil || *record.Original.ConfirmationRequired < value) {
			v := value
			record.Original.ConfirmationRequired = &v
			data, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return marshalErr
			}
			batch.Put(j.key(record.Original), data)
		}
	}
	for height := range dirty {
		data, marshalErr := json.Marshal(metas[height])
		if marshalErr != nil {
			return marshalErr
		}
		batch.Put([]byte(j.accessor.getBlockMetaKey(height)), data)
	}
	if batch.Len() == 0 {
		return nil
	}
	return j.write(batch)
}

// SaveMeta serializes requirement snapshots with duplicate scan metadata. Fresh
// occurrences get independent requirements; only identical anchors can merge.
func (j *OccurrenceJournal) SaveMeta(meta *evmtypes.BlockMeta) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, tx := range meta.Transactions {
		tx.BlockHeight = meta.Height
		if err := j.mutableLocked(tx); err != nil {
			return err
		}
	}
	current, err := j.accessor.GetBlockMeta(meta.Height)
	if err != nil {
		return err
	}
	if current != nil && strings.EqualFold(current.BlockHash, meta.BlockHash) {
		requirements := make(map[ecommon.Hash]int64, len(current.Transactions))
		directions := make(map[ecommon.Hash]bool, len(current.Transactions))
		attributions := make(map[ecommon.Hash][]evmtypes.VaultAttribution, len(current.Transactions))
		for _, old := range current.Transactions {
			if strings.EqualFold(old.BlockHash, meta.BlockHash) {
				attributions[ecommon.HexToHash(old.Hash)] = old.Vaults
			}
			if old.BlockHash == "" || strings.EqualFold(old.BlockHash, meta.BlockHash) {
				directions[ecommon.HexToHash(old.Hash)] = old.Inbound
			}
			if old.ConfirmationRequired != nil && strings.EqualFold(old.BlockHash, meta.BlockHash) {
				requirements[ecommon.HexToHash(old.Hash)] = *old.ConfirmationRequired
			}
		}
		for i := range meta.Transactions {
			if strings.EqualFold(meta.Transactions[i].BlockHash, meta.BlockHash) {
				meta.Transactions[i].Vaults = mergeVaultAttribution(meta.Transactions[i].Vaults, attributions[ecommon.HexToHash(meta.Transactions[i].Hash)])
			}
			if inbound, known := directions[ecommon.HexToHash(meta.Transactions[i].Hash)]; known {
				meta.Transactions[i].Inbound = inbound
			}
			if value, ok := requirements[ecommon.HexToHash(meta.Transactions[i].Hash)]; ok && strings.EqualFold(meta.Transactions[i].BlockHash, meta.BlockHash) {
				required := value
				if existing := meta.Transactions[i].ConfirmationRequired; existing != nil {
					required = max(required, *existing)
				}
				meta.Transactions[i].ConfirmationRequired = &required
			}
		}
	}

	// A returning anchor may be filtered by Observer's D15 hold. Retain its
	// known watch floor before a successful scanner finalizer retires the watch.
	for i := range meta.Transactions {
		tx := &meta.Transactions[i]
		if !strings.EqualFold(tx.BlockHash, meta.BlockHash) {
			continue
		}
		original := evmtypes.TransactionMeta{Hash: tx.Hash, BlockHeight: meta.Height, BlockHash: meta.BlockHash}
		watch, readErr := j.loadLocked(original)
		if readErr != nil {
			return readErr
		}
		if watch != nil {
			tx.Inbound = watch.Original.Inbound
		}
		if watch != nil && watch.Original.ConfirmationRequired != nil && (tx.ConfirmationRequired == nil || *tx.ConfirmationRequired < *watch.Original.ConfirmationRequired) {
			required := *watch.Original.ConfirmationRequired
			tx.ConfirmationRequired = &required
		}
	}

	batch := new(leveldb.Batch)
	if current != nil {
		if err = j.retainReplacedMetaLocked(batch, current, meta); err != nil {
			return err
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	batch.Put([]byte(j.accessor.getBlockMetaKey(meta.Height)), data)
	return j.write(batch)
}

// CaptureIdentity fills missing sender/nonce evidence without changing the original
// anchor. Search restarts far enough back to cover blocks inspected before identity
// was known; the indexed writer also registers that catch-up with the matcher.
func (j *OccurrenceJournal) CaptureIdentity(record OccurrenceRequest, sender string, nonce uint64) (OccurrenceRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.mutableLocked(record.Original); err != nil {
		return record, err
	}
	current, err := j.loadLocked(record.Original)
	if err != nil {
		return record, err
	}
	if current == nil {
		return record, errors.New("occurrence request disappeared")
	}
	current.Original.Sender = sender
	current.Original.Nonce = &nonce
	start := current.Original.BlockHeight
	if current.SearchStartHeight != nil {
		start = *current.SearchStartHeight
	}
	current.SearchHeight = min(current.SearchHeight, start)
	if err = j.putLocked(*current); err != nil {
		return record, err
	}
	return *current, nil
}

// RewindSearch durably moves the matcher to the detected fork boundary. This is
// independent of the forward delivery cursor and does not acknowledge any replay.
func (j *OccurrenceJournal) RewindSearch(height int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.matcherLocked()
	if err != nil {
		return err
	}
	if err = rewindMatcherFork(&state, height); err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	if err = j.saveMatcher(batch, state); err != nil {
		return err
	}
	return j.write(batch)
}

// ReplayPrepared recognizes an already acknowledged sweep without trusting a
// height-only cursor. Every currently stored original occurrence must still have
// its exact durable cleanup and pending rescan; partial gate requests do not qualify.
func (j *OccurrenceJournal) ReplayPrepared(meta *evmtypes.BlockMeta) (bool, error) {
	if meta == nil {
		return false, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	check := func(original evmtypes.TransactionMeta) (bool, error) {
		record, err := j.loadLocked(original)
		if err != nil {
			return false, err
		}
		return record != nil && record.Cleaned && record.NeedsRescan && record.RescanHeight == meta.Height, nil
	}
	if len(meta.Transactions) == 0 {
		return check(evmtypes.TransactionMeta{BlockHeight: meta.Height, BlockHash: meta.BlockHash})
	}
	for _, tx := range meta.Transactions {
		tx.BlockHeight = meta.Height
		ready, err := check(tx)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
	}
	return true, nil
}

// PruneBlockMeta removes aged scan metadata through the indexed writer. Unresolved
// occurrence records live independently and are not expired by this retention window.
func (j *OccurrenceJournal) PruneBlockMeta(height int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	metas, err := j.accessor.GetBlockMetas()
	if err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	for _, meta := range metas {
		if meta.Height < height {
			batch.Delete([]byte(j.accessor.getBlockMetaKey(meta.Height)))
		}
	}
	if batch.Len() == 0 {
		return nil
	}
	return j.write(batch)
}

// mutableLocked fences both exact and transaction-wide changes while a terminal
// handoff owns the evidence. A sibling occurrence must not race that decision.
func (j *OccurrenceJournal) mutableLocked(original evmtypes.TransactionMeta) error {
	if j.deliveries[string(j.key(original))] || (original.Hash != "" && j.deliveries["tx/"+ecommon.HexToHash(original.Hash).Hex()]) {
		return errors.New("terminal occurrence handoff is pending")
	}
	return nil
}

// publishConflict reserves the exact current policy through Observer's stored or
// rejected callback. A timeout after enqueue leaves the reservation with Observer,
// preventing an acknowledged stronger floor from racing a delayed terminal send.
// No journal lock is held while waiting for Observer or making an RPC call.
func (j *OccurrenceJournal) publishConflict(queue chan<- stypes.ErrataBlock, record OccurrenceRequest, proof CanonicalEvidence, required int64, view *CanonicalValidator, block stypes.ErrataBlock, timeout time.Duration) error {
	j.mu.Lock()
	current, err := j.loadLocked(record.Original)
	if err != nil {
		j.mu.Unlock()
		return err
	}
	if current == nil || !current.Cleaned || current.NeedsRescan || current.HeldFailedReplay != nil || !reflect.DeepEqual(*current, record) || j.mutableLocked(record.Original) != nil || !ConflictConfirmed(current.Original, proof.Height, proof.TipHeight, required) {
		j.mu.Unlock()
		return nil
	}
	ready, readErr := j.terminalTxReady(record.Original.Hash, proof, required)
	if readErr != nil || !ready {
		j.mu.Unlock()
		return readErr
	}
	// A busy unbuffered receiver must not turn a fresh proof into an
	// arbitrarily old delivery. Cancel the pre-enqueue wait on expiry or
	// invalidation; after enqueue, Observer owns the acknowledgment.
	view.mu.Lock()
	validFor := proof.CheckedAt.Add(view.freshness).Sub(view.now())
	if view.generation != proof.Generation || validFor <= 0 {
		view.mu.Unlock()
		j.mu.Unlock()
		return nil
	}
	invalidated := view.invalidated
	view.mu.Unlock()
	key := string(j.key(record.Original))
	txKey := "tx/" + ecommon.HexToHash(record.Original.Hash).Hex()
	if j.deliveries == nil {
		j.deliveries = make(map[string]bool)
	}
	j.deliveries[key] = true
	j.deliveries[txKey] = true
	done := make(chan error, 1)
	block.OnStored = func() {
		j.mu.Lock()
		batch := new(leveldb.Batch)
		batch.Delete([]byte(key))
		writeErr := j.pruneTerminalSourceLocked(batch, record.Original)
		if writeErr == nil {
			writeErr = j.write(batch)
		}
		delete(j.deliveries, key)
		delete(j.deliveries, txKey)
		j.mu.Unlock()
		done <- writeErr
	}
	block.OnRejected = func(error) {
		j.mu.Lock()
		delete(j.deliveries, key)
		delete(j.deliveries, txKey)
		j.mu.Unlock()
		done <- nil
	}
	j.mu.Unlock()
	if timeout <= 0 || timeout > 12*time.Second {
		timeout = 12 * time.Second
	}
	timer := time.NewTimer(min(timeout, validFor))
	defer timer.Stop()
	release := func() {
		j.mu.Lock()
		delete(j.deliveries, key)
		delete(j.deliveries, txKey)
		j.mu.Unlock()
	}
	select {
	case queue <- block:
	case <-timer.C:
		release()
		return nil
	case <-invalidated:
		release()
		return nil
	}
	select {
	case err = <-done:
		return err
	case <-timer.C:
		return nil
	}
}

// HoldFailedReplays syncs failed gas-only replays before removing them from
// Observer admission. Passing those views on can overwrite the old principal
// voter before any finalizer. The original payload is not retained, so every
// replay of a recorded transaction is held, even an identical one, and counted
// as an Observer hold. Ordinary failed vault observations without a retained
// recovery watch keep their existing gas-accounting path.
func (j *OccurrenceJournal) HoldFailedReplays(txIn stypes.TxIn) (stypes.TxIn, error) {
	if len(txIn.TxArray) == 0 {
		return txIn, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]*stypes.TxInItem, 0, len(txIn.TxArray))
	batch := new(leveldb.Batch)
	for _, item := range txIn.TxArray {
		if item == nil {
			kept = append(kept, item)
			continue
		}
		if item.EVMAttributionPending {
			kept = append(kept, item)
			continue
		}
		record, err := j.firstTxRecord("held", item.Tx)
		if err != nil {
			return stypes.TxIn{}, err
		}
		if record == nil && item.EVMFailedSynthetic {
			record, err = j.firstTxRecord("tx", item.Tx)
			if err != nil {
				return stypes.TxIn{}, err
			}
		}
		held := record != nil
		if held {
			if err = j.mutableLocked(record.Original); err != nil {
				return stypes.TxIn{}, err
			}
			if item.EVMFailedSynthetic {
				record.HeldFailedReplay = item.Copy()
				record.HeldFailedReplay.OnObservedTxStored = nil
				record.HeldFailedReplay.OnObservedTxRejected = nil
			}
			record.Original.Vaults = mergeVaultAttribution(record.Original.Vaults, item.VaultAttribution)
			record.NeedsRescan = true
			record.RescanHeight = item.BlockHeight
			record.CompletedRescanHash = ""
			record.SkippedCanonical = false
			record.ObserverHold = false
			data, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return stypes.TxIn{}, marshalErr
			}
			batch.Put(j.key(record.Original), data)
		}
		if !held {
			kept = append(kept, item)
		}
	}
	if batch.Len() > 0 {
		if err := j.write(batch); err != nil {
			return stypes.TxIn{}, err
		}
	}
	txIn.TxArray = kept
	return txIn, nil
}

// admittedWatches is a bounded rotating diagnostic/probe page. Every record
// already owns its durable evidence; there is no promotion or admission quota.
func (j *OccurrenceJournal) admittedWatches() ([]OccurrenceRequest, error) { return j.watchPage() }

// Displaced evidence and replacement metadata are written in one atomic batch.
func (j *OccurrenceJournal) retainReplacedMetaLocked(batch *leveldb.Batch, meta, replacement *evmtypes.BlockMeta) error {
	known := make(map[string]OccurrenceRequest)
	// Legacy metadata has no per-transaction anchor, and an older reorg pass could
	// relabel the block hash. Only re-extraction of the same transaction from that
	// unchanged block proves a hashless entry is the same occurrence.
	reextracted := make(map[ecommon.Hash]bool, len(replacement.Transactions))
	for _, tx := range replacement.Transactions {
		if strings.EqualFold(tx.BlockHash, replacement.BlockHash) {
			reextracted[ecommon.HexToHash(tx.Hash)] = true
		}
	}
	for _, original := range meta.Transactions {
		if original.BlockHash != "" && strings.EqualFold(original.BlockHash, replacement.BlockHash) {
			continue
		}
		if original.BlockHash == "" && meta.BlockHash != "" && strings.EqualFold(meta.BlockHash, replacement.BlockHash) && reextracted[ecommon.HexToHash(original.Hash)] {
			continue
		}
		original.BlockHeight = meta.Height
		if err := j.mutableLocked(original); err != nil {
			return err
		}
		key := string(j.key(original))
		record, exists := known[key]
		if !exists {
			prior, err := j.loadLocked(original)
			if err != nil {
				return err
			}
			if prior != nil {
				record, exists = *prior, true
			}
		}
		if !exists {
			start := meta.Height
			record = OccurrenceRequest{Original: original, NeedsRescan: true, RescanHeight: meta.Height, SearchHeight: meta.Height, SearchStartHeight: &start}
		} else {
			record.Original.Vaults = mergeVaultAttribution(record.Original.Vaults, original.Vaults)
			if original.ConfirmationRequired != nil && (record.Original.ConfirmationRequired == nil || *original.ConfirmationRequired > *record.Original.ConfirmationRequired) {
				record.Original.ConfirmationRequired = original.ConfirmationRequired
			}
			if record.Original.Sender == "" {
				record.Original.Sender, record.Original.Nonce = original.Sender, original.Nonce
			}
		}
		known[key] = record
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		batch.Put([]byte(key), data)
	}
	return nil
}

// PrepareRecoveryHandoff matches exactly the TxID-wide completion set, including
// every retained occurrence. Delivery always waits for acknowledged cleanup.
func (j *OccurrenceJournal) PrepareRecoveryHandoff(txIn stypes.TxIn) (stypes.TxIn, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]*stypes.TxInItem, 0, len(txIn.TxArray))
	for _, item := range txIn.TxArray {
		if item == nil || strings.TrimSpace(item.Tx) == "" {
			continue
		}
		kept = append(kept, item)
		owned, err := j.hasTxFact("tx", item.Tx)
		if err != nil {
			return stypes.TxIn{}, err
		}
		if owned {
			unclean, err := j.hasTxFact("unclean", item.Tx)
			if err != nil {
				return stypes.TxIn{}, err
			}
			item.RecoveryOwned = true
			if unclean {
				return stypes.TxIn{}, errors.New("recovery handoff waits for acknowledged cleanup")
			}
		}
	}
	txIn.TxArray = kept
	return txIn, nil
}

// A late cleanup acknowledgement must not clean a newer request or a raised floor.
func (j *OccurrenceJournal) acknowledgeCleanup(records []OccurrenceRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	batch := new(leveldb.Batch)
	for _, snapshot := range records {
		current, err := j.loadLocked(snapshot.Original)
		if err != nil {
			return err
		}
		if current == nil || current.CleanupGeneration != snapshot.CleanupGeneration {
			continue
		}
		if err = j.mutableLocked(current.Original); err != nil {
			return err
		}
		current.Cleaned = true
		data, err := json.Marshal(current)
		if err != nil {
			return err
		}
		batch.Put(j.key(current.Original), data)
	}
	if batch.Len() == 0 {
		return nil
	}
	return j.write(batch)
}

func mergeVaultAttribution(existing, added []evmtypes.VaultAttribution) []evmtypes.VaultAttribution {
	result := append([]evmtypes.VaultAttribution(nil), existing...)
	for _, vault := range added {
		found := false
		for i, old := range result {
			if old.PubKey.Equals(vault.PubKey) {
				if old.Router.IsEmpty() && !vault.Router.IsEmpty() {
					result[i].Router = vault.Router
				}
				found = true
				break
			}
		}
		if !found {
			result = append(result, vault)
		}
	}
	return result
}

// RecoveryPubKeyValidator adds transaction-scoped historical attribution to the
// current registry. It is local to one extraction: a recovered key must never
// re-enter the global pool list or authorize unrelated transactions. Callers must
// propagate Err before treating an empty extraction as successfully completed.
type RecoveryPubKeyValidator struct {
	pubkeymanager.PubKeyValidator
	journal          *OccurrenceJournal
	hash             string
	chain            common.Chain
	known            map[common.PubKey]evmtypes.VaultAttribution
	missing          map[string]bool
	missingContracts map[string]struct{}
	owned            bool
	Err              error
}

func NewRecoveryPubKeyValidator(manager pubkeymanager.PubKeyValidator, journal *OccurrenceJournal, hash string, chain common.Chain) *RecoveryPubKeyValidator {
	result := &RecoveryPubKeyValidator{PubKeyValidator: manager, journal: journal, hash: hash, chain: chain, known: make(map[common.PubKey]evmtypes.VaultAttribution), missing: make(map[string]bool)}
	// Ownership is fixed only for this extraction. A later detection still passes
	// the journal's cleanup barrier at handoff; no long-lived negative cache exists.
	if journal != nil {
		journal.mu.Lock()
		result.owned, result.Err = journal.hasTxFact("tx", hash)
		journal.mu.Unlock()
	}
	return result
}

// poolAddress captures the exact membership/router pair used by extraction and
// caches it for the rest of this transaction. The production manager provides an
// atomic snapshot; test providers may implement the ordinary split methods.
func (p *RecoveryPubKeyValidator) poolAddress(address string, chain common.Chain, activeOnly bool) (valid bool, poolInfo common.ChainPoolInfo) {
	defer func() {
		if p.owned && !valid && chain.Equals(p.chain) && ecommon.IsHexAddress(address) {
			address = strings.ToLower(address)
			// A weaker lookup must not erase an earlier active-only requirement.
			p.missing[address] = p.missing[address] || activeOnly
		}
	}()
	for key := range p.known {
		if activeOnly && !p.owned {
			break
		}
		derived, err := key.GetAddress(chain)
		if err == nil && strings.EqualFold(derived.String(), address) {
			return true, common.ChainPoolInfo{PubKey: key, Chain: chain, PoolAddress: derived}
		}
	}
	if p.PubKeyValidator != nil {
		var ok bool
		var info common.ChainPoolInfo
		var router common.Address
		if snapshot, available := p.PubKeyValidator.(interface {
			GetPoolAddressInfo(string, common.Chain, bool) (bool, common.ChainPoolInfo, common.Address)
		}); available {
			ok, info, router = snapshot.GetPoolAddressInfo(address, chain, activeOnly)
		} else {
			if activeOnly {
				ok, info = p.PubKeyValidator.IsActivePoolAddress(address, chain)
			} else {
				ok, info = p.PubKeyValidator.IsValidPoolAddress(address, chain)
			}
			if ok {
				router = p.PubKeyValidator.GetContract(chain, info.PubKey)
			}
		}
		if ok {
			p.known[info.PubKey] = evmtypes.VaultAttribution{PubKey: info.PubKey, Router: router}
			return true, info
		}
	}
	if p.journal == nil || !p.owned || !chain.Equals(p.chain) {
		return false, common.EmptyChainPoolInfo
	}
	vault, err := p.journal.RecoveryVault(p.hash, address)
	if err != nil {
		p.Err = errors.Join(p.Err, err)
		return false, common.EmptyChainPoolInfo
	}
	if vault == nil {
		return false, common.EmptyChainPoolInfo
	}
	p.known[vault.PubKey] = *vault
	return true, common.ChainPoolInfo{PubKey: vault.PubKey, Chain: chain, PoolAddress: common.Address(address)}
}

func (p *RecoveryPubKeyValidator) IsValidPoolAddress(address string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return p.poolAddress(address, chain, false)
}

func (p *RecoveryPubKeyValidator) IsActivePoolAddress(address string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return p.poolAddress(address, chain, true)
}

func (p *RecoveryPubKeyValidator) GetContract(chain common.Chain, key common.PubKey) common.Address {
	if chain.Equals(p.chain) {
		if known, ok := p.known[key]; ok {
			return known.Router
		}
	}
	if p.PubKeyValidator != nil {
		return p.PubKeyValidator.GetContract(chain, key)
	}
	return common.NoAddress
}

// Attribution returns the validated snapshot in a stable order. The caller puts
// it on its local extracted item before any later registry pruning can occur.
func (p *RecoveryPubKeyValidator) Attribution() []stypes.ObservationVaultAttribution {
	result := make([]stypes.ObservationVaultAttribution, 0, len(p.known))
	for _, vault := range p.known {
		result = append(result, vault)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PubKey.String() < result[j].PubKey.String() })
	return result
}

// IsValidContract restores only a router previously authenticated for this exact
// transaction. Deposit parsing still checks its association with the target key.
func (p *RecoveryPubKeyValidator) IsValidContract(address string) bool {
	if p.journal == nil || !p.owned {
		return false
	}
	ok, err := p.journal.recoveryRouter(p.hash, address)
	p.Err = errors.Join(p.Err, err)
	return ok
}

// RecordContractLookup retains a failed contract-recognition check, including
// checks on log emitters before their vault recipient could be decoded. A
// successful whitelist or router check must not become a readiness hint.
func (p *RecoveryPubKeyValidator) RecordContractLookup(address string, valid bool) {
	if p.owned && !valid && ecommon.IsHexAddress(address) {
		if p.missingContracts == nil {
			p.missingContracts = make(map[string]struct{})
		}
		p.missingContracts[strings.ToLower(address)] = struct{}{}
	}
}

// UnattributedItem carries only local readiness hints when an owned transaction
// produced no payload. It is withheld from Observer and ordinary metadata.
// Known endpoints are retained as evidence, but only a failed lookup becoming
// available can wake the wait; unchanged knowledge cannot cause a rewind loop.
func (p *RecoveryPubKeyValidator) UnattributedItem(tx *etypes.Transaction, signer etypes.Signer) *stypes.TxInItem {
	if !p.owned || p.Err != nil || (len(p.missing) == 0 && len(p.missingContracts) == 0) || tx == nil || tx.To() == nil || signer == nil {
		return nil
	}
	sender, err := signer.Sender(tx)
	if err != nil {
		return nil
	}
	// Contract parsing may stop before looking up the actual sender. Preserve
	// any authenticated endpoint even when the missing fact is a different router.
	p.IsValidPoolAddress(sender.Hex(), p.chain)
	p.IsValidPoolAddress(tx.To().Hex(), p.chain)
	addresses := make([]string, 0, len(p.missing))
	for address := range p.missing {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	contracts := make([]string, 0, len(p.missingContracts))
	for address := range p.missingContracts {
		contracts = append(contracts, address)
	}
	sort.Strings(contracts)
	return &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:], Sender: sender.Hex(), To: tx.To().Hex(),
		EVMAttributionPending: true, EVMAttributionAddresses: addresses, EVMAttributionActiveOnly: maps.Clone(p.missing), EVMAttributionContracts: contracts,
	}
}

// FilterUnattributed keeps existing recovery fail-closed, but prevents unrelated
// router events that ordinary Observer filtering would discard from creating new
// orphan evidence. Unknown current membership never completes an owned attempt.
func (j *OccurrenceJournal) FilterUnattributed(txIn stypes.TxIn) (stypes.TxIn, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]*stypes.TxInItem, 0, len(txIn.TxArray))
	for _, item := range txIn.TxArray {
		if item == nil {
			continue
		}
		from, to := false, false
		for _, vault := range item.VaultAttribution {
			address, err := vault.PubKey.GetAddress(j.chain)
			if err != nil {
				return stypes.TxIn{}, err
			}
			from = from || strings.EqualFold(address.String(), item.Sender)
			to = to || strings.EqualFold(address.String(), item.To)
		}
		if (from || to) && (!item.RequiredMigration || (from && to)) {
			kept = append(kept, item)
			continue
		}
		owned, err := j.hasTxFact("tx", item.Tx)
		if err != nil {
			return stypes.TxIn{}, err
		}
		if owned {
			item.EVMAttributionPending = true
			kept = append(kept, item)
		}
	}
	txIn.TxArray = kept
	return txIn, nil
}

// RefreshAttribution checks a bounded, rotating page using local registry state
// only. It never rewinds on a timer: a replay is re-armed only after authenticated
// endpoint knowledge becomes available, while conflicts remain independently
// eligible through the ordinary watch/matcher paths.
func (j *OccurrenceJournal) RefreshAttribution(manager pubkeymanager.PubKeyValidator, view *CanonicalValidator) error {
	records, err := j.attributionPage()
	if err != nil {
		return err
	}
	var contracts map[string]struct{}
	var refreshErr error
	for _, snapshot := range records {
		wait := snapshot.AttributionWait
		if wait == nil {
			continue
		}
		provider := NewRecoveryPubKeyValidator(manager, j, snapshot.Original.Hash, j.chain)
		hasLookupHints := len(wait.LookupAddresses) != 0 || len(wait.LookupContracts) != 0
		from, to := false, false
		if !hasLookupHints || wait.RequiredMigration {
			from, _ = provider.IsValidPoolAddress(wait.Sender, j.chain)
			to, _ = provider.IsValidPoolAddress(wait.To, j.chain)
		}
		// A parsed-item wait uses its endpoints. An empty-extraction marker must
		// wait for its failed lookup, even when another endpoint is already known.
		ready := !hasLookupHints && (from || to)
		for _, address := range wait.LookupAddresses {
			activeOnly, recorded := wait.LookupActiveOnly[strings.ToLower(address)]
			if !recorded {
				activeOnly = true
			}
			// Each hint uses its own lookup cache: a weaker sibling lookup must
			// not supply the authority for an active-only recipient.
			probe := NewRecoveryPubKeyValidator(manager, j, snapshot.Original.Hash, j.chain)
			known, _ := probe.poolAddress(address, j.chain, activeOnly)
			provider.Err = errors.Join(provider.Err, probe.Err)
			if known {
				for key, vault := range probe.known {
					provider.known[key] = vault
				}
			}
			ready = ready || known
		}
		for _, address := range wait.LookupContracts {
			known := provider.IsValidContract(address)
			if !known && manager != nil {
				// Snapshot the current router set once per maintenance page. These are
				// readiness hints only; the replay still validates the decoded vault.
				if contracts == nil {
					contracts = make(map[string]struct{})
					for _, contract := range manager.GetContracts(j.chain, false) {
						contracts[strings.ToLower(contract.String())] = struct{}{}
					}
				}
				_, known = contracts[strings.ToLower(address)]
			}
			ready = ready || known
		}
		if provider.Err != nil {
			refreshErr = errors.Join(refreshErr, provider.Err)
			continue
		}
		if !ready || (wait.RequiredMigration && (!from || !to)) {
			continue
		}
		proof := provider.Attribution()
		if err = j.rearmAttribution(snapshot, proof, view); err != nil {
			refreshErr = errors.Join(refreshErr, err)
		}
	}
	return refreshErr
}

// rearmAttribution applies only the wait inspected outside the lock. New requests,
// cleanup or replay state win over a late readiness result; current floors and
// accounting payloads are retained rather than copied from the stale snapshot.
func (j *OccurrenceJournal) rearmAttribution(snapshot OccurrenceRequest, proof []stypes.ObservationVaultAttribution, view *CanonicalValidator) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	current, err := j.loadLocked(snapshot.Original)
	if err != nil || current == nil {
		return err
	}
	if snapshot.AttributionWait == nil || current.AttributionWait == nil || !current.Cleaned || current.NeedsRescan || current.CleanupGeneration != snapshot.CleanupGeneration || !reflect.DeepEqual(current.AttributionWait, snapshot.AttributionWait) {
		return nil
	}
	if err = j.mutableLocked(current.Original); err != nil {
		return err
	}
	if view == nil {
		return errors.New("attribution replay has no canonical view")
	}
	view.Invalidate()
	current.Original.Vaults = mergeVaultAttribution(current.Original.Vaults, proof)
	current.RescanHeight = current.AttributionWait.Height
	current.NeedsRescan = true
	current.CompletedRescanHash = ""
	current.SkippedCanonical = false
	current.ObserverHold = false
	current.AttributionWait = nil
	return j.putLocked(*current)
}

func minimumRecoveryHeight(height, floor int64) int64 {
	minimum := max(int64(1), height-btypes.MaxCursorRewindBlocks)
	if floor >= height {
		minimum = height
	} else {
		minimum = max(minimum, floor+1)
	}
	return minimum
}

// ReportReplayWindow uses the ordered scanner's next height, never prefetch.
// A single index lookup exposes durable holds without walking the backlog.
func (j *OccurrenceJournal) ReportReplayWindow(height, floor int64) error {
	if height <= 0 {
		return errors.New("invalid recovery report height")
	}
	oldest, pending, err := j.NextRescan()
	if err != nil {
		return err
	}
	metrics.SetRecoveryReplayWindowBlocked(j.chain.String(), pending && oldest < minimumRecoveryHeight(height, floor))
	return nil
}
