package observer

// Phase 1: Deterministic collision resolution (scaffold)
//
// This file introduces types and a pure function to determine a single
// winner among multiple memoless inbound candidates that share the same
// (chain_id, vault_pubkey, reference). The deterministic rule is the
// lexicographically smallest (inbound_height, txid). Wiring into the
// handler is done in a subsequent change to keep behavior unchanged.

// CollisionCandidate represents an inbound candidate for a memoless
// transaction that shares the same (chain_id, vault_pubkey, reference)
// scope with other candidates.
type CollisionCandidate struct {
    ChainID       string
    VaultPubKey   string // string form to avoid importing additional deps here
    Reference     string
    InboundHeight int64
    TxID          string
}

// lessByHeightTx returns true if a is strictly smaller than b according to the
// lexicographic order (InboundHeight, TxID).
func lessByHeightTx(a, b CollisionCandidate) bool {
    if a.InboundHeight < b.InboundHeight {
        return true
    }
    if a.InboundHeight > b.InboundHeight {
        return false
    }
    return a.TxID < b.TxID
}

// DetermineCollisionWinner selects a single winner and returns the remaining
// elements as losers using the deterministic rule above. If candidates is
// empty, the zero-value winner is returned.
func DetermineCollisionWinner(candidates []CollisionCandidate) (winner CollisionCandidate, losers []CollisionCandidate) {
    if len(candidates) == 0 {
        return CollisionCandidate{}, nil
    }
    // Find min per (height, txid)
    minIdx := 0
    for i := 1; i < len(candidates); i++ {
        if lessByHeightTx(candidates[i], candidates[minIdx]) {
            minIdx = i
        }
    }
    // Build losers slice without realloc if possible
    losers = make([]CollisionCandidate, 0, len(candidates)-1)
    for i := 0; i < len(candidates); i++ {
        if i == minIdx {
            continue
        }
        losers = append(losers, candidates[i])
    }
    return candidates[minIdx], losers
}

