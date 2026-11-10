package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type KeeperAnchorsCacheSuite struct{}

var _ = Suite(&KeeperAnchorsCacheSuite{})

var (
	anchorUSDC = common.Asset{Chain: common.ETHChain, Symbol: "USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", Ticker: "USDC"}
	anchorUSDT = common.Asset{Chain: common.ETHChain, Symbol: "USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", Ticker: "USDT"}
)

func setAnchorPool(c *C, ctx cosmos.Context, k KVStore, asset common.Asset, anchor bool) {
	pool := NewPool()
	pool.Asset = asset
	pool.Status = types.PoolStatus_Available
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	if anchor {
		k.SetMimir(ctx, TorAnchorMimirKey(asset), 1)
	}
}

// writeRawPool stores a pool without going through SetPool, so the cache cannot
// see it: a read that still misses it proves the read was served from cache.
func writeRawPool(ctx cosmos.Context, k KVStore, asset common.Asset) {
	pool := NewPool()
	pool.Asset = asset
	k.setPool(ctx, k.GetKey(prefixPool, asset.String()), pool)
}

func (s *KeeperAnchorsCacheSuite) TestServesSnapshotAndCopies(c *C) {
	ctx, k := setupKeeperForTest(c)
	setAnchorPool(c, ctx, k, anchorUSDC, true)
	setAnchorPool(c, ctx, k, common.BTCAsset, false)

	cached := keeper.WithAnchorsCache(ctx)
	first := k.GetAnchors(cached, common.TOR)
	c.Assert(first, DeepEquals, k.GetAnchors(ctx, common.TOR))
	c.Assert(first, DeepEquals, []common.Asset{anchorUSDC})

	// a caller modifying a list, filled or served from cache, does not change
	// the snapshot
	first[0] = common.BTCAsset
	hit := k.GetAnchors(cached, common.TOR)
	c.Assert(hit, DeepEquals, []common.Asset{anchorUSDC})
	hit[0] = common.BTCAsset

	// a raw write the cache cannot see: an uncached read finds the new
	// anchor, the cached read does not, so it came from the snapshot
	writeRawPool(ctx, k, anchorUSDT)
	k.setInt64(ctx, k.GetKey(prefixMimir, TorAnchorMimirKey(anchorUSDT)), 1)
	keeper.MimirCacheFromContext(ctx).Disable()
	c.Assert(k.GetAnchors(ctx, common.TOR), HasLen, 2)
	c.Assert(k.GetAnchors(cached, common.TOR), DeepEquals, []common.Asset{anchorUSDC})

	// non-TOR assets never use the cache
	c.Assert(k.GetAnchors(cached, common.BTCAsset), DeepEquals, []common.Asset{common.BTCAsset})
}

func (s *KeeperAnchorsCacheSuite) TestExistingPoolWriteKeepsCache(c *C) {
	ctx, k := setupKeeperForTest(c)
	setAnchorPool(c, ctx, k, anchorUSDC, true)
	cached := keeper.WithAnchorsCache(ctx)
	c.Assert(k.GetAnchors(cached, common.TOR), HasLen, 1)

	pool, err := k.GetPool(cached, anchorUSDC)
	c.Assert(err, IsNil)
	pool.BalanceRune = pool.BalanceRune.Add(cosmos.NewUint(common.One))
	c.Assert(k.SetPool(cached, pool), IsNil)

	writeRawPool(ctx, k, anchorUSDT)
	k.setInt64(ctx, k.GetKey(prefixMimir, TorAnchorMimirKey(anchorUSDT)), 1)
	c.Assert(k.GetAnchors(cached, common.TOR), DeepEquals, []common.Asset{anchorUSDC})
}

func (s *KeeperAnchorsCacheSuite) TestWritesDisableCache(c *C) {
	for name, write := range map[string]func(c *C, ctx cosmos.Context, k KVStore){
		"new pool": func(c *C, ctx cosmos.Context, k KVStore) {
			setAnchorPool(c, ctx, k, anchorUSDT, false)
			k.setInt64(ctx, k.GetKey(prefixMimir, TorAnchorMimirKey(anchorUSDT)), 1)
		},
		"anchor mimir": func(c *C, ctx cosmos.Context, k KVStore) {
			writeRawPool(ctx, k, anchorUSDT)
			k.SetMimir(ctx, TorAnchorMimirKey(anchorUSDT), 1)
		},
		"delete mimir": func(c *C, ctx cosmos.Context, k KVStore) {
			writeRawPool(ctx, k, anchorUSDT)
			k.setInt64(ctx, k.GetKey(prefixMimir, TorAnchorMimirKey(anchorUSDT)), 1)
			c.Assert(k.DeleteMimir(ctx, "unrelated"), IsNil)
		},
		"remove pool": func(c *C, ctx cosmos.Context, k KVStore) {
			writeRawPool(ctx, k, anchorUSDT)
			k.setInt64(ctx, k.GetKey(prefixMimir, TorAnchorMimirKey(anchorUSDT)), 1)
			k.RemovePool(ctx, common.BTCAsset)
		},
	} {
		ctx, k := setupKeeperForTest(c)
		setAnchorPool(c, ctx, k, anchorUSDC, true)
		cached := keeper.WithAnchorsCache(ctx)
		c.Assert(k.GetAnchors(cached, common.TOR), HasLen, 1, Commentf(name))

		write(c, cached, k)
		keeper.MimirCacheFromContext(ctx).Disable()
		c.Assert(k.GetAnchors(cached, common.TOR), DeepEquals, k.GetAnchors(ctx, common.TOR), Commentf(name))
		c.Assert(k.GetAnchors(cached, common.TOR), HasLen, 2, Commentf(name))
	}
}

// A pool written before the first fill may sit in a cache context that is
// later discarded; filling there would snapshot it, so the cache never fills.
func (s *KeeperAnchorsCacheSuite) TestPoolWriteBeforeFillDisables(c *C) {
	ctx, k := setupKeeperForTest(c)
	setAnchorPool(c, ctx, k, anchorUSDC, true)
	k.SetMimir(ctx, TorAnchorMimirKey(anchorUSDT), 1)
	cached := keeper.WithAnchorsCache(ctx)

	scratch, _ := cached.CacheContext() // never written back
	setAnchorPool(c, scratch, k, anchorUSDT, false)
	c.Assert(k.GetAnchors(scratch, common.TOR), HasLen, 2)

	c.Assert(k.GetAnchors(cached, common.TOR), DeepEquals, []common.Asset{anchorUSDC})
}

// A new pool in a discarded cache context disables the cache, so the parent
// keeps reading its own store.
func (s *KeeperAnchorsCacheSuite) TestDiscardedNewPoolAfterFill(c *C) {
	ctx, k := setupKeeperForTest(c)
	setAnchorPool(c, ctx, k, anchorUSDC, true)
	k.SetMimir(ctx, TorAnchorMimirKey(anchorUSDT), 1)
	cached := keeper.WithAnchorsCache(ctx)
	c.Assert(k.GetAnchors(cached, common.TOR), HasLen, 1)

	scratch, _ := cached.CacheContext() // never written back
	setAnchorPool(c, scratch, k, anchorUSDT, false)
	c.Assert(k.GetAnchors(scratch, common.TOR), HasLen, 2)

	c.Assert(k.GetAnchors(cached, common.TOR), DeepEquals, []common.Asset{anchorUSDC})
}
