package keeperv1

import (
	"fmt"
	"math"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type KeeperPendingAffiliateLiquidityFeeSuite struct{}

var _ = Suite(&KeeperPendingAffiliateLiquidityFeeSuite{})

func (s *KeeperPendingAffiliateLiquidityFeeSuite) TestAccumulateOrderAndDeleteByTx(c *C) {
	ctx, k := setupKeeperForTest(c)
	txA := GetRandomTxHash()
	txB := GetRandomTxHash()

	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txA, 2, "bravo", cosmos.NewUint(20)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txA, 0, "alpha", cosmos.NewUint(10)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txA, 0, "alpha", cosmos.NewUint(5)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txB, 0, "charlie", cosmos.NewUint(30)), IsNil)

	got, err := k.GetPendingAffiliateLiquidityFees(ctx, txA)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 2)
	c.Check(got[0].TxID.Equals(txA), Equals, true)
	c.Check(got[0].Index, Equals, uint32(0))
	c.Check(got[0].Thorname, Equals, "ALPHA")
	c.Check(got[0].Fee.Equal(cosmos.NewUint(15)), Equals, true)
	c.Check(got[1].Index, Equals, uint32(2))
	c.Check(got[1].Thorname, Equals, "BRAVO")
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txA), Equals, true)

	k.DeletePendingAffiliateLiquidityFees(ctx, txA)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txA), Equals, false)
	all, err := k.GetAllPendingAffiliateLiquidityFees(ctx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 1)
	c.Check(all[0].TxID.Equals(txB), Equals, true)
}

func (s *KeeperPendingAffiliateLiquidityFeeSuite) TestCommitIsAtomicAndIdempotent(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "alpha", cosmos.NewUint(10)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 1, "alpha", cosmos.NewUint(5)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "bravo", cosmos.NewUint(20)), IsNil)

	c.Assert(k.CommitPendingAffiliateLiquidityFees(ctx, txID), IsNil)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txID), Equals, false)
	committed := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(committed, HasLen, 2)
	c.Check(committed[0].Thorname, Equals, "ALPHA")
	c.Check(committed[0].Fee.Equal(cosmos.NewUint(15)), Equals, true)
	c.Check(committed[1].Thorname, Equals, "BRAVO")
	c.Check(committed[1].Fee.Equal(cosmos.NewUint(20)), Equals, true)

	// Replaying finality sees no pending keys and cannot double-credit.
	c.Assert(k.CommitPendingAffiliateLiquidityFees(ctx, txID), IsNil)
	committed = k.GetAffiliateLiquidityFees(ctx)
	c.Check(committed[0].Fee.Equal(cosmos.NewUint(15)), Equals, true)
	c.Check(committed[1].Fee.Equal(cosmos.NewUint(20)), Equals, true)

	// Force the normal accumulator to overflow on the second thorname. The
	// cache context must preserve both pending keys and the first accumulator.
	k.DeleteAffiliateLiquidityFees(ctx)
	txID = GetRandomTxHash()
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "BRAVO", cosmos.NewUint(math.MaxUint64)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "ALPHA", cosmos.NewUint(1)), IsNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "BRAVO", cosmos.NewUint(1)), IsNil)
	c.Assert(k.CommitPendingAffiliateLiquidityFees(ctx, txID), NotNil)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txID), Equals, true)
	committed = k.GetAffiliateLiquidityFees(ctx)
	c.Assert(committed, HasLen, 1)
	c.Check(committed[0].Thorname, Equals, "BRAVO")
}

func (s *KeeperPendingAffiliateLiquidityFeeSuite) TestValidationOverflowAndCorruptionFailClosed(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()
	overflow := cosmos.NewUint(math.MaxUint64).Add(cosmos.OneUint())

	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, common.BlankTxID, 0, "alpha", cosmos.OneUint()), NotNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "", cosmos.OneUint()), NotNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "bad/name", cosmos.OneUint()), NotNil)
	c.Assert(k.AddPendingAffiliateLiquidityFee(ctx, txID, 0, "alpha", overflow), NotNil)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txID), Equals, false)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixPendingAffiliateLiqFee, pendingAffiliateLiquidityFeeKey(txID, 0, "ALPHA"))
	store.Set(key, []byte{0xff, 0xff})
	_, err := k.GetPendingAffiliateLiquidityFees(ctx, txID)
	c.Assert(err, NotNil)
	c.Assert(k.CommitPendingAffiliateLiquidityFees(ctx, txID), NotNil)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txID), Equals, true)
	c.Check(k.GetAffiliateLiquidityFees(ctx), HasLen, 0)

	// A physically different key must not silently alias the same normalized
	// tx/index/thorname identity during commit or genesis export.
	k.DeletePendingAffiliateLiquidityFees(ctx, txID)
	nonCanonicalKey := k.GetKey(prefixPendingAffiliateLiqFee, fmt.Sprintf("%s/0/ALPHA", txID))
	k.setUint64(ctx, nonCanonicalKey, 1)
	_, err = k.GetPendingAffiliateLiquidityFees(ctx, txID)
	c.Assert(err, ErrorMatches, "non-canonical pending affiliate liquidity fee key.*")
	c.Assert(k.CommitPendingAffiliateLiquidityFees(ctx, txID), NotNil)
	c.Check(k.HasPendingAffiliateLiquidityFees(ctx, txID), Equals, true)
	c.Check(k.GetAffiliateLiquidityFees(ctx), HasLen, 0)
}
