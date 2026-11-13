package types

import (
	"errors"
	"strings"
	"sync"
)

// TrackObservationHolds distinguishes an acknowledged attempt from a durable
// handoff. An orphan rejection can describe a canonical A-B-A return fenced by
// Observer's signature history, whose recovery evidence is intended to be
// retained even though the batch may advance; permanent revert and
// not-applicable remain terminal. This installs the ObservationHeld hook, but
// the scanner consumers that honor it (rather than treating an orphan rejection
// as terminal) land in later commits of this stack.
// Install before delivery; read only after Persisted has acknowledged the batch.
func (t *TxIn) TrackObservationHolds() {
	var held sync.Map
	t.ObservationHeld = func(txID string) bool {
		_, ok := held.Load(strings.TrimPrefix(strings.ToLower(txID), "0x"))
		return ok
	}
	for _, item := range t.TxArray {
		if item == nil {
			continue
		}
		id := strings.TrimPrefix(strings.ToLower(item.Tx), "0x")
		rejected := item.OnObservedTxRejected
		item.OnObservedTxRejected = func(err error) {
			if errors.Is(err, ErrObservedTxOrphaned) {
				held.Store(id, true)
			}
			if rejected != nil {
				rejected(err)
			}
		}
	}
}
