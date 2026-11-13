package utxo

import (
	"errors"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const (
	// A pending marker suppresses another InstantSend claim, but allows the
	// ChainLock block scan to deliver independently of early admission. Block claims
	// retain the transaction ID as their completed marker.
	instantSendPendingMarker = "instant-send-pending-v1"

	// The observer has stored the early observation locally; network credit is not implied.
	// An independent ChainLock handoff is still required.
	instantSendStoredMarker = "instant-send-delivered-v1"
)

// ClaimInstantSendTxs persists delivery claims, not financial allowance. Core
// enforces exposure limits; ordinary ChainLock delivery stays independently eligible.
func (t *TemporalStorage) ClaimInstantSendTxs(ids []string) ([]string, error) {
	t.observedTxMu.Lock()
	defer t.observedTxMu.Unlock()
	batch := new(leveldb.Batch)
	found := make(map[string]bool)
	var accepted []string
	for _, id := range ids {
		id = strings.ToLower(id)
		if id == "" {
			return nil, errors.New("empty InstantSend transaction ID")
		}
		if found[id] {
			continue
		}
		found[id] = true
		key := []byte(t.getObservedTxKey(id))
		exists, err := t.db.Has(key, nil)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		batch.Put(key, []byte(instantSendPendingMarker))
		accepted = append(accepted, id)
	}
	if batch.Len() > 0 {
		err := t.db.Write(batch, &opt.WriteOptions{Sync: true})
		if err != nil {
			return nil, err
		}
	}
	return accepted, nil
}

// MarkInstantSendStored records Observer persistence for pending InstantSend claims.
// Financial exposure is tracked by THORChain.
func (t *TemporalStorage) MarkInstantSendStored(ids []string) error {
	_, err := t.markInstantSendObservations(ids, false)
	return err
}

// FilterNewBlockTxs selects transactions without a completed block observation,
// including those already observed through InstantSend. It leaves markers unchanged
// so a failed Observer handoff remains eligible for a rescan.
func (t *TemporalStorage) FilterNewBlockTxs(ids []string) ([]string, error) {
	t.observedTxMu.Lock()
	defer t.observedTxMu.Unlock()
	found := make(map[string]bool)
	var accepted []string
	for _, id := range ids {
		id = strings.ToLower(id)
		if id == "" {
			return nil, errors.New("empty InstantSend transaction ID")
		}
		if found[id] {
			continue
		}
		found[id] = true
		value, err := t.db.Get([]byte(t.getObservedTxKey(id)), nil)
		if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return nil, err
		}
		pending := string(value) == instantSendPendingMarker
		stored := string(value) == instantSendStoredMarker
		if errors.Is(err, leveldb.ErrNotFound) || pending || stored {
			accepted = append(accepted, id)
		}
	}
	return accepted, nil
}

// CompleteBlockTxs completes delivery markers after Observer stores a block batch
// durably. Only DASH uses this handoff; other chains retain TrackObservedTxs.
func (t *TemporalStorage) CompleteBlockTxs(ids []string) ([]string, error) {
	return t.markInstantSendObservations(ids, true)
}

// markInstantSendObservations records early Observer persistence or, when
// chainLocked is true, completed delivery of ChainLocked block observations.
// Early acknowledgements cannot recreate missing markers or undo block delivery.
func (t *TemporalStorage) markInstantSendObservations(ids []string, chainLocked bool) ([]string, error) {
	t.observedTxMu.Lock()
	defer t.observedTxMu.Unlock()
	batch := new(leveldb.Batch)
	found := make(map[string]bool)
	var accepted []string
	for _, id := range ids {
		id = strings.ToLower(id)
		if id == "" {
			return nil, errors.New("empty InstantSend transaction ID")
		}
		if found[id] {
			continue
		}
		found[id] = true
		key := []byte(t.getObservedTxKey(id))
		value, err := t.db.Get(key, nil)
		pending := string(value) == instantSendPendingMarker
		stored := string(value) == instantSendStoredMarker
		switch {
		case errors.Is(err, leveldb.ErrNotFound):
			if !chainLocked {
				continue
			}
		case err != nil:
			return nil, err
		case !pending && !(chainLocked && stored):
			continue
		}
		marker := id
		if !chainLocked {
			marker = instantSendStoredMarker
		}
		batch.Put(key, []byte(marker))
		accepted = append(accepted, id)
	}
	if batch.Len() > 0 {
		err := t.db.Write(batch, &opt.WriteOptions{Sync: true})
		if err != nil {
			return nil, err
		}
	}
	return accepted, nil
}
