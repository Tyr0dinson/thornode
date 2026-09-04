package generic

import "time"

// Scanner provides shared state for chain-specific block scanners.
type Scanner struct {
	blockReferences *BlockReferenceStore
}

// NewScanner creates a scanner with a persistent block-reference store.
func NewScanner(
	state StateStore,
	maxBlockReferences int,
	retention time.Duration,
) (*Scanner, error) {
	blockReferences, err := NewBlockReferenceStore(state, maxBlockReferences, retention)
	if err != nil {
		return nil, err
	}
	return &Scanner{blockReferences: blockReferences}, nil
}

// SetBlockReference adds or verifies a canonical block reference.
func (s *Scanner) SetBlockReference(reference BlockReference) error {
	return s.blockReferences.Set(reference)
}

// ResolveBlockReference returns the exact reference proven by a consecutive
// upper block boundary.
func (s *Scanner) ResolveBlockReference(timestamp int64) (BlockReference, error) {
	return s.blockReferences.Resolve(timestamp)
}
