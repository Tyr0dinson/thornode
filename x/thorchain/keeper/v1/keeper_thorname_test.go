package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	. "gopkg.in/check.v1"
)

type KeeperTHORNameSuite struct{}

var _ = Suite(&KeeperTHORNameSuite{})

func (s *KeeperTHORNameSuite) TestTHORName(c *C) {
	ctx, k := setupKeeperForTest(c)
	var err error
	ref := "helloworld"

	ok := k.THORNameExists(ctx, ref, common.THORChain)
	c.Assert(ok, Equals, false)

	thorAddr := GetRandomTHORAddress()
	name := NewTHORName(ref, common.THORChain, thorAddr, 50)
	k.SetTHORName(ctx, name)
	bnbAddr := GetRandomBNBAddress()
	name = NewTHORName(ref, common.BNBChain, bnbAddr, 50)
	k.SetTHORName(ctx, name)
	bnbAddr2 := GetRandomBNBAddress()
	name = NewTHORName("other", common.BNBChain, bnbAddr2, 50)
	k.SetTHORName(ctx, name)

	ok = k.THORNameExists(ctx, ref, common.THORChain)
	c.Assert(ok, Equals, true)
	ok = k.THORNameExists(ctx, ref, common.BNBChain)
	c.Assert(ok, Equals, true)
	ok = k.THORNameExists(ctx, ref, common.ETHChain)
	c.Assert(ok, Equals, false)

	name, err = k.GetTHORName(ctx, ref, common.THORChain)
	c.Assert(err, IsNil)
	c.Assert(name.Address.Equals(thorAddr), Equals, true)
	name, err = k.GetTHORName(ctx, ref, common.BNBChain)
	c.Assert(err, IsNil)
	c.Assert(name.Address.Equals(bnbAddr), Equals, true)

	var count int
	iterator := k.GetTHORNameIterator(ctx, ref)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count += 1
		fmt.Println(iterator.Value())
	}
	c.Assert(count, Equals, 2)
}
