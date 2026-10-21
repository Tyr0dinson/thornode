package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

type KeeperAddrPubKeySuite struct{}

var _ = Suite(&KeeperAddrPubKeySuite{})

func (s *KeeperAddrPubKeySuite) TestAddrPubKey(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test data
	pubKey := GetRandomPubKey()
	addr, err := pubKey.GetAddress(common.THORChain)
	c.Assert(err, IsNil)

	// Test GetAddrPubKey with non-existent address
	result, err := k.GetAddrPubKey(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(result, Equals, common.EmptyPubKey)

	// Test HasAddrPubKey with non-existent address
	exists := k.HasAddrPubKey(ctx, addr)
	c.Assert(exists, Equals, false)

	// Test SetAddrPubKey
	k.SetAddrPubKey(ctx, addr, pubKey)

	// Test GetAddrPubKey with existing address
	result, err = k.GetAddrPubKey(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(result, Equals, pubKey)

	// Test HasAddrPubKey with existing address
	exists = k.HasAddrPubKey(ctx, addr)
	c.Assert(exists, Equals, true)

	// Test SetAddrPubKey with empty pubkey (should delete)
	k.SetAddrPubKey(ctx, addr, common.EmptyPubKey)
	result, err = k.GetAddrPubKey(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(result, Equals, common.EmptyPubKey)
	exists = k.HasAddrPubKey(ctx, addr)
	c.Assert(exists, Equals, false)

	// Test DeleteAddrPubKey
	k.SetAddrPubKey(ctx, addr, pubKey)
	k.DeleteAddrPubKey(ctx, addr)
	result, err = k.GetAddrPubKey(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(result, Equals, common.EmptyPubKey)
	exists = k.HasAddrPubKey(ctx, addr)
	c.Assert(exists, Equals, false)
}

func (s *KeeperAddrPubKeySuite) TestAddrPubKeyIterator(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test data
	pubKey1 := GetRandomPubKey()
	pubKey2 := GetRandomPubKey()
	addr1, err := pubKey1.GetAddress(common.THORChain)
	c.Assert(err, IsNil)
	addr2, err := pubKey2.GetAddress(common.THORChain)
	c.Assert(err, IsNil)

	// Add mappings
	k.SetAddrPubKey(ctx, addr1, pubKey1)
	k.SetAddrPubKey(ctx, addr2, pubKey2)

	// Test iterator
	iterator := k.GetAddrPubKeyIterator(ctx)
	defer iterator.Close()

	count := 0
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 2)
}

func (s *KeeperAddrPubKeySuite) TestAddrPubKeyMultipleAddresses(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test data - same pubkey for multiple chains
	pubKey := GetRandomPubKey()
	addr1, err := pubKey.GetAddress(common.THORChain)
	c.Assert(err, IsNil)
	addr2, err := pubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Set same pubkey for different addresses
	k.SetAddrPubKey(ctx, addr1, pubKey)
	k.SetAddrPubKey(ctx, addr2, pubKey)

	// Verify both mappings exist
	result1, err := k.GetAddrPubKey(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(result1, Equals, pubKey)

	result2, err := k.GetAddrPubKey(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(result2, Equals, pubKey)

	// Verify deleting one doesn't affect the other
	k.DeleteAddrPubKey(ctx, addr1)
	result1, err = k.GetAddrPubKey(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(result1, Equals, common.EmptyPubKey)

	result2, err = k.GetAddrPubKey(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(result2, Equals, pubKey)
}
