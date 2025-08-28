package observer

import (
    "fmt"

    btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// refundRecorder abstracts idempotency tracking for refunds. In production,
// this will be backed by Keeper via the bridge; tests can use an in-memory
// implementation to assert behavior without network calls.
type refundRecorder interface {
    HasRefundRecord(txidIn string) bool
    SetRefundRecord(txidIn, reason string) error
}

// inMemoryRefundRecorder is a simple map-backed recorder for unit tests.
type inMemoryRefundRecorder struct {
    m map[string]string
}

func newInMemoryRefundRecorder() *inMemoryRefundRecorder {
    return &inMemoryRefundRecorder{m: make(map[string]string)}
}

func (r *inMemoryRefundRecorder) HasRefundRecord(txidIn string) bool {
    _, ok := r.m[txidIn]
    return ok
}

func (r *inMemoryRefundRecorder) SetRefundRecord(txidIn, reason string) error {
    r.m[txidIn] = reason
    return nil
}

// ScheduleLoserRefund is a Phase 2 stub for scheduling an idempotent refund
// for a collision loser or unavailable registration. The final implementation
// will consult keeper idempotency records and construct a canonical refund memo.
//
// For now, this is a placeholder to anchor tests and wiring work; it returns
// an empty job id and no error.
func (m *MemolessHandler) ScheduleLoserRefund(txIn *btypes.TxInItem, reason, reference, winnerTxID string) (string, error) {
    if txIn == nil {
        return "", fmt.Errorf("nil tx input")
    }
    // Idempotency check
    if m.rr != nil && m.rr.HasRefundRecord(txIn.Tx) {
        return "", nil
    }
    if m.rr != nil {
        if err := m.rr.SetRefundRecord(txIn.Tx, reason); err != nil {
            return "", err
        }
    }
    // Build canonical refund memo (placeholder used later in plumbing)
    _ = buildCanonicalRefundMemo(reason, reference, txIn.Tx)
    // TODO: Integrate with refund plumbing; return job id when available
    return "", nil
}

// buildCanonicalRefundMemo constructs the canonical refund memo string for
// memoless loser/refund scenarios.
func buildCanonicalRefundMemo(reason, reference, txidIn string) string {
    return fmt.Sprintf("REFUND:MEMOLESS:%s:%s:%s", reason, reference, txidIn)
}
