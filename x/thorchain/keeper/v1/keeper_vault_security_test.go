package keeperv1

import (
	storetypes "cosmossdk.io/store/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type KeeperVaultSecuritySuite struct{}

var _ = Suite(&KeeperVaultSecuritySuite{})

func (s *KeeperVaultSecuritySuite) TestGetPoolMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	for i, asset := range []common.Asset{common.ETHAsset, common.BTCAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(i+1) * 100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(i+1) * 7 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	// stored bytes that do not decode
	store.Set(k.GetKey(prefixPool, common.DOGEAsset.String()), []byte{0x0a})

	memo := make(map[string]poolMemo)
	check := func(asset common.Asset) {
		ctx1 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		want, err1 := k.GetPool(ctx1, asset)
		ctx2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		got, err2 := k.getPoolMemo(ctx2, asset, memo)
		c.Check(ctx2.GasMeter().GasConsumed(), Equals, ctx1.GasMeter().GasConsumed(), Commentf("%s", asset))
		c.Check(err2 == nil, Equals, err1 == nil, Commentf("%s", asset))
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), Commentf("%s", asset))
		}
		c.Check(got, DeepEquals, want, Commentf("%s", asset))
	}
	for i := 0; i < 2; i++ { // decoded, then from memo
		check(common.ETHAsset)
		check(common.BTCAsset)
		check(common.LTCAsset) // missing
		check(common.DOGEAsset)
	}
	c.Check(memo, HasLen, 2)

	// a changed pool is decoded again
	pool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(3)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	check(common.ETHAsset)
	got, err := k.getPoolMemo(ctx, common.ETHAsset, memo)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(3))
}
