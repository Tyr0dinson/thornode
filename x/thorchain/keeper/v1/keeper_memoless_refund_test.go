package keeperv1

import (
    . "gopkg.in/check.v1"
)

type MemolessRefundTestSuite struct{}

var _ = Suite(&MemolessRefundTestSuite{})

// TestRefundRecordIdempotency verifies SetRefundRecord/HasRefundRecord behavior.
func (s *MemolessRefundTestSuite) TestRefundRecordIdempotency(c *C) {
    ctx, k := setupKeeperForTest(c)

    txid := "TX123"

    // Initially no record
    c.Assert(k.HasRefundRecord(ctx, txid), Equals, false)

    // First set
    c.Assert(k.SetRefundRecord(ctx, txid, "REF_COLLISION_LOST"), IsNil)
    c.Assert(k.HasRefundRecord(ctx, txid), Equals, true)

    // Second set (idempotent)
    c.Assert(k.SetRefundRecord(ctx, txid, "REF_COLLISION_LOST"), IsNil)
    c.Assert(k.HasRefundRecord(ctx, txid), Equals, true)

    // Different txid remains independent
    other := "TX999"
    c.Assert(k.HasRefundRecord(ctx, other), Equals, false)
}

