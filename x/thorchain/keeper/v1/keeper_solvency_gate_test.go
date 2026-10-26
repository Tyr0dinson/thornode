package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type SolvencyGateKeeperSuite struct{}

var _ = Suite(&SolvencyGateKeeperSuite{})

func (s *SolvencyGateKeeperSuite) TestGatedInboundsOrderedByHeightPerChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	pk1, pk2 := GetRandomPubKey(), GetRandomPubKey()
	// 87-character base58 SOL tx ids are case-sensitive.
	solTxID := common.TxID("5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQUW")
	inbounds := []SolvencyGatedInbound{
		{Chain: common.GAIAChain, PubKey: pk1, Height: 100, TxID: GetRandomTxHash()},
		{Chain: common.GAIAChain, PubKey: pk2, Height: 2, TxID: GetRandomTxHash()},
		{Chain: common.GAIAChain, PubKey: pk1, Height: 10, TxID: GetRandomTxHash()},
		{Chain: common.SOLChain, PubKey: pk1, Height: 1, TxID: solTxID},
	}
	for i := range inbounds {
		inbounds[i].ParkedAt = 7
		c.Assert(k.SetSolvencyGatedInbound(ctx, inbounds[i]), IsNil)
	}
	c.Assert(k.SetSolvencyGatedInbound(ctx, SolvencyGatedInbound{Chain: common.GAIAChain, Height: -1}), NotNil)

	got, err := k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 3)
	for i, want := range []SolvencyGatedInbound{inbounds[1], inbounds[2], inbounds[0]} {
		c.Assert(got[i], DeepEquals, want)
	}

	got, err = k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, 2)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 2)

	got, err = k.GetSolvencyGatedInbounds(ctx, common.SOLChain, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxID, Equals, solTxID)

	k.DeleteSolvencyGatedInbound(ctx, inbounds[2])
	got, err = k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 2)
	c.Assert(got[0].TxID, Equals, inbounds[1].TxID)
	c.Assert(got[1].TxID, Equals, inbounds[0].TxID)
}

func (s *SolvencyGateKeeperSuite) TestSolvencyConfirmationNeverMovesBackwards(c *C) {
	ctx, k := setupKeeperForTest(c)
	pk := GetRandomPubKey()
	height, at, err := k.GetSolvencyConfirmation(ctx, common.GAIAChain, pk)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(0))
	c.Assert(at, Equals, int64(0))

	ctx = ctx.WithBlockHeight(10)
	c.Assert(k.SetSolvencyConfirmation(ctx, common.GAIAChain, pk, 500), IsNil)
	ctx = ctx.WithBlockHeight(11)
	c.Assert(k.SetSolvencyConfirmation(ctx, common.GAIAChain, pk, 400), IsNil)
	height, at, err = k.GetSolvencyConfirmation(ctx, common.GAIAChain, pk)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(500))
	c.Assert(at, Equals, int64(10))

	// A later consensus at the same height moves the consensus height forward.
	ctx = ctx.WithBlockHeight(12)
	c.Assert(k.SetSolvencyConfirmation(ctx, common.GAIAChain, pk, 500), IsNil)
	height, at, err = k.GetSolvencyConfirmation(ctx, common.GAIAChain, pk)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(500))
	c.Assert(at, Equals, int64(12))

	// Confirmations are per vault and chain.
	height, _, err = k.GetSolvencyConfirmation(ctx, common.BTCChain, pk)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(0))
	height, _, err = k.GetSolvencyConfirmation(ctx, common.GAIAChain, GetRandomPubKey())
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(0))
}
