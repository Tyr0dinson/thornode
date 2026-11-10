package evm

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/syndtr/goleveldb/leveldb"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type probeBlockCache struct {
	NonceConflictRPC
	blocks  map[int64]*etypes.Block
	budget  int64
	headers map[int64]*etypes.Header
}

func (r *probeBlockCache) GetBlock(height int64) (*etypes.Block, error) {
	if block := r.blocks[height]; block != nil {
		return block, nil
	}
	if r.budget <= 0 {
		return nil, errors.New("bounded conflict probe exhausted")
	}
	r.budget--
	block, err := r.NonceConflictRPC.GetBlock(height)
	if err == nil {
		r.blocks[height] = block
	}
	return block, err
}

func (r *probeBlockCache) GetHeader(height int64) (*etypes.Header, error) {
	if header := r.headers[height]; header != nil {
		return header, nil
	}
	header, err := r.NonceConflictRPC.GetHeader(height)
	if err == nil {
		r.headers[height] = header
	}
	return header, err
}

// ProbeOccurrenceWatches gives unresolved records a rotating catch-up pass. It
// complements block-driven matching; lack of a match never expires the evidence.
func ProbeOccurrenceWatches(journal *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer, policy func(*etypes.Block) (int64, error), queue chan<- stypes.ErrataBlock, timeout time.Duration, offset *int) error {
	if policy == nil {
		return nil
	} // Clients configure the effective policy at construction.
	records, err := journal.admittedWatches()
	if err != nil {
		return err
	}
	return probeOccurrenceRecords(journal, rpc, view, signer, policy, queue, timeout, offset, records, nil)
}

// probeOccurrenceRecords shares a finite block-read budget across this page.
// RPC uncertainty leaves work pending, while canonical reappearance goes through
// replay or mature-skip settlement rather than directly deleting its obligation.
func probeOccurrenceRecords(journal *OccurrenceJournal, rpc NonceConflictRPC, view *CanonicalValidator, signer etypes.Signer, policy func(*etypes.Block) (int64, error), queue chan<- stypes.ErrataBlock, timeout time.Duration, offset *int, records []OccurrenceRequest, searchOverride *int64) error {
	if len(records) == 0 {
		return nil
	}
	tip, err := rpc.GetTipHeader()
	if err != nil {
		return nil
	} // Uncertainty holds; ordinary scanning can continue.
	if tip == nil || tip.Number == nil || !tip.Number.IsInt64() {
		return nil
	}
	cached := &probeBlockCache{NonceConflictRPC: rpc, blocks: make(map[int64]*etypes.Block), budget: 8, headers: make(map[int64]*etypes.Header)}
	policies := make(map[ecommon.Hash]int64)
	effective := func(block *etypes.Block) (int64, error) {
		if value, ok := policies[block.Hash()]; ok {
			return value, nil
		}
		value, policyErr := policy(block)
		if policyErr == nil {
			policies[block.Hash()] = value
		}
		return value, policyErr
	}
	// The durable page cursor already rotates the first RPC-budget recipient.
	*offset++
	for _, record := range records {
		journal.mu.Lock()
		held, heldErr := journal.hasTxFact("held", record.Original.Hash)
		journal.mu.Unlock()
		if heldErr != nil {
			return heldErr
		}
		if held {
			continue
		}
		if !record.Cleaned || record.NeedsRescan || record.Original.Hash == "" {
			continue
		}
		if record.Original.Sender == "" || record.Original.Nonce == nil {
			tx, _, lookupErr := rpc.GetTransaction(record.Original.Hash)
			if lookupErr != nil && !errors.Is(lookupErr, ethereum.NotFound) {
				continue
			}
			// A deposit original keeps no nonce; see RetainTransactionIdentity.
			if tx != nil && tx.Hash() == ecommon.HexToHash(record.Original.Hash) && signer != nil && tx.Type() != etypes.DepositTxType {
				sender, senderErr := signer.Sender(tx)
				if senderErr != nil {
					continue
				}
				record, err = journal.CaptureIdentity(record, sender.Hex(), tx.Nonce())
				if err != nil {
					return err
				}
			}
		}
		end := tip.Number.Int64()
		if searchOverride == nil && record.SearchHeight > end {
			continue
		}
		if record.CompletedRescanHash != "" {
			// A single inconsistent header must not erase an acknowledged replay
			// and turn it into a persistent, possibly out-of-range rewind.
			location, locationErr := LocateTransaction(rpc, record.Original.Hash)
			if locationErr != nil {
				continue
			}
			sameLocation := (location.State == TransactionCanonical || location.State == TransactionFailed) &&
				location.Height == record.RescanHeight && strings.EqualFold(location.BlockHash, record.CompletedRescanHash)
			if !sameLocation {
				_, completedErr := view.Validate(record.RescanHeight, record.CompletedRescanHash)
				if errors.Is(completedErr, stypes.ErrObservedTxOrphaned) {
					// Index absence may also be transient. Corroborate displacement
					// with a full replacement block. This rare read shares the probe
					// budget; ordinary absent watches keep all eight search slots.
					block, blockErr := cached.GetBlock(record.RescanHeight)
					if blockErr == nil && block != nil && block.Number().Int64() == record.RescanHeight &&
						!strings.EqualFold(block.Hash().Hex(), record.CompletedRescanHash) {
						if _, canonicalErr := view.Validate(record.RescanHeight, block.Hash().Hex()); canonicalErr == nil {
							record, err = journal.forgetCompletedRescan(record)
							if err != nil {
								return err
							}
						}
					}
				}
			}
		}
		searchHeight := record.SearchHeight
		if searchOverride != nil {
			searchHeight = *searchOverride
		}
		proof, probeErr := ScanNonceConflict(cached, view, record.Original, signer, searchHeight, end, 8, effective)
		if probeErr != nil {
			continue
		}
		if proof.OriginalCanonical != nil {
			settled, settleErr := journal.settleSkippedCanonical(record, *proof.OriginalCanonical, cached, view, effective)
			if settleErr != nil {
				return settleErr
			}
			if settled {
				continue
			}
			if err = journal.ScheduleRescan(record, *proof.OriginalCanonical, view); err != nil {
				return err
			}
			continue
		}
		if proof.Failed && (record.NeedsRescan || record.RescanHeight != proof.Canonical.Height ||
			!strings.EqualFold(record.CompletedRescanHash, proof.Canonical.BlockHash)) {
			// An exact acknowledged replay must establish that the failed block no
			// longer produces the original inbound observation before its correction.
			if err = journal.ScheduleRescan(record, TransactionLocation{State: TransactionFailed, Height: proof.Canonical.Height, BlockHash: proof.Canonical.BlockHash}, view); err != nil {
				return err
			}
			continue
		}
		if proof.Conflict == nil && !proof.Failed {
			if proof.NextHeight != record.SearchHeight {
				if err = journal.UpdateSearch(record, proof.NextHeight); err != nil {
					return err
				}
			}
			continue
		}
		// Refresh canonical proof and either original absence or its failed
		// execution at the same location immediately before terminal delivery. This remains subject to the documented check/send race.
		location, locationErr := LocateTransaction(rpc, record.Original.Hash)
		if locationErr != nil {
			continue
		}
		fresh, freshErr := view.Validate(proof.Canonical.Height, proof.Canonical.BlockHash)
		if freshErr != nil || fresh.Generation != proof.Canonical.Generation {
			continue
		}
		if proof.Failed {
			if location.State != TransactionFailed || location.Height != fresh.Height ||
				!strings.EqualFold(location.BlockHash, fresh.BlockHash) {
				continue
			}
		} else if location.State != TransactionAbsent {
			continue
		}
		if !ConflictConfirmed(record.Original, fresh.Height, fresh.TipHeight, proof.Required) || !view.Current(fresh.Generation) {
			continue
		}
		identity := fmt.Sprintf("%s/%d/%s/%s", journal.chain, record.Original.BlockHeight, record.Original.BlockHash, record.Original.Hash)
		block := stypes.ErrataBlock{Height: record.Original.BlockHeight, DeliveryID: fmt.Sprintf("evm-conflict-%x", sha256.Sum256([]byte(identity))), Txs: []stypes.ErrataTx{{Chain: journal.chain, TxID: common.TxID(record.Original.Hash)}}}
		if err = journal.publishConflict(queue, record, fresh, proof.Required, view, block, timeout); err != nil {
			return err
		}
	}
	return nil
}

type NonceConflictRPC interface {
	TransactionLocationRPC
	CanonicalHeaderRPC
	GetBlock(int64) (*etypes.Block, error)
}

type NonceConflictEvidence struct {
	OriginalCanonical *TransactionLocation
	Conflict          *etypes.Transaction
	Failed            bool
	Canonical         CanonicalEvidence
	Required          int64
	NextHeight        int64
}

// ScanNonceConflict is one bounded step through a retained absence watch's
// range. A caller must persist NextHeight and inspect forward blocks until a
// canonical conflict or failed re-inclusion is sufficiently deep; no match has
// no expiry. The policy
// callback must compute the effective requirement of the entire conflicting
// block, not just its vault transactions or a universal confirmation cap.
func ScanNonceConflict(rpc NonceConflictRPC, validator *CanonicalValidator, original evmtypes.TransactionMeta, signer etypes.Signer, start, end, limit int64, policy func(*etypes.Block) (int64, error)) (NonceConflictEvidence, error) {
	result := NonceConflictEvidence{NextHeight: start}
	if start < 0 || end < start || limit <= 0 || policy == nil || validator == nil {
		return result, errors.New("invalid bounded nonce-conflict scan")
	}
	location, err := LocateTransaction(rpc, original.Hash)
	if err != nil {
		return result, err
	}
	if location.State == TransactionCanonical {
		result.OriginalCanonical = &location
		return result, nil
	}
	if location.State == TransactionFailed {
		// The original failed occurrence is not a replacement, and an unknown
		// original floor cannot authorize correction. Avoid whole-block policy
		// extraction (and its receipt RPCs) while either prerequisite is absent.
		if original.ConfirmationRequired == nil ||
			(location.Height == original.BlockHeight && strings.EqualFold(location.BlockHash, original.BlockHash)) {
			return result, nil
		}
		if !original.Inbound {
			// Vault and unknown originals need an accounting decision, not a TxID-wide
			// failed-execution correction. Replay once to retain any gas-only evidence.
			result.OriginalCanonical = &location
			return result, nil
		}
		// The same signed call can revert when executed in replacement history.
		// Require its exact raw inclusion, canonical proof and the whole block's
		// effective policy in addition to the retained original requirement.
		block, blockErr := rpc.GetBlock(location.Height)
		if blockErr != nil {
			return result, blockErr
		}
		if block == nil || block.Number().Int64() != location.Height ||
			!strings.EqualFold(block.Hash().Hex(), location.BlockHash) {
			return result, errors.New("failed inclusion block is inconsistent")
		}
		included := false
		for _, tx := range block.Transactions() {
			if tx != nil && tx.Hash() == ecommon.HexToHash(original.Hash) {
				included = true
				break
			}
		}
		if !included {
			return result, errors.New("failed transaction is absent from its claimed block")
		}
		canonical, canonicalErr := validator.Validate(location.Height, location.BlockHash)
		if canonicalErr != nil {
			return result, canonicalErr
		}
		required, policyErr := policy(block)
		if policyErr != nil {
			return result, policyErr
		}
		if !ConflictConfirmed(original, location.Height, canonical.TipHeight, required) {
			return result, nil
		}
		result.Failed = true
		result.Canonical = canonical
		result.Required = max(required, *original.ConfirmationRequired)
		return result, nil
	}
	if location.State != TransactionAbsent {
		return result, errors.New("original transaction absence remains uncertain")
	}

	remaining := limit
	for height := start; height <= end && remaining > 0; height++ {
		remaining--
		block, blockErr := rpc.GetBlock(height)
		if blockErr != nil {
			return result, fmt.Errorf("nonce-conflict block: %w", blockErr)
		}
		if block == nil || block.Number().Int64() != height {
			return result, errors.New("nonce-conflict block is inconsistent")
		}
		canonical, canonicalErr := validator.Validate(height, block.Hash().Hex())
		if canonicalErr != nil {
			return result, canonicalErr
		}
		// Even a receipt lookup returning NotFound cannot overrule finding our
		// original transaction in a freshly checked canonical block.
		for _, tx := range block.Transactions() {
			if tx.Hash() == ecommon.HexToHash(original.Hash) {
				result.OriginalCanonical = &TransactionLocation{State: TransactionCanonical, Transaction: tx, Height: height, BlockHash: block.Hash().Hex()}
				return result, nil
			}
		}
		if original.Sender == "" || original.Nonce == nil {
			if height == end {
				result.NextHeight = height
				return result, nil
			}
			result.NextHeight = height + 1
			continue
		}

		conflict, conflictErr := FindNonceConflict(block, original, signer)
		if conflictErr != nil {
			return result, conflictErr
		}
		if conflict != nil {
			if original.ConfirmationRequired == nil {
				return result, nil
			}
			required, policyErr := policy(block)
			if policyErr != nil {
				return result, policyErr
			}
			if !ConflictConfirmed(original, height, canonical.TipHeight, required) {
				return result, nil
			}
			result.Conflict = conflict
			result.Canonical = canonical
			result.Required = max(required, *original.ConfirmationRequired)
			return result, nil
		}
		if height == end {
			result.NextHeight = height
			return result, nil
		}
		result.NextHeight = height + 1
	}
	return result, nil
}

// A skip ends delivery, not accounting. Only a successful extraction that is
// still canonical and mature may settle its old recovery evidence. Empty and
// failed extractions never acquire SkippedCanonical.
func (j *OccurrenceJournal) settleSkippedCanonical(record OccurrenceRequest, location TransactionLocation, rpc NonceConflictRPC, view *CanonicalValidator, policy func(*etypes.Block) (int64, error)) (bool, error) {
	if record.ObserverHold || !record.SkippedCanonical || !record.Cleaned || record.NeedsRescan || location.State != TransactionCanonical || record.RescanHeight != location.Height || !strings.EqualFold(record.CompletedRescanHash, location.BlockHash) {
		return false, nil
	}
	j.mu.Lock()
	revision, err := j.txRevision(record.Original.Hash)
	j.mu.Unlock()
	if err != nil {
		return false, err
	}
	block, err := rpc.GetBlock(location.Height)
	if err != nil || block == nil || block.Number() == nil || !block.Number().IsInt64() || block.Number().Int64() != location.Height || !strings.EqualFold(block.Hash().Hex(), location.BlockHash) {
		return false, nil
	}
	included := false
	for _, tx := range block.Transactions() {
		if tx != nil && tx.Hash() == ecommon.HexToHash(record.Original.Hash) {
			included = true
			break
		}
	}
	if !included {
		return false, nil
	}
	required, err := policy(block)
	if err != nil || required < 0 {
		return false, nil
	}
	proof, err := view.Validate(location.Height, location.BlockHash)
	if err != nil {
		return false, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.generation != proof.Generation {
		return false, nil
	}
	currentRevision, err := j.txRevision(record.Original.Hash)
	if err != nil || currentRevision != revision {
		return false, err
	}
	current, err := j.loadLocked(record.Original)
	if err != nil {
		return false, err
	}
	if current == nil {
		return true, nil
	}
	if current.ObserverHold || current.CleanupGeneration != record.CleanupGeneration || !current.SkippedCanonical || current.NeedsRescan || !current.Cleaned || current.RescanHeight != location.Height || !strings.EqualFold(current.CompletedRescanHash, location.BlockHash) {
		return false, nil
	}
	// The successful canonical source is deliberately kept for a later reorg.
	// Every other source-only occurrence still blocks settlement until classified.
	canonicalID := string(j.key(evmtypes.TransactionMeta{Hash: record.Original.Hash, BlockHeight: location.Height, BlockHash: location.BlockHash}))
	meta, err := j.accessor.GetBlockMeta(location.Height)
	if err != nil {
		return false, err
	}
	if meta != nil && strings.EqualFold(meta.BlockHash, location.BlockHash) {
		for _, tx := range meta.Transactions {
			if ecommon.HexToHash(tx.Hash) == ecommon.HexToHash(record.Original.Hash) && tx.ConfirmationRequired != nil {
				if *tx.ConfirmationRequired < 0 {
					return false, nil
				}
				required = max(required, *tx.ConfirmationRequired)
			}
		}
	}
	ready, err := j.terminalTxReady(record.Original.Hash, proof, required, canonicalID)
	if err != nil || !ready {
		return false, err
	}
	if err = j.mutableLocked(current.Original); err != nil {
		return false, err
	}
	batch := new(leveldb.Batch)
	batch.Delete(j.key(current.Original))
	if current.Original.BlockHeight != location.Height || !strings.EqualFold(current.Original.BlockHash, location.BlockHash) {
		if err = j.pruneTerminalSourceLocked(batch, current.Original); err != nil {
			return false, err
		}
	}
	if err = j.write(batch); err != nil {
		return false, err
	}
	return true, nil
}
