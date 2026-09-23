package keeperv1

import (
	"github.com/cosmos/cosmos-sdk/runtime"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type KeeperNodeBlameSuite struct{}

var _ = Suite(&KeeperNodeBlameSuite{})

func (s *KeeperNodeBlameSuite) TestGetNodeBlameRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test getting non-existent record returns empty record with address set
	addr := GetRandomBech32Addr()
	record, err := k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(record.NodeAddress.Equals(addr), Equals, true)
	c.Check(record.KeygenBlameCountTotal, Equals, int64(0))
	c.Check(record.KeysignBlameCountTotal, Equals, int64(0))
	c.Check(record.KeygenBlameCountSinceChurn, Equals, int64(0))
	c.Check(record.KeysignBlameCountSinceChurn, Equals, int64(0))
}

func (s *KeeperNodeBlameSuite) TestSetNodeBlameRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomBech32Addr()
	record := NewNodeBlameRecord(addr)
	record.KeygenBlameCountTotal = 5
	record.KeysignBlameCountTotal = 3
	record.KeygenBlameCountSinceChurn = 2
	record.KeysignBlameCountSinceChurn = 1

	err := k.SetNodeBlameRecord(ctx, record)
	c.Assert(err, IsNil)

	// Retrieve and verify
	retrieved, err := k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(retrieved.NodeAddress.Equals(addr), Equals, true)
	c.Check(retrieved.KeygenBlameCountTotal, Equals, int64(5))
	c.Check(retrieved.KeysignBlameCountTotal, Equals, int64(3))
	c.Check(retrieved.KeygenBlameCountSinceChurn, Equals, int64(2))
	c.Check(retrieved.KeysignBlameCountSinceChurn, Equals, int64(1))

	// Test error case: empty address
	emptyRecord := NodeBlameRecord{}
	err = k.SetNodeBlameRecord(ctx, emptyRecord)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "node address cannot be empty")
}

func (s *KeeperNodeBlameSuite) TestIncrementKeygenBlame(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomBech32Addr()

	// First increment
	err := k.IncrementKeygenBlame(ctx, addr)
	c.Assert(err, IsNil)

	record, err := k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(record.KeygenBlameCountTotal, Equals, int64(1))
	c.Check(record.KeygenBlameCountSinceChurn, Equals, int64(1))
	c.Check(record.KeysignBlameCountTotal, Equals, int64(0))
	c.Check(record.KeysignBlameCountSinceChurn, Equals, int64(0))

	// Second increment
	err = k.IncrementKeygenBlame(ctx, addr)
	c.Assert(err, IsNil)

	record, err = k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(record.KeygenBlameCountTotal, Equals, int64(2))
	c.Check(record.KeygenBlameCountSinceChurn, Equals, int64(2))
}

func (s *KeeperNodeBlameSuite) TestIncrementKeysignBlame(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomBech32Addr()

	// First increment
	err := k.IncrementKeysignBlame(ctx, addr)
	c.Assert(err, IsNil)

	record, err := k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(record.KeysignBlameCountTotal, Equals, int64(1))
	c.Check(record.KeysignBlameCountSinceChurn, Equals, int64(1))
	c.Check(record.KeygenBlameCountTotal, Equals, int64(0))
	c.Check(record.KeygenBlameCountSinceChurn, Equals, int64(0))

	// Second increment
	err = k.IncrementKeysignBlame(ctx, addr)
	c.Assert(err, IsNil)

	record, err = k.GetNodeBlameRecord(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(record.KeysignBlameCountTotal, Equals, int64(2))
	c.Check(record.KeysignBlameCountSinceChurn, Equals, int64(2))
}

func (s *KeeperNodeBlameSuite) TestCalculateKeygenPenaltyMultiplier(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomBech32Addr()

	// Test with no blames - should return 1
	mult, err := k.CalculateKeygenPenaltyMultiplier(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(mult, Equals, int64(1))

	// Add some blames
	for i := 0; i < 3; i++ {
		err = k.IncrementKeygenBlame(ctx, addr)
		c.Assert(err, IsNil)
	}

	// Test with no cap (MaxKeygenBlameMultiplier = 0, default)
	mult, err = k.CalculateKeygenPenaltyMultiplier(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(mult, Equals, int64(3))

	// Add more blames
	for i := 0; i < 7; i++ {
		err = k.IncrementKeygenBlame(ctx, addr)
		c.Assert(err, IsNil)
	}

	// Should now be 10 blames, returned without cap
	mult, err = k.CalculateKeygenPenaltyMultiplier(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(mult, Equals, int64(10))

	// Set a cap via mimir
	k.SetMimir(ctx, constants.MaxKeygenBlameMultiplier.String(), 5)

	// Should be capped at 5
	mult, err = k.CalculateKeygenPenaltyMultiplier(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(mult, Equals, int64(5))

	// Test with cap higher than count
	k.SetMimir(ctx, constants.MaxKeygenBlameMultiplier.String(), 20)
	mult, err = k.CalculateKeygenPenaltyMultiplier(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(mult, Equals, int64(10))
}

func (s *KeeperNodeBlameSuite) TestResetSinceChurnCountersForMembers(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create multiple nodes with blame records
	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	addr3 := GetRandomBech32Addr()

	// Set up node 1 with keygen blames
	for i := 0; i < 3; i++ {
		err := k.IncrementKeygenBlame(ctx, addr1)
		c.Assert(err, IsNil)
	}

	// Set up node 2 with keysign blames
	for i := 0; i < 2; i++ {
		err := k.IncrementKeysignBlame(ctx, addr2)
		c.Assert(err, IsNil)
	}

	// Set up node 3 with both
	err := k.IncrementKeygenBlame(ctx, addr3)
	c.Assert(err, IsNil)
	err = k.IncrementKeysignBlame(ctx, addr3)
	c.Assert(err, IsNil)

	// Verify initial state
	record1, _ := k.GetNodeBlameRecord(ctx, addr1)
	c.Check(record1.KeygenBlameCountTotal, Equals, int64(3))
	c.Check(record1.KeygenBlameCountSinceChurn, Equals, int64(3))

	record2, _ := k.GetNodeBlameRecord(ctx, addr2)
	c.Check(record2.KeysignBlameCountTotal, Equals, int64(2))
	c.Check(record2.KeysignBlameCountSinceChurn, Equals, int64(2))

	record3, _ := k.GetNodeBlameRecord(ctx, addr3)
	c.Check(record3.KeygenBlameCountTotal, Equals, int64(1))
	c.Check(record3.KeysignBlameCountTotal, Equals, int64(1))
	c.Check(record3.KeygenBlameCountSinceChurn, Equals, int64(1))
	c.Check(record3.KeysignBlameCountSinceChurn, Equals, int64(1))

	// Reset since_churn counters only for nodes 1 and 3 (just-succeeded keygen members)
	err = k.ResetSinceChurnCountersForMembers(ctx, []cosmos.AccAddress{addr1, addr3})
	c.Assert(err, IsNil)

	// Node 1 reset: total preserved, since_churn cleared
	record1, _ = k.GetNodeBlameRecord(ctx, addr1)
	c.Check(record1.KeygenBlameCountTotal, Equals, int64(3))
	c.Check(record1.KeygenBlameCountSinceChurn, Equals, int64(0))

	// Node 2 NOT a member of the succeeded keygen; counters must be untouched
	record2, _ = k.GetNodeBlameRecord(ctx, addr2)
	c.Check(record2.KeysignBlameCountTotal, Equals, int64(2))
	c.Check(record2.KeysignBlameCountSinceChurn, Equals, int64(2))

	// Node 3 reset: totals preserved, both since_churn cleared
	record3, _ = k.GetNodeBlameRecord(ctx, addr3)
	c.Check(record3.KeygenBlameCountTotal, Equals, int64(1))
	c.Check(record3.KeysignBlameCountTotal, Equals, int64(1))
	c.Check(record3.KeygenBlameCountSinceChurn, Equals, int64(0))
	c.Check(record3.KeysignBlameCountSinceChurn, Equals, int64(0))

	// Empty address in the slice must be skipped without panicking on
	// record.NodeAddress.String() inside SetNodeBlameRecord, and other
	// valid addresses in the same call must still be reset.
	addr4 := GetRandomBech32Addr()
	for i := 0; i < 2; i++ {
		err = k.IncrementKeygenBlame(ctx, addr4)
		c.Assert(err, IsNil)
	}
	record4, _ := k.GetNodeBlameRecord(ctx, addr4)
	c.Check(record4.KeygenBlameCountSinceChurn, Equals, int64(2))

	err = k.ResetSinceChurnCountersForMembers(ctx, []cosmos.AccAddress{{}, addr4})
	c.Assert(err, IsNil)

	record4, _ = k.GetNodeBlameRecord(ctx, addr4)
	c.Check(record4.KeygenBlameCountTotal, Equals, int64(2))
	c.Check(record4.KeygenBlameCountSinceChurn, Equals, int64(0))

	// A corrupt record must surface as an error rather than be silently
	// swallowed, while other members in the same call are still reset.
	addr5 := GetRandomBech32Addr()
	c.Assert(k.IncrementKeygenBlame(ctx, addr5), IsNil)
	corrupt := GetRandomBech32Addr()
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(k.GetKey(prefixNodeBlameRecord, corrupt.String()), []byte{0xff, 0xff, 0xff})

	err = k.ResetSinceChurnCountersForMembers(ctx, []cosmos.AccAddress{corrupt, addr5})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "(?s).*"+corrupt.String()+".*")

	record5, _ := k.GetNodeBlameRecord(ctx, addr5)
	c.Check(record5.KeygenBlameCountSinceChurn, Equals, int64(0))
}

func (s *KeeperNodeBlameSuite) TestGetNodeBlameIterator(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create a few records
	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()

	err := k.IncrementKeygenBlame(ctx, addr1)
	c.Assert(err, IsNil)
	err = k.IncrementKeysignBlame(ctx, addr2)
	c.Assert(err, IsNil)

	// Test iterator
	iter := k.GetNodeBlameIterator(ctx)
	c.Check(iter, NotNil)
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Check(count, Equals, 2)
}
