package types

import "gitlab.com/thorchain/thornode/v3/common"

type ErrataBlock struct {
	// RetainRequirements is a local-only, synchronous durability callback. Observer
	// supplies the maximum requirements of the exact removed occurrences and their
	// saved payload history before deleting the deck. It runs under the deck lock:
	// implementations may retain policy/intent, without RPC, replay, or callbacks.
	RetainRequirements func([]ObservationOccurrence) error `json:"-"`

	// FinaliseHeights is Observer-owned local identity history. It is populated
	// from removed deck keys, not inferred from the shared recovery floor.
	FinaliseHeights map[string][]int64 `json:"-"`
	// LocalOnly invalidates one orphaned occurrence without voting network errata.
	LocalOnly bool `json:"local_only,omitempty"`
	// BlockHash is the original block, never the replacement canonical hash.
	BlockHash  string `json:"block_hash,omitempty"`
	Height     int64
	Txs        []ErrataTx
	DeliveryID string
	// Persisted is an optional in-process durability acknowledgment. The XMR
	// reorg journal waits until ObserverStorage has persisted both the errata
	// outbox record and its local on-deck cleanup, and the local errata
	// attestation has been accepted, before advancing delivery.
	// Producers must provide a buffered channel with room for one result.
	// Observer does not block if the acknowledgement receiver is unavailable.
	Persisted chan error

	// Local-only durable handoff callbacks, an alternative to Persisted for
	// producers that prefer callbacks over a channel. OnStored means the
	// Observer fsynced the errata tombstones/pending-attestation records and
	// exact deck mutation; the chain scanner may then advance its reorg
	// journal. Attestation itself is retried durably by the Observer.
	OnStored   func()
	OnRejected func(error)
}

type ErrataTx struct {
	TxID  common.TxID
	Chain common.Chain
}
