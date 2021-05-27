package keeperv1

import (
	"encoding/hex"
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
)

type KeeperPoolSuite struct{}

var _ = Suite(&KeeperPoolSuite{})

func (s *KeeperPoolSuite) TestPool(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Check(k.SetPool(ctx, Pool{}), NotNil) // empty asset should error

	pool := NewPool()
	pool.Asset = common.BNBAsset

	k.SetPool(ctx, pool)
	pool, err := k.GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Check(pool.Asset.Equals(common.BNBAsset), Equals, true)
	c.Check(k.PoolExist(ctx, common.BNBAsset), Equals, true)
	c.Check(k.PoolExist(ctx, common.BTCAsset), Equals, false)

	pools, err := k.GetPools(ctx)
	c.Assert(err, IsNil)
	c.Assert(pools, HasLen, 1)

	p, err := k.GetPool(ctx, common.BTCAsset)
	c.Check(err, IsNil)
	c.Check(p.Valid(), NotNil)
	newPool := NewPool()
	newPool.Asset = common.BTCAsset
	newPool.PoolUnits = cosmos.NewUint(1024)
	newPool.BalanceRune = cosmos.NewUint(1000)
	newPool.BalanceAsset = cosmos.NewUint(1000)
	newPool.Status = types.PoolStatus_Available
	b, err := k.cdc.MarshalBinaryBare(&newPool)
	if err != nil {
		panic(err)
	}
	fmt.Println("hex:", hex.EncodeToString(b))
}
