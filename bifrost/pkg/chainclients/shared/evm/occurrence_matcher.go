package evm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// Height/Transaction/Phase/After identify the next unprocessed index entry.
// Revision fences registration or reorg rewinds racing an RPC outside j.mu.
type occurrenceMatcher struct {
	Initialized bool   `json:"initialized"`
	Revision    uint64 `json:"revision"`
	Height      int64  `json:"height"`
	Target      int64  `json:"target"`
	BlockHash   string `json:"block_hash,omitempty"`
	Transaction int    `json:"transaction,omitempty"`
	Phase       int    `json:"phase,omitempty"`
	After       string `json:"after,omitempty"`
	ForkHeight  int64  `json:"fork_height"`
	HasFork     bool   `json:"has_fork,omitempty"`
	LastHeight  int64  `json:"last_height,omitempty"`
	LastHash    string `json:"last_hash,omitempty"`
}

// occurrenceCandidate is a durable hint to recheck a matched block. Due is a
// scheduling height, not proof of maturity or permission to emit terminal errata.
type occurrenceCandidate struct {
	Original  evmtypes.TransactionMeta `json:"original"`
	Height    int64                    `json:"height"`
	BlockHash string                   `json:"block_hash"`
	Due       int64                    `json:"due"`
}

func (j *OccurrenceJournal) matcherLocked() (occurrenceMatcher, error) {
	var state occurrenceMatcher
	data, err := j.accessor.db.Get(j.indexPrefix("matcher"), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	if err == nil && (state.Height < 0 || state.Transaction < 0 || state.Phase < 0 || state.Phase > 1) {
		err = errors.New("invalid recovery matcher cursor")
	}
	return state, err
}

func (j *OccurrenceJournal) saveMatcher(batch *leveldb.Batch, state occurrenceMatcher) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	batch.Put(j.indexPrefix("matcher"), data)
	return nil
}

// rewindMatcher clears partial-block progress and advances the revision fence.
// An RPC begun before registration or a reorg must not write its old cursor back.
func rewindMatcher(state *occurrenceMatcher, height int64) error {
	if height < 0 {
		return errors.New("negative recovery matcher rewind")
	}
	if state.Revision == ^uint64(0) {
		return errors.New("recovery matcher revision exhausted")
	}
	if !state.Initialized {
		state.Initialized = true
		state.Height = height
		state.Target = height - 1
		state.ForkHeight = height
	} else {
		state.Height = min(state.Height, height)
	}
	state.Revision++
	state.BlockHash = ""
	state.Transaction = 0
	state.Phase = 0
	state.After = ""
	state.LastHash = ""
	state.LastHeight = 0
	return nil
}

// rewindMatcherFork remembers the earliest fork in this recovery period. A
// sender learned later must revisit conflicts scanned before its identity was
// known, even if another shallower fork has since been detected. Registration
// resets this boundary only after the journal becomes empty.
func rewindMatcherFork(state *occurrenceMatcher, height int64) error {
	if err := rewindMatcher(state, height); err != nil {
		return err
	}
	if !state.HasFork || height < state.ForkHeight {
		state.ForkHeight = height
	}
	state.HasFork = true
	return nil
}

// Registration shares the same batch and mutex as cursor rewinds. A newly
// learned identity searches from the current fork boundary as well as forward.
func (j *OccurrenceJournal) registerMatcherLocked(batch *leveldb.Batch, height int64) error {
	state, err := j.matcherLocked()
	if err != nil {
		return err
	}
	existing, _, readErr := j.indexedRecordsLocked(j.indexPrefix("search-start"), 1, nil)
	if readErr != nil {
		return readErr
	}
	target := state.Target
	if len(existing) == 0 {
		state = occurrenceMatcher{Revision: state.Revision}
	}
	// Registration is not proof that the already matched chain is unchanged.
	// Keep its last anchor even when the new search starts earlier: that start
	// may still lie above an undetected fork and omit a replacement conflict.
	lastHeight, lastHash := state.LastHeight, state.LastHash
	if err = rewindMatcher(&state, height); err != nil {
		return err
	}
	state.LastHeight, state.LastHash = lastHeight, lastHash
	state.Target = max(state.Target, target)
	return j.saveMatcher(batch, state)
}

// Called by ordered delivery or the scan-loop maintenance pass, never prefetch.
func (j *OccurrenceJournal) advanceMatcherTargetLocked(batch *leveldb.Batch, height int64) error {
	state, err := j.matcherLocked()
	if err != nil {
		return err
	}
	if !state.Initialized {
		state = occurrenceMatcher{Initialized: true, Height: height, Target: height, ForkHeight: height}
	} else {
		state.Target = max(state.Target, height)
	}
	return j.saveMatcher(batch, state)
}

func (j *OccurrenceJournal) candidateKey(c occurrenceCandidate) []byte {
	return append(j.indexPrefix("candidate"), fmt.Sprintf("%s/%020d/%s", j.key(c.Original), c.Height, strings.ToLower(c.BlockHash))...)
}

func (j *OccurrenceJournal) candidateDueKey(c occurrenceCandidate) []byte {
	return append(j.indexPrefix("due"), fmt.Sprintf("%020d/%s", c.Due, j.candidateKey(c))...)
}

// putCandidateLocked moves the due-height index with its candidate in the same batch.
// Raising a requirement must leave one scheduled recheck, not a stale earlier due key.
func (j *OccurrenceJournal) putCandidateLocked(batch *leveldb.Batch, c occurrenceCandidate) error {
	key := j.candidateKey(c)
	data, err := j.accessor.db.Get(key, nil)
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return err
	}
	if err == nil {
		var old occurrenceCandidate
		if err = json.Unmarshal(data, &old); err != nil {
			return err
		}
		batch.Delete(j.candidateDueKey(old))
	}
	data, err = json.Marshal(c)
	if err != nil {
		return err
	}
	batch.Put(key, data)
	batch.Put(j.candidateDueKey(c), key)
	return nil
}

// ServiceOccurrenceMatcher is called by the ordered scan loop, including while
// extraction is paused. The independent cursor covers canonical blocks without
// moving the delivery cursor or acknowledging any observation. scannedHeight is
// the ordered delivery cursor, never the highest fetched/prefetched block.
func ServiceOccurrenceMatcher(j *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer, scannedHeight int64, paused bool) error {
	j.mu.Lock()
	records, _, err := j.indexedRecordsLocked(j.indexPrefix("search-start"), 1, nil)
	j.mu.Unlock()
	if err != nil || len(records) == 0 {
		return err
	}
	started := time.Now()
	tip, err := rpc.GetTipHeader()
	if err != nil || tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Sign() < 0 {
		return nil
	}
	j.mu.Lock()
	state, err := j.matcherLocked()
	if err == nil && tip.Number.Int64() > state.Target {
		batch := new(leveldb.Batch)
		err = j.advanceMatcherTargetLocked(batch, tip.Number.Int64())
		if err == nil {
			err = j.write(batch)
		}
	}
	j.mu.Unlock()
	if err != nil {
		return err
	}
	// Let forward delivery catch up without starving recovery. A paused or
	// caught-up scanner can spend more time reducing the independent matcher lag.
	// This is a cooperative budget: one page/RPC can overrun it, and at least one
	// page is attempted so slow calls cannot prevent all recovery progress.
	budget := time.Second
	if !paused && scannedHeight < tip.Number.Int64() {
		budget = 50 * time.Millisecond
	}
	// A single cached raw block is reused across its transaction/fanout pages.
	cached := &matcherBlockCache{NonceConflictRPC: rpc}
	proofs := &matcherProofCache{}
	deadline := started.Add(budget)
	for page := 0; page < 256; page++ {
		j.mu.Lock()
		before, readErr := j.matcherLocked()
		j.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		if err = matchOccurrenceBlock(j, cached, view, signer, proofs); err != nil {
			return err
		}
		j.mu.Lock()
		after, readErr := j.matcherLocked()
		j.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		if after == before || after.Height > after.Target || after.Revision != before.Revision || time.Now().After(deadline) {
			break
		}
	}
	return nil
}

type matcherBlockCache struct {
	NonceConflictRPC
	block *etypes.Block
}

func (r *matcherBlockCache) GetBlock(height int64) (*etypes.Block, error) {
	if r.block != nil && r.block.Number().IsInt64() && r.block.Number().Int64() == height {
		return r.block, nil
	}
	block, err := r.NonceConflictRPC.GetBlock(height)
	if err == nil {
		r.block = block
	}
	return block, err
}

const matcherAnchorCount = 256

type matcherAnchor struct {
	Height int64  `json:"height"`
	Hash   string `json:"hash"`
}

// The ring records what the matcher itself processed, rather than trusting
// metadata that forward extraction may already have replaced. A canonical old
// anchor proves its entire prefix is unchanged. Registration still rewinds the
// unprocessed cursor independently, so finding a fork cannot skip new watches.
func (j *OccurrenceJournal) matcherFork(state occurrenceMatcher, view *CanonicalValidator) (matcherAnchor, bool, error) {
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.indexPrefix("matched-block")), nil)
	var anchors []matcherAnchor
	for iter.Next() {
		var anchor matcherAnchor
		if err := json.Unmarshal(iter.Value(), &anchor); err != nil {
			iter.Release()
			return matcherAnchor{}, false, err
		}
		if anchor.Height >= 0 && anchor.Height < state.LastHeight && anchor.Hash != "" {
			anchors = append(anchors, anchor)
		}
	}
	err := iter.Error()
	iter.Release()
	if err != nil {
		return matcherAnchor{}, false, err
	}
	sort.Slice(anchors, func(i, k int) bool { return anchors[i].Height < anchors[k].Height })
	low, high := 0, len(anchors)
	for low < high {
		mid := low + (high-low)/2
		_, proofErr := view.Validate(anchors[mid].Height, anchors[mid].Hash)
		switch {
		case proofErr == nil:
			low = mid + 1
		case errors.Is(proofErr, stypes.ErrObservedTxOrphaned):
			high = mid
		default:
			return matcherAnchor{}, false, proofErr
		}
	}
	if low == 0 {
		return matcherAnchor{}, false, nil
	}
	anchor := anchors[low-1]
	if _, err = view.Validate(anchor.Height, anchor.Hash); err != nil {
		return matcherAnchor{}, false, err
	}
	return anchor, true, nil
}

// rewindMatcherEvidenceLocked is the conservative fallback when the bounded anchor
// ring cannot identify a common ancestor. Known shallow forks use the tighter rewind
// instead of repeatedly searching from the oldest unresolved record.
func (j *OccurrenceJournal) rewindMatcherEvidenceLocked(state *occurrenceMatcher) error {
	records, _, err := j.indexedRecordsLocked(j.indexPrefix("search-start"), 1, nil)
	if err != nil || len(records) == 0 {
		return err
	}
	start := records[0].Original.BlockHeight
	if records[0].SearchStartHeight != nil {
		start = min(start, *records[0].SearchStartHeight)
	}
	// Retained metadata includes the supported fork range preceding a lost
	// transaction. A conflicting replacement can be mined before that transaction.
	iter := j.accessor.db.NewIterator(util.BytesPrefix([]byte(j.accessor.PrefixBlockMeta)), nil)
	for iter.Next() {
		height, parseErr := strconv.ParseInt(strings.TrimPrefix(string(iter.Key()), j.accessor.PrefixBlockMeta), 10, 64)
		if parseErr != nil {
			iter.Release()
			return parseErr
		}
		start = min(start, height)
	}
	readErr := iter.Error()
	iter.Release()
	if readErr != nil {
		return readErr
	}

	return rewindMatcherFork(state, start)
}

// MatchOccurrenceBlock performs one bounded matching page. It is independent
// of forward extraction, so rejected cleanup cannot starve accepted recovery.
// Every saved continuation and its candidates share one atomic durable write.
func MatchOccurrenceBlock(j *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer) error {
	return matchOccurrenceBlock(j, rpc, view, signer, nil)
}

// A page only creates recheck hints. Reuse at most two proofs within this one
// service call; every page still checks the shared generation before writing.
// The next service call refreshes both proofs, and terminal decisions never use
// this cache. Avoid repeated daemon round trips for each 128-transaction page.
type matcherProofCache struct {
	entries [2]struct {
		height int64
		hash   string
		proof  CanonicalEvidence
		valid  bool
	}
	next int
}

func (c *matcherProofCache) validate(view *CanonicalValidator, height int64, hash string) (CanonicalEvidence, error) {
	if c != nil {
		for _, entry := range c.entries {
			if entry.valid && entry.height == height && strings.EqualFold(entry.hash, hash) && view.Current(entry.proof.Generation) {
				return entry.proof, nil
			}
		}
	}
	proof, err := view.Validate(height, hash)
	if err == nil && c != nil {
		c.entries[c.next].height = height
		c.entries[c.next].hash = hash
		c.entries[c.next].proof = proof
		c.entries[c.next].valid = true
		c.next = (c.next + 1) % len(c.entries)
	}
	return proof, err
}

func matchOccurrenceBlock(j *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer, proofs *matcherProofCache) error {
	j.mu.Lock()
	state, err := j.matcherLocked()
	j.mu.Unlock()
	if err != nil || !state.Initialized {
		return err
	}
	// A reorg can happen while forward extraction is paused. Detect displacement
	// of the last matched block and revisit retained evidence, without a growing
	// second block-history database. Normal scanner reorgs use their tighter fork.
	if state.LastHash != "" {
		if _, lastErr := proofs.validate(view, state.LastHeight, state.LastHash); lastErr != nil {
			if !errors.Is(lastErr, stypes.ErrObservedTxOrphaned) {
				return nil
			}
			fork, known, forkErr := j.matcherFork(state, view)
			if forkErr != nil {
				return nil
			}
			j.mu.Lock()
			latest, readErr := j.matcherLocked()
			if readErr == nil && latest.Revision == state.Revision && latest.LastHash == state.LastHash {
				if known {
					readErr = rewindMatcherFork(&latest, fork.Height+1)
					// Keep the common ancestor until its child is matched. A second, deeper
					// reorg between service calls must not skip the newly replaced prefix.
					if readErr == nil && latest.Height == fork.Height+1 {
						latest.LastHeight = fork.Height
						latest.LastHash = fork.Hash
					}
				} else {
					readErr = j.rewindMatcherEvidenceLocked(&latest)
				}
				if readErr == nil {
					batch := new(leveldb.Batch)
					readErr = j.saveMatcher(batch, latest)
					if readErr == nil {
						readErr = j.write(batch)
					}
				}
			}
			j.mu.Unlock()
			return readErr
		}
	}
	if state.Height > state.Target {
		return nil
	}
	block, err := rpc.GetBlock(state.Height)
	if err != nil {
		return nil
	}
	if block == nil || block.Number() == nil || !block.Number().IsInt64() || block.Number().Int64() != state.Height {
		return nil
	}
	linked := true
	if state.LastHash != "" && state.LastHeight < math.MaxInt64 && state.Height == state.LastHeight+1 {
		if linked, err = SameBlock(rpc, block.ParentHash(), state.LastHash, state.LastHeight); err != nil {
			return nil
		}
	}
	if !linked {
		view.Invalidate()
		fork, known, forkErr := j.matcherFork(state, view)
		if forkErr != nil {
			return nil
		}
		j.mu.Lock()
		latest, readErr := j.matcherLocked()
		if readErr == nil && latest.Revision == state.Revision && latest.Height == state.Height && latest.LastHash == state.LastHash {
			if known {
				readErr = rewindMatcherFork(&latest, fork.Height+1)
				// Keep the common ancestor until its child is matched. A second, deeper
				// reorg between service calls must not skip the newly replaced prefix.
				if readErr == nil && latest.Height == fork.Height+1 {
					latest.LastHeight = fork.Height
					latest.LastHash = fork.Hash
				}
			} else {
				readErr = j.rewindMatcherEvidenceLocked(&latest)
			}
			if readErr == nil {
				batch := new(leveldb.Batch)
				readErr = j.saveMatcher(batch, latest)
				if readErr == nil {
					readErr = j.write(batch)
				}
			}
		}
		j.mu.Unlock()
		return readErr
	}
	proof, err := proofs.validate(view, state.Height, block.Hash().Hex())
	if err != nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	latest, err := j.matcherLocked()
	if err != nil {
		return err
	}
	if latest.Revision != state.Revision || latest.Height != state.Height || latest.Transaction != state.Transaction || latest.Phase != state.Phase || latest.After != state.After {
		return nil
	}
	if !view.Current(proof.Generation) {
		return nil
	}
	state.Target = latest.Target
	if state.BlockHash != "" && !strings.EqualFold(state.BlockHash, block.Hash().Hex()) {
		if err = rewindMatcher(&state, state.Height); err != nil {
			return err
		}
	}
	state.BlockHash = block.Hash().Hex()
	batch := new(leveldb.Batch)
	work := 0
	transactions := block.Transactions()
	for state.Transaction < len(transactions) && work < occurrenceWorkPage {
		tx := transactions[state.Transaction]
		if tx == nil {
			state.Transaction++
			state.Phase = 0
			state.After = ""
			work++
			continue
		}
		// Match hash first, then outer sender/nonce. Authorization-list nonce use
		// is not terminal evidence here; unmatched obligations remain retained.
		prefix := append(j.indexPrefix("tx"), tx.Hash().Hex()+"/"...)
		if state.Phase == 1 {
			if signer == nil {
				state.Transaction++
				state.Phase = 0
				state.After = ""
				work++
				continue
			}
			sender, senderErr := signer.Sender(tx)
			nonce, nonceKnown := TransactionNonce(tx)
			if senderErr != nil || !nonceKnown {
				state.Transaction++
				state.Phase = 0
				state.After = ""
				work++
				continue
			}
			prefix = append(j.indexPrefix("nonce"), fmt.Sprintf("%s/%020d/", strings.ToLower(sender.Hex()), nonce)...)
		}
		iter := j.accessor.db.NewIterator(util.BytesPrefix(prefix), nil)
		valid := iter.First()
		if state.After != "" {
			valid = iter.Seek([]byte(state.After))
			if valid && bytes.Equal(iter.Key(), []byte(state.After)) {
				valid = iter.Next()
			}
		}
		// One transaction may match many old occurrences. Count that fanout against
		// the page budget and persist the exact index continuation with its hints.
		for valid && work < occurrenceWorkPage {
			data, readErr := j.accessor.db.Get(iter.Value(), nil)
			if readErr != nil {
				iter.Release()
				return readErr
			}
			var record OccurrenceRequest
			if readErr = json.Unmarshal(data, &record); readErr != nil {
				iter.Release()
				return readErr
			}
			candidate := occurrenceCandidate{Original: record.Original, Height: state.Height, BlockHash: state.BlockHash, Due: state.Height}
			if err = j.putCandidateLocked(batch, candidate); err != nil {
				iter.Release()
				return err
			}
			state.After = string(bytes.Clone(iter.Key()))
			work++
			valid = iter.Next()
		}
		readErr := iter.Error()
		iter.Release()
		if readErr != nil {
			return readErr
		}
		if !valid {
			state.After = ""
			state.Phase++
			if state.Phase == 2 {
				state.Transaction++
				state.Phase = 0
			}
			work++
		}
	}
	if state.Transaction == len(transactions) {
		if state.Height == math.MaxInt64 {
			return errors.New("recovery matcher height exhausted")
		}
		anchor, marshalErr := json.Marshal(matcherAnchor{Height: state.Height, Hash: state.BlockHash})
		if marshalErr != nil {
			return marshalErr
		}
		batch.Put(append(j.indexPrefix("matched-block"), fmt.Sprintf("%03d", state.Height%matcherAnchorCount)...), anchor)
		state.LastHeight = state.Height
		state.LastHash = state.BlockHash
		state.Height++
		state.BlockHash = ""
		state.Transaction = 0
		state.Phase = 0
		state.After = ""
	}
	if err = j.saveMatcher(batch, state); err != nil {
		return err
	}
	return j.write(batch)
}

// dueCandidates reads one ordered maturity page and leaves later work on disk.
// A due entry is removed only after a terminal outcome or proven displacement.
func (j *OccurrenceJournal) dueCandidates(tip int64) ([]occurrenceCandidate, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.indexPrefix("due")), nil)
	defer iter.Release()
	var result []occurrenceCandidate
	for iter.Next() && len(result) < 16 {
		data, err := j.accessor.db.Get(iter.Value(), nil)
		if err != nil {
			return nil, err
		}
		var c occurrenceCandidate
		if err = json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		if c.Due > tip {
			break
		}
		result = append(result, c)
	}
	return result, iter.Error()
}

// candidateBlockCache shares raw input across this bounded candidate page and
// its nested full probes. It is not canonical evidence: every settlement still
// validates the block hash and generation independently. Retain at most one
// block per candidate, and discard all cached data when this pass returns.
type candidateBlockCache struct {
	NonceConflictRPC
	blocks map[int64]*etypes.Block
	limit  int
}

func (r *candidateBlockCache) GetBlock(height int64) (*etypes.Block, error) {
	if block := r.blocks[height]; block != nil {
		return block, nil
	}
	block, err := r.NonceConflictRPC.GetBlock(height)
	if err == nil && block != nil && block.Number() != nil && block.Number().IsInt64() && block.Number().Int64() == height && len(r.blocks) < r.limit {
		r.blocks[height] = block
	}
	return block, err
}

// Candidates are scheduling hints. Full policy, canonical evidence, retained
// floors, cleanup, replay and accounting holds are checked again at maturity.
func ProbeOccurrenceCandidates(j *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer, policy func(*etypes.Block) (int64, error), queue chan<- stypes.ErrataBlock, timeout time.Duration) error {
	if policy == nil {
		return nil
	}
	j.mu.Lock()
	iter := j.accessor.db.NewIterator(util.BytesPrefix(j.indexPrefix("due")), nil)
	hasCandidates := iter.First()
	readErr := iter.Error()
	iter.Release()
	j.mu.Unlock()
	if readErr != nil || !hasCandidates {
		return readErr
	}
	tip, err := rpc.GetTipHeader()
	if err != nil || tip == nil || tip.Number == nil || !tip.Number.IsInt64() {
		return nil
	}
	height := tip.Number.Int64()
	if height < 0 || height == math.MaxInt64 {
		return nil
	}
	candidates, err := j.dueCandidates(height)
	if err != nil {
		return err
	}
	// Policy calculation may extract every receipt in an Ethereum block. Share
	// only successful calculations for the exact block identity during this pass;
	// a transient error is retried and the next pass recomputes current policy.
	cached := &candidateBlockCache{NonceConflictRPC: rpc, blocks: make(map[int64]*etypes.Block), limit: len(candidates)}
	type blockIdentity struct {
		height int64
		hash   string
	}
	policies := make(map[blockIdentity]int64)
	effective := func(block *etypes.Block) (int64, error) {
		identity := blockIdentity{height: block.Number().Int64(), hash: block.Hash().Hex()}
		if value, ok := policies[identity]; ok {
			return value, nil
		}
		value, policyErr := policy(block)
		if policyErr == nil && value >= 0 && len(policies) < len(candidates) {
			policies[identity] = value
		}
		return value, policyErr
	}
	for _, candidate := range candidates {
		j.mu.Lock()
		record, readErr := j.loadLocked(candidate.Original)
		retained := int64(0)
		ready := false
		// Cleanup, replay and accounting holds cannot be lifted by a candidate
		// policy RPC. Leave the hint scheduled, but do no block/receipt work until
		// those prerequisites and the original confirmation floor are available.
		if readErr == nil && record != nil && record.Cleaned && !record.NeedsRescan && record.Original.Hash != "" && record.Original.ConfirmationRequired != nil && *record.Original.ConfirmationRequired >= 0 {
			var held bool
			held, readErr = j.hasTxFact("held", record.Original.Hash)
			if readErr == nil && !held {
				retained, readErr = j.retainedFloor(record.Original.Hash)
				ready = readErr == nil
			}
		}
		j.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		resolved := record == nil
		due := height + 1
		if ready {
			block, blockErr := cached.GetBlock(candidate.Height)
			if blockErr == nil && block != nil && block.Number().IsInt64() && block.Number().Int64() == candidate.Height {
				if !strings.EqualFold(block.Hash().Hex(), candidate.BlockHash) {
					if _, proofErr := view.Validate(candidate.Height, block.Hash().Hex()); proofErr == nil {
						resolved = true
					}
				} else if required, policyErr := effective(block); policyErr == nil && required >= 0 {
					required = max(required, *record.Original.ConfirmationRequired, retained)
					if required <= int64(^uint64(0)>>1)-candidate.Height {
						due = max(due, candidate.Height+required)
					}
					if required <= int64(^uint64(0)>>1)-candidate.Height && candidate.Height+required <= height && record.Cleaned && !record.NeedsRescan {
						offset := 0
						if err = probeOccurrenceRecords(j, cached, view, signer, effective, queue, timeout, &offset, []OccurrenceRequest{*record}, &candidate.Height); err != nil {
							return err
						}
						j.mu.Lock()
						current, currentErr := j.loadLocked(candidate.Original)
						j.mu.Unlock()
						if currentErr != nil {
							return currentErr
						}
						resolved = current == nil
						// A completed skip still needs its maturity decision. Canonical
						// location alone must not retire that scheduling responsibility.
						if current != nil && !current.SkippedCanonical && !current.NeedsRescan && current.RescanHeight == candidate.Height && strings.EqualFold(current.CompletedRescanHash, candidate.BlockHash) {
							location, locationErr := LocateTransaction(rpc, current.Original.Hash)
							resolved = locationErr == nil && location.State == TransactionCanonical && location.Height == candidate.Height && strings.EqualFold(location.BlockHash, candidate.BlockHash)
						}
					}
				}
			}
		}
		j.mu.Lock()
		// Other passes may have replaced or rescheduled this hint while RPC ran.
		currentData, currentErr := j.accessor.db.Get(j.candidateKey(candidate), nil)
		if errors.Is(currentErr, leveldb.ErrNotFound) {
			j.mu.Unlock()
			continue
		}
		if currentErr != nil {
			j.mu.Unlock()
			return currentErr
		}
		var current occurrenceCandidate
		if currentErr = json.Unmarshal(currentData, &current); currentErr != nil {
			j.mu.Unlock()
			return currentErr
		}
		if current.Due != candidate.Due {
			j.mu.Unlock()
			continue
		}
		batch := new(leveldb.Batch)
		if resolved {
			batch.Delete(j.candidateKey(candidate))
			batch.Delete(j.candidateDueKey(candidate))
		} else {
			candidate.Due = due
			err = j.putCandidateLocked(batch, candidate)
		}
		if err == nil {
			err = j.write(batch)
		}
		j.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
