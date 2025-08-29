//go:build integration
// +build integration

package thorchain

import (
    . "gopkg.in/check.v1"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// E2E-style test at server layer that simulates loser routing and asserts
// idempotent refund record behavior via keeper.
func (s *QuerierSuite) TestMemolessE2E_ServerLoserRouting(c *C) {
    ctx, mgr := setupManagerForTest(c)

    // Create a minimal observed tx
    tx := NewObservedTx(GetRandomTx(), 1, GetRandomPubKey(), 1)

    // Route loser outcome twice: should be idempotent
    c.Assert(routeMemolessOutcome(ctx, mgr, tx, true, "REF_COLLISION_LOST", "12345"), IsNil)
    c.Assert(routeMemolessOutcome(ctx, mgr, tx, true, "REF_COLLISION_LOST", "12345"), IsNil)

    // Verify keeper refund record exists
    km, ok := mgr.Keeper().(interface{ HasRefundRecord(ctx cosmos.Context, txidIn string) bool })
    c.Assert(ok, Equals, true)
    c.Assert(km.HasRefundRecord(ctx, tx.Tx.ID.String()), Equals, true)
}
