package keeperv1

import (
	. "gopkg.in/check.v1"
)

type KeeperKeygenSuite struct{}

var _ = Suite(&KeeperKeygenSuite{})

func (s *KeeperKeygenSuite) TestKeeperKeygen(c *C) {
	var err error
	ctx, k := setupKeeperForTest(c)

	keygenBlock := NewKeygenBlock(1)
	keygenMembers := []string{GetRandomPubKey().String(), GetRandomPubKey().String(), GetRandomPubKey().String()}
	keygen, err := NewKeygen(ctx.BlockHeight(), keygenMembers, AsgardKeygen)
	c.Assert(err, IsNil)
	c.Assert(keygen.IsEmpty(), Equals, false)
	keygenBlock.Keygens = append(keygenBlock.Keygens, keygen)
	k.SetKeygenBlock(ctx, keygenBlock)

	keygenBlock, err = k.GetKeygenBlock(ctx, 1)
	c.Assert(err, IsNil)
	c.Assert(keygenBlock, NotNil)
	c.Assert(keygenBlock.Height, Equals, int64(1))

	keygenBlock, err = k.GetKeygenBlock(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(keygenBlock, NotNil)

	iter := k.GetKeygenBlockIterator(ctx)
	defer iter.Close()
}

func (s *KeeperKeygenSuite) TestChurnKeygenExpected(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initially absent.
	memberships, ok, err := k.GetChurnKeygenExpected(ctx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
	c.Assert(memberships, IsNil)

	// Set and round-trip.
	want := [][]string{
		{GetRandomPubKey().String(), GetRandomPubKey().String()},
		{GetRandomPubKey().String(), GetRandomPubKey().String(), GetRandomPubKey().String()},
	}
	k.SetChurnKeygenExpected(ctx, want)

	got, ok, err := k.GetChurnKeygenExpected(ctx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(len(got), Equals, len(want))
	for i := range want {
		c.Assert(got[i], DeepEquals, want[i])
	}

	// Delete clears the record.
	k.DeleteChurnKeygenExpected(ctx)
	_, ok, err = k.GetChurnKeygenExpected(ctx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)

	// Setting an empty slice also deletes.
	k.SetChurnKeygenExpected(ctx, want)
	k.SetChurnKeygenExpected(ctx, nil)
	_, ok, err = k.GetChurnKeygenExpected(ctx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
}
