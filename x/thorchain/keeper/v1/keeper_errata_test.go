package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type KeeperErrataTxSuite struct{}

var _ = Suite(&KeeperErrataTxSuite{})

func (s *KeeperErrataTxSuite) TestErrataTxVoter(c *C) {
	ctx, k := setupKeeperForTest(c)

	txID := GetRandomTxHash()
	voter := NewErrataTxVoter(txID, common.ETHChain)

	k.SetErrataTxVoter(ctx, voter)
	voter, err := k.GetErrataTxVoter(ctx, voter.TxID, voter.Chain)
	c.Assert(err, IsNil)
	c.Check(voter.TxID.Equals(txID), Equals, true)
	c.Check(voter.Chain.Equals(common.ETHChain), Equals, true)
	c.Check(k.GetErrataTxVoterIterator(ctx), NotNil)

	errtaVoter, err := k.GetErrataTxVoter(ctx, GetRandomTxHash(), common.ETHChain)
	c.Check(err, IsNil)
	c.Check(errtaVoter.Empty(), Equals, false)
}

func (s *KeeperErrataTxSuite) TestErrataTxVotersAreScopedByExternalHeight(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	first := NewErrataTxVoterAtHeight(txID, common.XMRChain, 100)
	first.BlockHeight = 10
	second := NewErrataTxVoterAtHeight(txID, common.XMRChain, 120)
	second.BlockHeight = 20
	k.SetErrataTxVoter(ctx, first)
	k.SetErrataTxVoter(ctx, second)

	gotFirst, err := k.GetErrataTxVoterAtHeight(ctx, txID, common.XMRChain, 100)
	c.Assert(err, IsNil)
	c.Check(gotFirst.BlockHeight, Equals, int64(10))
	c.Check(gotFirst.ExternalHeight, Equals, int64(100))

	gotSecond, err := k.GetErrataTxVoterAtHeight(ctx, txID, common.XMRChain, 120)
	c.Assert(err, IsNil)
	c.Check(gotSecond.BlockHeight, Equals, int64(20))
	c.Check(gotSecond.ExternalHeight, Equals, int64(120))

	legacy, err := k.GetErrataTxVoter(ctx, txID, common.XMRChain)
	c.Assert(err, IsNil)
	c.Check(legacy.BlockHeight, Equals, int64(0))
}
