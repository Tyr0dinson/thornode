package thorchain

import (
    "fmt"

    "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// processMemolessLoserRefund schedules a server-side refund for a memoless
// collision loser or unavailable registration with idempotency.
func processMemolessLoserRefund(ctx cosmos.Context, mgr *Mgrs, tx ObservedTx, reason, reference string) error {
    // Idempotency via keeper memoless refund records
    if km, ok := mgr.Keeper().(interface{ HasRefundRecord(ctx cosmos.Context, txidIn string) bool; SetRefundRecord(ctx cosmos.Context, txidIn, reason string) error }); ok {
        txid := tx.Tx.ID.String()
        if km.HasRefundRecord(ctx, txid) {
            return nil
        }
        if err := km.SetRefundRecord(ctx, txid, reason); err != nil {
            return err
        }
    }

    // Use canonical memo format for auditability
    memo := fmt.Sprintf("REFUND:MEMOLESS:%s:%s:%s", reason, reference, tx.Tx.ID.String())
    // Schedule refund using existing helper
    // Choose a generic refund code for memoless losers; this can be refined
    // or made reason-specific as needed.
    const refundCode = CodeTxFail
    return refundTx(ctx, tx, mgr, refundCode, memo, "")
}

// routeMemolessOutcome routes memoless outcomes to the correct server-side
// handling. For losers or unavailable registrations, it schedules an
// idempotent refund via processMemolessLoserRefund. For winners, it is a no-op.
func routeMemolessOutcome(ctx cosmos.Context, mgr *Mgrs, tx ObservedTx, isLoser bool, reason, reference string) error {
    if !isLoser {
        return nil
    }
    return processMemolessLoserRefund(ctx, mgr, tx, reason, reference)
}
