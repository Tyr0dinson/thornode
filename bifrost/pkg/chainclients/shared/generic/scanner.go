package generic

// Scanner is the reusable base for chain-specific block scanners. Shared
// scanner capabilities belong here; their data handling stays in contained
// stores so this type can grow without mixing persistence concerns together.
type Scanner struct {
	blockReferences *BlockReferenceStore
}

// NewScanner creates a shared scanner with a persistent block-reference store.
func NewScanner(state StateStore, maxBlockReferences int) (*Scanner, error) {
	blockReferences, err := NewBlockReferenceStore(state, maxBlockReferences)
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

// RewindBlockReferences removes references above height.
func (s *Scanner) RewindBlockReferences(height int64) error {
	return s.blockReferences.Rewind(height)
}
