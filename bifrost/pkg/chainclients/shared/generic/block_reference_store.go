package generic

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// blockReferencePrefix identifies block-reference entries.
const blockReferencePrefix = "scanner/block-references/"

// blockReferenceKey pads heights so keys sort numerically.
func blockReferenceKey(height int64) string {
	return fmt.Sprintf("%s%020d", blockReferencePrefix, height)
}

// ErrBlockReferenceUnavailable means the store does not contain enough
// consecutive blocks to prove which block was current at a requested time.
var ErrBlockReferenceUnavailable = errors.New("block reference unavailable")

// BlockReference identifies one block on the external chain being scanned.
// Timestamp is its on-chain Unix time in milliseconds.
type BlockReference struct {
	Height    int64  `json:"-"`
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

// BlockReferenceStore persists a bounded, concurrency-safe block index.
type BlockReferenceStore struct {
	mu            sync.RWMutex
	state         StateStore
	maxReferences int
	retention     int64
	references    []BlockReference
}

// NewBlockReferenceStore loads an index bounded by count and retention.
// Resolution requires at least two entries.
func NewBlockReferenceStore(
	state StateStore,
	maxReferences int,
	retention time.Duration,
) (*BlockReferenceStore, error) {
	if state == nil {
		return nil, fmt.Errorf("state store is nil")
	}
	if maxReferences < 2 {
		return nil, fmt.Errorf("max block references must be at least 2")
	}
	if retention <= 0 {
		return nil, fmt.Errorf("block reference retention must be positive")
	}

	s := &BlockReferenceStore{
		state:         state,
		maxReferences: maxReferences,
		retention:     retention.Milliseconds(),
	}
	if err := s.load(); err != nil {
		return nil, err
	}

	return s, nil
}

// load restores references and applies the configured limits.
func (s *BlockReferenceStore) load() error {
	err := s.state.Iterate(blockReferencePrefix, func(key string, value []byte) error {
		var reference BlockReference
		if err := json.Unmarshal(value, &reference); err != nil {
			return fmt.Errorf("failed to unmarshal block reference %s: %w", key, err)
		}
		height, err := strconv.ParseInt(strings.TrimPrefix(key, blockReferencePrefix), 10, 64)
		if err != nil {
			return fmt.Errorf("failed to parse block reference height from %s: %w", key, err)
		}
		reference.Height = height
		s.references = append(s.references, reference)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to load block references: %w", err)
	}
	if err = s.validateBlockReferences(); err != nil {
		return fmt.Errorf("invalid persisted block references: %w", err)
	}

	kept, dropped := s.prune(s.references)
	for _, reference := range dropped {
		if err = s.state.Delete(blockReferenceKey(reference.Height)); err != nil {
			return fmt.Errorf("failed to drop stale block reference: %w", err)
		}
	}
	s.references = kept

	return nil
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
	kept, dropped := s.prune(next)

	// Persist the reference only if pruning retained it.
	if kept[0].Height <= reference.Height {
		value, err := json.Marshal(reference)
		if err != nil {
			return fmt.Errorf("failed to marshal block reference: %w", err)
		}
		if err = s.state.Put(blockReferenceKey(reference.Height), value); err != nil {
			return fmt.Errorf("failed to persist block reference: %w", err)
		}
	}
	s.references = kept

	for _, stale := range dropped {
		if err := s.state.Delete(blockReferenceKey(stale.Height)); err != nil {
			log.Warn().Err(err).
				Int64("height", stale.Height).
				Msg("failed to delete pruned block reference")
		}
	}

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
	if len(s.references) == 0 {
		return BlockReference{}, fmt.Errorf(
			"%w for timestamp %d: index is empty",
			ErrBlockReferenceUnavailable, targetTimestamp,
		)
	}
	if i == 0 {
		oldest := s.references[0]
		return BlockReference{}, fmt.Errorf(
			"%w for timestamp %d: %dms older than the oldest indexed block %d",
			ErrBlockReferenceUnavailable, targetTimestamp,
			oldest.Timestamp-targetTimestamp, oldest.Height,
		)
	}
	if i == len(s.references) {
		newest := s.references[len(s.references)-1]
		return BlockReference{}, fmt.Errorf(
			"%w for timestamp %d: %dms newer than the newest indexed block %d",
			ErrBlockReferenceUnavailable, targetTimestamp,
			targetTimestamp-newest.Timestamp, newest.Height,
		)
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

// prune applies retention and count limits while keeping two boundaries.
func (s *BlockReferenceStore) prune(references []BlockReference) (kept, dropped []BlockReference) {
	cut := 0
	if len(references) > 2 {
		cutoff := references[len(references)-1].Timestamp - s.retention
		cut = sort.Search(len(references), func(i int) bool {
			return references[i].Timestamp >= cutoff
		})
		if max := len(references) - 2; cut > max {
			cut = max
		}
	}
	if over := len(references) - cut - s.maxReferences; over > 0 {
		cut += over
	}
	return references[cut:], references[:cut]
}

// validateBlockReferences validates loaded references.
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
