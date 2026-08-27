package generic

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Keep this key stable so extracting the store does not require a data migration.
const blockReferencesStateKey = "scanner/block-references"

// ErrBlockReferenceUnavailable means the store does not contain enough
// consecutive blocks to prove which block was current at a requested time.
var ErrBlockReferenceUnavailable = errors.New("block reference unavailable")

// BlockReference identifies one block on the external chain being scanned.
// Timestamp is the block's on-chain Unix time in milliseconds; Height orders
// references and Hash is the canonical block ID.
type BlockReference struct {
	Height    int64  `json:"height"`
	Hash      string `json:"hash"`
	Timestamp int64  `json:"timestamp"`
}

// Validate checks the fields required for storing and resolving a reference.
func (reference BlockReference) Validate() error {
	if reference.Height < 0 {
		return fmt.Errorf("block reference height is negative")
	}
	if reference.Hash == "" {
		return fmt.Errorf("block reference hash is empty")
	}
	if reference.Timestamp <= 0 {
		return fmt.Errorf("block reference timestamp must be positive")
	}
	return nil
}

// BlockReferenceStore is a bounded, persistent, concurrency-safe index of
// block references. It owns the reference schema and all lookup guarantees;
// its StateStore remains responsible only for chain-isolated bytes.
type BlockReferenceStore struct {
	mu            sync.RWMutex
	state         StateStore
	maxReferences int
	references    []BlockReference
}

// NewBlockReferenceStore restores a block-reference index from state. At least
// two entries are required because exact resolution needs both boundaries.
func NewBlockReferenceStore(state StateStore, maxReferences int) (*BlockReferenceStore, error) {
	if state == nil {
		return nil, fmt.Errorf("state store is nil")
	}
	if maxReferences < 2 {
		return nil, fmt.Errorf("max block references must be at least 2")
	}

	s := &BlockReferenceStore{
		state:         state,
		maxReferences: maxReferences,
	}
	value, found, err := state.Get(blockReferencesStateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load block references: %w", err)
	}
	if !found {
		return s, nil
	}
	if err = json.Unmarshal(value, &s.references); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block references: %w", err)
	}
	if err = s.validateBlockReferences(); err != nil {
		return nil, fmt.Errorf("invalid persisted block references: %w", err)
	}
	if len(s.references) > maxReferences {
		s.references = append([]BlockReference(nil), s.references[len(s.references)-maxReferences:]...)
		if err = s.persist(s.references); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// Set inserts a reference by height. Calls may arrive out of order because the
// standard block scanner prefetches blocks concurrently.
func (s *BlockReferenceStore) Set(reference BlockReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	i := sort.Search(len(s.references), func(i int) bool {
		return s.references[i].Height >= reference.Height
	})
	if i < len(s.references) && s.references[i].Height == reference.Height {
		if s.references[i] == reference {
			return nil
		}
		return fmt.Errorf("conflicting block reference at height %d", reference.Height)
	}
	if i > 0 && s.references[i-1].Timestamp > reference.Timestamp {
		return fmt.Errorf("block timestamp decreases at height %d", reference.Height)
	}
	if i < len(s.references) && reference.Timestamp > s.references[i].Timestamp {
		return fmt.Errorf("block timestamp decreases after height %d", reference.Height)
	}

	next := make([]BlockReference, 0, len(s.references)+1)
	next = append(next, s.references[:i]...)
	next = append(next, reference)
	next = append(next, s.references[i:]...)
	if len(next) > s.maxReferences {
		next = next[len(next)-s.maxReferences:]
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.references = next

	return nil
}

// Resolve returns the exact block current at targetTimestamp. The following
// consecutive block must also be known and have a later timestamp; otherwise
// different scanner tips could produce different signing payloads.
func (s *BlockReferenceStore) Resolve(targetTimestamp int64) (BlockReference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	i := sort.Search(len(s.references), func(i int) bool {
		return s.references[i].Timestamp > targetTimestamp
	})
	if i == 0 || i == len(s.references) {
		return BlockReference{}, fmt.Errorf("%w for timestamp %d", ErrBlockReferenceUnavailable, targetTimestamp)
	}
	selected := s.references[i-1]
	next := s.references[i]
	if next.Height != selected.Height+1 {
		return BlockReference{}, fmt.Errorf(
			"%w for timestamp %d: missing height after %d",
			ErrBlockReferenceUnavailable,
			targetTimestamp,
			selected.Height,
		)
	}

	return selected, nil
}

// Rewind removes references above height.
func (s *BlockReferenceStore) Rewind(height int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := sort.Search(len(s.references), func(i int) bool {
		return s.references[i].Height > height
	})
	if i == len(s.references) {
		return nil
	}
	next := append([]BlockReference(nil), s.references[:i]...)
	if err := s.persist(next); err != nil {
		return err
	}
	s.references = next
	return nil
}

func (s *BlockReferenceStore) persist(references []BlockReference) error {
	value, err := json.Marshal(references)
	if err != nil {
		return fmt.Errorf("failed to marshal block references: %w", err)
	}
	if err = s.state.Put(blockReferencesStateKey, value); err != nil {
		return fmt.Errorf("failed to persist block references: %w", err)
	}
	return nil
}

// validateBlockReferences validates references restored during construction,
// before the store is shared and requires locking.
func (s *BlockReferenceStore) validateBlockReferences() error {
	for i, reference := range s.references {
		if err := reference.Validate(); err != nil {
			return fmt.Errorf("invalid block reference at index %d: %w", i, err)
		}
		if i == 0 {
			continue
		}
		previous := s.references[i-1]
		if reference.Height <= previous.Height {
			return fmt.Errorf("block heights are not strictly increasing")
		}
		if reference.Timestamp < previous.Timestamp {
			return fmt.Errorf("block timestamps are decreasing")
		}
	}
	return nil
}
