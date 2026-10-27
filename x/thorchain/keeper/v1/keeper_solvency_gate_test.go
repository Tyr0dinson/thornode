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

	got, err := k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, pk1, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 2)
	for i, want := range []SolvencyGatedInbound{inbounds[2], inbounds[0]} {
		c.Assert(got[i], DeepEquals, want)
	}

	got, err = k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, pk1, 1)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1)

	got, err = k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, pk2, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0], DeepEquals, inbounds[1])

	got, err = k.GetSolvencyGatedInbounds(ctx, common.SOLChain, pk1, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxID, Equals, solTxID)

	c.Assert(k.HasSolvencyGatedInbound(ctx, inbounds[2]), Equals, true)
	c.Assert(k.HasSolvencyGatedInboundsForVault(ctx, common.GAIAChain, pk1), Equals, true)
	c.Assert(k.HasSolvencyGatedInboundsForVault(ctx, common.BTCChain, pk1), Equals, false)
	k.DeleteSolvencyGatedInbound(ctx, inbounds[2])
	c.Assert(k.HasSolvencyGatedInbound(ctx, inbounds[2]), Equals, false)
	got, err = k.GetSolvencyGatedInbounds(ctx, common.GAIAChain, pk1, 10)
	c.Assert(err, IsNil)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxID, Equals, inbounds[0].TxID)
	k.DeleteSolvencyGatedInbound(ctx, inbounds[0])
	c.Assert(k.HasSolvencyGatedInboundsForVault(ctx, common.GAIAChain, pk1), Equals, false)
}

// Heads are each vault's lowest-height inbound, one per vault however many it has.
func (s *SolvencyGateKeeperSuite) TestGatedInboundHeadsOnePerVault(c *C) {
	ctx, k := setupKeeperForTest(c)
	pks := []common.PubKey{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	lowest := map[common.PubKey]int64{}
	for i, pk := range pks {
		for h := int64(5); h > 0; h-- {
			height := h*10 + int64(i)
			c.Assert(k.SetSolvencyGatedInbound(ctx, SolvencyGatedInbound{Chain: common.GAIAChain, PubKey: pk, Height: height, TxID: GetRandomTxHash()}), IsNil)
			lowest[pk] = height
		}
	}
	c.Assert(k.SetSolvencyGatedInbound(ctx, SolvencyGatedInbound{Chain: common.BTCChain, PubKey: pks[0], Height: 1, TxID: GetRandomTxHash()}), IsNil)

	heads, err := k.GetSolvencyGatedInboundHeads(ctx, common.GAIAChain, 10)
	c.Assert(err, IsNil)
	c.Assert(heads, HasLen, 3)
	seen := map[common.PubKey]bool{}
	for i, in := range heads {
		c.Assert(in.Chain, Equals, common.GAIAChain)
		c.Assert(in.Height, Equals, lowest[in.PubKey])
		c.Assert(seen[in.PubKey], Equals, false)
		seen[in.PubKey] = true
		if i > 0 {
			c.Assert(heads[i-1].PubKey.String() < in.PubKey.String(), Equals, true)
		}
	}

	heads, err = k.GetSolvencyGatedInboundHeads(ctx, common.GAIAChain, 2)
	c.Assert(err, IsNil)
	c.Assert(heads, HasLen, 2)

	heads, err = k.GetSolvencyGatedInboundHeads(ctx, common.ETHChain, 10)
	c.Assert(err, IsNil)
	c.Assert(heads, HasLen, 0)
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
