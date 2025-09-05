package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

type KeeperReferenceMemoSuite struct{}

var _ = Suite(&KeeperReferenceMemoSuite{})

func (s *KeeperReferenceMemoSuite) TestReferenceMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	var err error
	ref := "123456"
	chain := common.BTCChain
	signer := GetRandomBech32Addr()
	hash := GetRandomTxHash()

	ok := k.ReferenceMemoExists(ctx, chain, ref)
	c.Assert(ok, Equals, false)

	refMemo := NewReferenceMemo(chain, "my memo", ref, 35)
	refMemo.RegistrationHash = hash
	refMemo.RegisteredBy = signer
	k.SetReferenceMemo(ctx, refMemo)

	ok = k.ReferenceMemoExists(ctx, chain, ref)
	c.Assert(ok, Equals, true)
	ok = k.ReferenceMemoExists(ctx, chain, "123456789")
	c.Assert(ok, Equals, false)
	ok = k.ReferenceMemoExists(ctx, common.ETHChain, ref)
	c.Assert(ok, Equals, false)

	refMemo, err = k.GetReferenceMemo(ctx, chain, ref)
	c.Assert(err, IsNil)
	c.Check(refMemo.Chain.Equals(common.BTCChain), Equals, true)
	c.Check(refMemo.Memo, Equals, "my memo")
	c.Check(refMemo.Reference, Equals, ref)
	c.Check(refMemo.Height, Equals, int64(35))
	c.Check(refMemo.RegisteredBy.String(), Equals, signer.String())
	c.Check(refMemo.RegistrationHash.String(), Equals, hash.String())

	refMemo, err = k.GetReferenceMemoByTxnHash(ctx, hash)
	c.Assert(err, IsNil)
	c.Check(refMemo.Chain.Equals(common.BTCChain), Equals, true)
	c.Check(refMemo.Memo, Equals, "my memo")
	c.Check(refMemo.Reference, Equals, ref)
	c.Check(refMemo.Height, Equals, int64(35))
	c.Check(refMemo.RegisteredBy.String(), Equals, signer.String())
	c.Check(refMemo.RegistrationHash.String(), Equals, hash.String())
}
