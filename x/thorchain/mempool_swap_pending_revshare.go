package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// pendingMempoolSwapAffiliateIdentity binds the queue identity to every AMM
// leg executed by one MsgSwap. Keeping this in the context avoids changing the
// versioned Swapper interface solely for the v1 pending-REVSHARE journal.
type pendingMempoolSwapAffiliateIdentity struct {
	txID  common.TxID
	index uint32
}

type pendingMempoolSwapAffiliateIdentityContextKey struct{}

func withPendingMempoolSwapAffiliateIdentity(ctx cosmos.Context, txID common.TxID, index uint32) cosmos.Context {
	return ctx.WithValue(
		pendingMempoolSwapAffiliateIdentityContextKey{},
		pendingMempoolSwapAffiliateIdentity{txID: txID, index: index},
	)
}

func getPendingMempoolSwapAffiliateIdentity(ctx cosmos.Context) (pendingMempoolSwapAffiliateIdentity, bool) {
	identity, ok := ctx.Value(pendingMempoolSwapAffiliateIdentityContextKey{}).(pendingMempoolSwapAffiliateIdentity)
	if !ok || identity.txID.IsEmpty() {
		return pendingMempoolSwapAffiliateIdentity{}, false
	}
	return identity, true
}

// prepareMempoolSwapAffiliateContext selects where swap-time REVSHARE is
// recorded. An admitted, unfinalized mempool swap journals its exact realized
// liquidity fees; a finalized swap first flushes any prior speculative slices
// and then records new slices in the ordinary per-block accumulator.
func prepareMempoolSwapAffiliateContext(
	ctx cosmos.Context,
	k keeper.Keeper,
	msg MsgSwap,
) (cosmos.Context, error) {
	voter, err := k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	if err != nil {
		return ctx, fmt.Errorf("fail to load swap voter for pending affiliate fees: %w", err)
	}
	if !voter.HasReservedMempoolSwapTOR() {
		return ctx, nil
	}
	if voter.FinalisedHeight == 0 {
		return withPendingMempoolSwapAffiliateIdentity(ctx, msg.Tx.ID, msg.Index), nil
	}
	if err := commitPendingAffiliateLiquidityFeesAtomic(ctx, k, msg.Tx.ID); err != nil {
		return ctx, fmt.Errorf("fail to commit pending affiliate fees for finalized swap: %w", err)
	}
	return ctx, nil
}

// commitPendingAffiliateLiquidityFeesAtomic moves every pending slice/leg for
// an inbound into the normal per-block accumulator exactly once. Both the
// additions and source-key deletions are discarded if any entry fails.
func commitPendingAffiliateLiquidityFeesAtomic(ctx cosmos.Context, k keeper.Keeper, txID common.TxID) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := k.CommitPendingAffiliateLiquidityFees(cacheCtx, txID); err != nil {
		return err
	}
	commit()
	return nil
}
