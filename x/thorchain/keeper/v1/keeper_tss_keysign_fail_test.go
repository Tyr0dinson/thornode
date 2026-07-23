package keeperv1

import (
	. "gopkg.in/check.v1"
)

type KeeperTssKeysignFailureSuite struct{}

var _ = Suite(&KeeperTssKeysignFailureSuite{})

func (KeeperTssKeysignFailureSuite) TestTssKeysignFailVoter(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	voter, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Check(err, IsNil)
	c.Check(voter.Empty(), Equals, true)
	c.Check(voter.Completed, Equals, false)

	// Current production voters remain at height zero while collecting votes.
	live := NewTssKeysignFailVoter(id, 0)
	live.Signers = []string{GetRandomBech32Addr().String()}
	k.SetTssKeysignFailVoter(ctx, live)
	voter1, err1 := k.GetTssKeysignFailVoter(ctx, id)
	c.Check(err1, IsNil)
	c.Check(voter1.Completed, Equals, false)
	c.Check(k.GetTssKeysignFailVoterIterator(ctx), NotNil)
}

func (KeeperTssKeysignFailureSuite) TestLegacyCompletedVoterNormalization(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()

	// This is the exact terminal representation written before Completed was
	// added: the record exists, but consensus cleared its transient fields.
	k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{ID: id})

	var stored TssKeysignFailVoter
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &stored)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(stored.Completed, Equals, false)

	normalized, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Assert(normalized.Completed, Equals, true)

	// The handler uses the normal setter on its early-return path, making the
	// compatibility marker explicit and durable after the first replay.
	k.SetTssKeysignFailVoter(ctx, normalized)
	stored = TssKeysignFailVoter{}
	found, err = k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &stored)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(stored.Completed, Equals, true)
}

func (KeeperTssKeysignFailureSuite) TestPositiveHeightLegacyCompletedVoterNormalization(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()

	// Early handler versions set Height before consensus. A record completed
	// after the signer-clearing behavior was introduced can therefore retain a
	// positive Height while having the same empty terminal consensus state.
	k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{
		ID:     id,
		Height: 1024,
	})

	normalized, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Assert(normalized.Completed, Equals, true)
	c.Assert(normalized.Height, Equals, int64(1024))

	// The earliest terminal representation retained the consensus signers.
	k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{
		ID:      id,
		Height:  2048,
		Signers: []string{GetRandomBech32Addr().String(), GetRandomBech32Addr().String()},
	})
	normalized, err = k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Assert(normalized.Completed, Equals, true)
	c.Assert(normalized.Height, Equals, int64(2048))
	c.Assert(normalized.Signers, HasLen, 2)
}
