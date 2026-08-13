package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperTxOutSuite struct{}

var _ = Suite(&KeeperTxOutSuite{})

func (KeeperTxOutSuite) TestKeeperTxOut(c *C) {
	ctx, k := setupKeeperForTest(c)
	txOut := NewTxOut(1)
	spent := cosmos.NewUint(100)
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "hello",
		CloutSpent:  &spent,
	}
	txOut.TxArray = append(txOut.TxArray, txOutItem)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(92419747020392)
	pool.BalanceAsset = cosmos.NewUint(1402011488988)
	err := k.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	txOut1, err := k.GetTxOut(ctx, 1)
	c.Assert(err, IsNil)
	c.Assert(txOut1, NotNil)
	c.Assert(txOut1.Height, Equals, int64(1))

	txOut2, err := k.GetTxOut(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(txOut2, NotNil)

	c.Check(k.AppendTxOut(ctx, 100, txOutItem), IsNil)

	iter := k.GetTxOutIterator(ctx)
	c.Check(iter, NotNil)
	defer iter.Close()
	c.Check(k.ClearTxOut(ctx, 100), IsNil)

	txOut3 := NewTxOut(1024)
	c.Check(k.SetTxOut(ctx, txOut3), IsNil)

	value, clout, err := k.GetTxOutValue(ctx, 1)
	c.Assert(err, IsNil)
	c.Check(value.Uint64(), Equals, uint64(659193934902), Commentf("%d", value.Uint64()))
	c.Check(clout.String(), Equals, "100")
}

func (KeeperTxOutSuite) TestKeeperTxOutCache(c *C) {
	ctx, k := setupKeeperForTest(c)
	spent := cosmos.NewUint(100)
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "hello",
		CloutSpent:  &spent,
	}
	stored := NewTxOut(5)
	stored.TxArray = append(stored.TxArray, item)
	c.Assert(k.SetTxOut(ctx, stored), IsNil)

	// read returns the TxOut of height and the gas the read consumed
	read := func(ctx cosmos.Context, height int64) (*TxOut, uint64) {
		before := ctx.GasMeter().GasConsumed()
		txOut, err := k.GetTxOut(ctx, height)
		c.Assert(err, IsNil)
		return txOut, ctx.GasMeter().GasConsumed() - before
	}

	plain := map[int64]*TxOut{}
	for _, h := range []int64{5, 6} {
		plain[h], _ = read(ctx, h)
	}
	c.Assert(plain[5].TxArray, HasLen, 1)
	c.Assert(plain[6].TxArray, HasLen, 0)

	cached := keeper.WithTxOutCache(ctx)
	cache := keeper.TxOutCacheFromContext(cached)
	c.Assert(cache, NotNil)
	c.Check(keeper.TxOutCacheFromContext(keeper.WithTxOutCache(cached)) == cache, Equals, true) // reused, not replaced

	// present and absent heights read the store once, then come from the cache
	for _, h := range []int64{5, 6} {
		first, gas := read(cached, h)
		c.Check(gas > 0, Equals, true)
		c.Check(first, DeepEquals, plain[h])
		second, gas := read(cached, h)
		c.Check(gas, Equals, uint64(0))
		c.Check(second, DeepEquals, plain[h])
	}

	// a caller mutating the returned TxOut does not change the cached one
	mutated, _ := read(cached, 5)
	mutated.TxArray[0].Memo = "changed"
	mutated.TxArray[0].Coin.Amount = cosmos.NewUint(1)
	mutated.TxArray = append(mutated.TxArray, item)
	again, _ := read(cached, 5)
	c.Check(again, DeepEquals, plain[5])

	// a fork shares the snapshot; its writes disable only the fork
	forked := keeper.ForkTxOutCache(cached)
	_, gas := read(forked, 6)
	c.Check(gas, Equals, uint64(0))
	_, gas = read(forked, 7) // filled through the fork
	c.Check(gas > 0, Equals, true)
	_, gas = read(cached, 7)
	c.Check(gas, Equals, uint64(0))
	branch, _ := forked.CacheContext()
	c.Assert(k.AppendTxOut(branch, 6, item), IsNil)
	got, gas := read(forked, 6)
	c.Check(gas > 0, Equals, true)
	c.Check(got.TxArray, HasLen, 0) // branch never written back
	got, gas = read(branch, 6)
	c.Check(gas > 0, Equals, true)
	c.Check(got.TxArray, HasLen, 1)
	_, gas = read(cached, 6)
	c.Check(gas, Equals, uint64(0))

	// any txout write through the cached context disables it for good,
	// including writes through a branch of it
	for i, write := range []func(ctx cosmos.Context){
		func(ctx cosmos.Context) { c.Assert(k.SetTxOut(ctx, stored), IsNil) },
		func(ctx cosmos.Context) { c.Assert(k.AppendTxOut(ctx, 6, item), IsNil) },
		func(ctx cosmos.Context) { c.Assert(k.ClearTxOut(ctx, 5), IsNil) },
	} {
		comment := Commentf("write %d", i)
		cached = keeper.WithTxOutCache(ctx)
		_, _ = read(cached, 5)
		_, gas = read(cached, 5)
		c.Check(gas, Equals, uint64(0), comment)
		branch, _ := cached.CacheContext()
		write(branch)
		_, gas = read(cached, 5)
		c.Check(gas > 0, Equals, true, comment)
		_, gas = read(cached, 5) // no refill
		c.Check(gas > 0, Equals, true, comment)
	}
}

// A cache miss at height >= keeper.TxOutCacheChunk fills the whole aligned
// chunk with one range iteration; every height must read as the store does,
// including heights of other key lengths that sort inside the chunk's bounds
// and bytes that do not decode.
func (KeeperTxOutSuite) TestKeeperTxOutCacheChunkFill(c *C) {
	ctx, k := setupKeeperForTest(c)
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		Memo:        "hello",
	}
	// 10500 and 150000 sort between "1000" and "1999"; 999 and 2000 border
	// the chunk [1000, 1999]
	heights := []int64{999, 1000, 1001, 1500, 1999, 2000, 10500, 150000, 28114071}
	for _, h := range heights {
		txOut := NewTxOut(h)
		txOut.TxArray = append(txOut.TxArray, item)
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	k.kvStore(ctx).Set(k.GetKey(prefixTxOut, "1700"), []byte{0x0a}) // does not decode

	var probe []int64
	for _, base := range []int64{1000, 10000, 150000, 28114000} {
		for _, d := range []int64{-2, -1, 0, 1, 2, 71, 499, 500, 700, 998, 999, 1000} {
			probe = append(probe, base+d)
		}
	}
	probe = append(probe, 1700, 10500)
	cached := keeper.WithTxOutCache(ctx)
	for _, h := range probe {
		_, wantBz, wantPresent := k.getTxOutBytes(ctx, h)
		_, gotBz, gotPresent := k.getTxOutBytes(cached, h)
		c.Check(gotPresent, Equals, wantPresent, Commentf("height %d", h))
		c.Check(gotBz, DeepEquals, wantBz, Commentf("height %d", h))
		want, wantErr := k.GetTxOut(ctx, h)
		got, gotErr := k.GetTxOut(cached, h)
		c.Check(got, DeepEquals, want, Commentf("height %d", h))
		c.Check(gotErr == nil, Equals, wantErr == nil, Commentf("height %d", h))
	}

	// once filled, a chunk's heights touch no store
	before := cached.GasMeter().GasConsumed()
	for h := int64(1000); h <= 1999; h++ {
		_, _, _ = k.getTxOutBytes(cached, h)
	}
	c.Check(cached.GasMeter().GasConsumed(), Equals, before)

	// a disabled cache, or a fork after its txout write, reads point by point
	disabled := keeper.WithTxOutCache(ctx)
	keeper.TxOutCacheFromContext(disabled).Disable()
	_, bz, present := k.getTxOutBytes(disabled, 1500)
	c.Check(present, Equals, true)
	c.Check(len(bz) > 0, Equals, true)
	c.Check(keeper.TxOutCacheFromContext(disabled).ClaimChunk(1), Equals, false)
}

func (KeeperTxOutSuite) TestTxOutValuationUsesFinalizedPoolBalances(c *C) {
	ctx, k := setupKeeperForTest(c)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	item := TxOutItem{
		Chain: common.ETHChain,
		Coin:  common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
	}
	baseline, _ := k.GetTOIsValue(ctx, item)
	c.Assert(baseline.IsZero(), Equals, false)

	pool.BalanceRune = cosmos.NewUint(800 * common.One)
	pool.BalanceAsset = cosmos.NewUint(125 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	liveSpeculative, _ := k.GetTOIsValue(ctx, item)
	c.Assert(liveSpeculative.Equal(baseline), Equals, false)

	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	effect := &record.PendingEffects.Pools[0]
	effect.Asset = common.ETHAsset
	effect.BalanceRuneIncrease = cosmos.ZeroUint()
	effect.BalanceRuneDecrease = cosmos.NewUint(200 * common.One)
	effect.BalanceAssetIncrease = cosmos.NewUint(25 * common.One)
	effect.BalanceAssetDecrease = cosmos.ZeroUint()
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)

	finalizedValue, _ := k.GetTOIsValue(ctx, item)
	c.Check(finalizedValue.Equal(baseline), Equals, true)
	storedPool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(storedPool.BalanceRune.Equal(pool.BalanceRune), Equals, true,
		Commentf("valuation must not rewrite the speculative live pool"))
	c.Check(storedPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	afterFinality, _ := k.GetTOIsValue(ctx, item)
	c.Check(afterFinality.Equal(liveSpeculative), Equals, true)
}

func (KeeperTxOutSuite) TestTxOutValuationFallsBackToRawPool(c *C) {
	ctx, k := setupKeeperForTest(c)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(5 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	effect := &record.PendingEffects.Pools[0]
	effect.Asset = common.ETHAsset
	effect.BalanceRuneIncrease = cosmos.ZeroUint()
	effect.BalanceRuneDecrease = cosmos.ZeroUint()
	effect.BalanceAssetIncrease = cosmos.NewUint(10 * common.One)
	effect.BalanceAssetDecrease = cosmos.ZeroUint()
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)

	item := TxOutItem{
		Chain: common.ETHChain,
		Coin:  common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
	}
	value, _ := k.GetTOIsValue(ctx, item)
	c.Check(value.Equal(pool.AssetValueInRune(item.Coin.Amount)), Equals, true)
	c.Check(value.IsZero(), Equals, false)
}
