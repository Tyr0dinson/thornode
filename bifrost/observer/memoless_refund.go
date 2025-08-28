package observer

import (
    "fmt"

    btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// ScheduleLoserRefund is a Phase 2 stub for scheduling an idempotent refund
// for a collision loser or unavailable registration. The final implementation
// will consult keeper idempotency records and construct a canonical refund memo.
//
// For now, this is a placeholder to anchor tests and wiring work; it returns
// an empty job id and no error.
func (m *MemolessHandler) ScheduleLoserRefund(txIn *btypes.TxInItem, reason, reference, winnerTxID string) (string, error) {
    // NOTE: This will be implemented in Phase 2. A proper implementation will:
    // 1) Check HasRefundRecord(txIn.Tx)
    // 2) If not present, SetRefundRecord(txIn.Tx, reason)
    // 3) Build canonical refund memo REFUND:MEMOLESS:<reason>:<ref>:<txid_in>
    // 4) Schedule refund via existing refund plumbing
    _ = fmt.Sprintf
    return "", nil
}
