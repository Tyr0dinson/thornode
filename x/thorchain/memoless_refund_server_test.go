package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MemolessServerRefundSuite struct{}

var _ = Suite(&MemolessServerRefundSuite{})

func (s *MemolessServerRefundSuite) TestProcessMemolessLoserRefund_Idempotency(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Build a minimal observed tx
	tx := NewObservedTx(GetRandomTx(), 1, GetRandomPubKey(), 1)

	// First call should set refund record and schedule a refund
	err := processMemolessLoserRefund(ctx, mgr, tx, "REF_COLLISION_LOST", "12345")
	c.Assert(err, IsNil)

	// Second call should be idempotent and return nil without duplicating state
	err = processMemolessLoserRefund(ctx, mgr, tx, "REF_COLLISION_LOST", "12345")
	c.Assert(err, IsNil)

	// Verify keeper refund record is present
	km, ok := mgr.Keeper().(interface {
		HasRefundRecord(ctx cosmos.Context, txidIn string) bool
	})
	c.Assert(ok, Equals, true)
	c.Assert(km.HasRefundRecord(ctx, tx.Tx.ID.String()), Equals, true)
}

// Hook location note: routeMemolessOutcome is intended to be invoked in the
// ObservedTxIn processing path once a memoless loser condition is signaled
// (post-detection in Bifrost). This test drives the hook contract.
func (s *MemolessServerRefundSuite) TestRouteMemolessOutcome_LoserTriggersRefund(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Build a minimal observed tx
	tx := NewObservedTx(GetRandomTx(), 1, GetRandomPubKey(), 1)

	// Route loser outcome should trigger refund and be idempotent
	err := routeMemolessOutcome(ctx, mgr, tx, true /*isLoser*/, "REF_COLLISION_LOST", "12345")
	c.Assert(err, IsNil)

	// Second call should be idempotent
	err = routeMemolessOutcome(ctx, mgr, tx, true, "REF_COLLISION_LOST", "12345")
	c.Assert(err, IsNil)
}
