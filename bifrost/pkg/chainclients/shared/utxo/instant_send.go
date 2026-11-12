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

// CompleteInstantSendObservations promotes only pending markers after Observer
// stores their observations. Financial exposure is tracked by THORChain.
func (t *TemporalStorage) CompleteInstantSendObservations(ids []string) error {
	_, err := t.trackInstantSendObservations(ids, false)
	return err
}

// TrackInstantSendBlockTxs claims a verified ChainLock block's new or early
// transactions, including early observations refused by the network count gate. Like ordinary block claims, its markers precede Observer delivery.
// Only the DASH block scanner uses this fallback; other chains retain TrackObservedTxs.
func (t *TemporalStorage) TrackInstantSendBlockTxs(ids []string) ([]string, error) {
	return t.trackInstantSendObservations(ids, true)
}

// trackInstantSendObservations serializes acknowledgements with block claims.
// An acknowledgement cannot recreate a missing marker or undo a completed claim.
func (t *TemporalStorage) trackInstantSendObservations(ids []string, claimNew bool) ([]string, error) {
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
			if !claimNew {
				continue
			}
		case err != nil:
			return nil, err
		case !pending && !(claimNew && stored):
			continue
		}
		marker := id
		if !claimNew {
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
