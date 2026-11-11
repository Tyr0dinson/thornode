package keeperv1

import (
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type KeeperTxOutWindowSuite struct{}

var _ = Suite(&KeeperTxOutWindowSuite{})

func windowTxOut(height int64, memo string) *TxOut {
	txOut := NewTxOut(height)
	txOut.TxArray = append(txOut.TxArray, types.TxOutItem{
		Chain:     common.BTCChain,
		ToAddress: GetRandomBTCAddress(),
		Coin:      common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		Memo:      memo,
	})
	return txOut
}

// writeRawTxOut stores a txout without going through SetTxOut, so the window
// cannot see it: a read that still misses it proves the read was served from
// the window.
func writeRawTxOut(ctx cosmos.Context, k KVStore, txOut *TxOut) {
	k.kvStore(ctx).Set(k.GetKey(prefixTxOut, strconv.FormatInt(txOut.Height, 10)), k.cdc.MustMarshal(txOut))
}

func (s *KeeperTxOutWindowSuite) TestServesSnapshot(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, h := range []int64{100, 150, 199, 200} {
		c.Assert(k.SetTxOut(ctx, windowTxOut(h, "OUT:"+strconv.FormatInt(h, 10))), IsNil)
	}
	// keys of other lengths sort inside the range: "txout//15" between 149
	// and 150, "txout//1500" between 150 and 151
	c.Assert(k.SetTxOut(ctx, windowTxOut(15, "OUT:15")), IsNil)
	c.Assert(k.SetTxOut(ctx, windowTxOut(1500, "OUT:1500")), IsNil)

	windowed := keeper.WithTxOutWindow(ctx, 100, 199)
	for h := int64(90); h <= 210; h++ {
		want, err := k.GetTxOut(ctx, h)
		c.Assert(err, IsNil)
		got, err := k.GetTxOut(windowed, h)
		c.Assert(err, IsNil)
		c.Check(got, DeepEquals, want, Commentf("height %d", h))
	}
	bz, present, ok := keeper.TxOutWindowFromContext(windowed).Get(150)
	c.Assert(ok, Equals, true)
	c.Assert(present, Equals, true)
	c.Assert(bz, NotNil)
	for _, h := range []int64{15, 1500} {
		_, _, ok = keeper.TxOutWindowFromContext(windowed).Get(h)
		c.Check(ok, Equals, false, Commentf("height %d", h))
	}

	// a raw write the window cannot see: covered heights keep the snapshot,
	// others read the store
	writeRawTxOut(ctx, k, windowTxOut(120, "OUT:120"))
	writeRawTxOut(ctx, k, windowTxOut(150, "OUT:changed"))
	writeRawTxOut(ctx, k, windowTxOut(210, "OUT:210"))
	got, err := k.GetTxOut(windowed, 120)
	c.Assert(err, IsNil)
	c.Check(got.TxArray, HasLen, 0)
	got, err = k.GetTxOut(windowed, 150)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].Memo, Equals, "OUT:150")
	got, err = k.GetTxOut(windowed, 210)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].Memo, Equals, "OUT:210")
}

func (s *KeeperTxOutWindowSuite) TestWritesOutsideRangeKeepIt(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(k.SetTxOut(ctx, windowTxOut(150, "OUT:150")), IsNil)
	windowed := keeper.WithTxOutWindow(ctx, 100, 199)
	_, err := k.GetTxOut(windowed, 150)
	c.Assert(err, IsNil)

	// the current block's and scheduled outbounds
	c.Assert(k.AppendTxOut(windowed, 200, windowTxOut(200, "OUT:200").TxArray[0]), IsNil)
	c.Assert(k.SetTxOut(windowed, windowTxOut(260, "OUT:260")), IsNil)
	c.Assert(k.ClearTxOut(windowed, 200), IsNil)
	c.Assert(k.SetTxOut(windowed, windowTxOut(99, "OUT:99")), IsNil)

	writeRawTxOut(ctx, k, windowTxOut(150, "OUT:changed"))
	got, err := k.GetTxOut(windowed, 150)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].Memo, Equals, "OUT:150")
}

func (s *KeeperTxOutWindowSuite) TestCoveredWritesDisableIt(c *C) {
	writes := map[string]func(ctx cosmos.Context, k KVStore){
		"set": func(ctx cosmos.Context, k KVStore) {
			c.Assert(k.SetTxOut(ctx, windowTxOut(120, "OUT:120")), IsNil)
		},
		"append": func(ctx cosmos.Context, k KVStore) {
			c.Assert(k.AppendTxOut(ctx, 150, windowTxOut(150, "OUT:more").TxArray[0]), IsNil)
		},
		"clear": func(ctx cosmos.Context, k KVStore) {
			c.Assert(k.ClearTxOut(ctx, 150), IsNil)
		},
		"delete key": func(ctx cosmos.Context, k KVStore) {
			k.DeleteKey(ctx, k.GetKey(prefixTxOut, "150"))
		},
		"not a height": func(ctx cosmos.Context, k KVStore) {
			k.DeleteKey(ctx, k.GetKey(prefixTxOut, "x"))
		},
	}
	for name, write := range writes {
		for _, loadFirst := range []bool{true, false} {
			comment := Commentf("%s, loaded first: %v", name, loadFirst)
			ctx, k := setupKeeperForTest(c)
			c.Assert(k.SetTxOut(ctx, windowTxOut(150, "OUT:150")), IsNil)
			windowed := keeper.WithTxOutWindow(ctx, 100, 199)
			if loadFirst {
				_, err := k.GetTxOut(windowed, 150)
				c.Assert(err, IsNil)
			}

			// the write goes through a discarded cache context: the window
			// must not have served (or loaded) it either way
			cached, _ := windowed.CacheContext()
			write(cached, k)
			_, _, ok := keeper.TxOutWindowFromContext(windowed).Get(150)
			c.Check(ok, Equals, false, comment)

			// reads now come from the store
			writeRawTxOut(ctx, k, windowTxOut(150, "OUT:changed"))
			got, err := k.GetTxOut(windowed, 150)
			c.Assert(err, IsNil)
			c.Check(got.TxArray[0].Memo, Equals, "OUT:changed", comment)
		}
	}
}

func (s *KeeperTxOutWindowSuite) TestHeightsOfDifferentLengths(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(k.SetTxOut(ctx, windowTxOut(98, "OUT:98")), IsNil)
	c.Assert(k.SetTxOut(ctx, windowTxOut(103, "OUT:103")), IsNil)

	// "txout//98" sorts after "txout//103": one range cannot hold 95..105
	windowed := keeper.WithTxOutWindow(ctx, 95, 105)
	for _, h := range []int64{98, 103} {
		got, err := k.GetTxOut(windowed, h)
		c.Assert(err, IsNil)
		c.Check(got.TxArray[0].Memo, Equals, "OUT:"+strconv.FormatInt(h, 10))
	}
	_, _, ok := keeper.TxOutWindowFromContext(windowed).Get(98)
	c.Check(ok, Equals, false)
}

func (s *KeeperTxOutWindowSuite) TestRange(c *C) {
	ctx, _ := setupKeeperForTest(c)
	c.Check(keeper.TxOutWindowFromContext(keeper.WithTxOutWindow(ctx, 10, 9)), IsNil)
	window := keeper.TxOutWindowFromContext(keeper.WithTxOutWindow(ctx, -290, 9))
	lo, hi, ok := window.Range(5)
	c.Check(ok, Equals, true)
	c.Check(lo, Equals, int64(1))
	c.Check(hi, Equals, int64(9))
	_, _, ok = window.Range(0)
	c.Check(ok, Equals, false)
	_, _, ok = window.Range(10)
	c.Check(ok, Equals, false)
	var none *keeper.TxOutWindow
	_, _, ok = none.Range(5)
	c.Check(ok, Equals, false)
}

// getSortedVaultSecurity's window and GetPendingOutbounds' walk read covered
// heights from the window too.
func (s *KeeperTxOutWindowSuite) TestWindowAndWalkServeSnapshot(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(k.SetTxOut(ctx, windowTxOut(150, "OUT:150")), IsNil)
	windowed := keeper.WithTxOutWindow(ctx, 100, 199)
	_, err := k.GetTxOut(windowed, 150)
	c.Assert(err, IsNil)

	writeRawTxOut(ctx, k, windowTxOut(120, "OUT:120"))
	writeRawTxOut(ctx, k, windowTxOut(150, "OUT:changed"))
	writeRawTxOut(ctx, k, windowTxOut(200, "OUT:200"))

	window := k.newTxOutWindow(windowed, 100, 200)
	_, _, present := window.get(120)
	c.Check(present, Equals, false)
	_, bz, present := window.get(150)
	c.Assert(present, Equals, true)
	record := NewTxOut(150)
	c.Assert(k.cdc.Unmarshal(bz, record), IsNil)
	c.Check(record.TxArray[0].Memo, Equals, "OUT:150")
	_, _, present = window.get(200)
	c.Check(present, Equals, true)

	walk := k.newTxOutWalk(windowed)
	got, err := k.getTxOutWalk(windowed, walk, 120)
	c.Assert(err, IsNil)
	c.Check(got, IsNil)
	got, err = k.getTxOutWalk(windowed, walk, 150)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].Memo, Equals, "OUT:150")
	got, err = k.getTxOutWalk(windowed, walk, 200)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].Memo, Equals, "OUT:200")
}

// EndBlock attaches the past heights and the scheduled heights as two windows
// over each other: each serves its own range, and a write disables only the
// window covering it.
func (s *KeeperTxOutWindowSuite) TestStackedWindows(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, h := range []int64{150, 200, 250} {
		c.Assert(k.SetTxOut(ctx, windowTxOut(h, "OUT:"+strconv.FormatInt(h, 10))), IsNil)
	}
	windowed := keeper.WithTxOutWindow(keeper.WithTxOutWindow(ctx, 100, 199), 201, 300)
	past := keeper.TxOutWindowFromContext(windowed).Covering(150)
	future := keeper.TxOutWindowFromContext(windowed).Covering(250)
	c.Assert(past, NotNil)
	c.Assert(future, NotNil)
	c.Assert(past == future, Equals, false)
	c.Check(keeper.TxOutWindowFromContext(windowed).Covering(200), IsNil)

	for h := int64(90); h <= 310; h++ {
		want, err := k.GetTxOut(ctx, h)
		c.Assert(err, IsNil)
		got, err := k.GetTxOut(windowed, h)
		c.Assert(err, IsNil)
		c.Check(got, DeepEquals, want, Commentf("height %d", h))
	}

	// raw writes the windows cannot see: both serve their snapshots
	writeRawTxOut(ctx, k, windowTxOut(150, "OUT:changed"))
	writeRawTxOut(ctx, k, windowTxOut(250, "OUT:changed"))
	readMemo := func(h int64) string {
		got, err := k.GetTxOut(windowed, h)
		c.Assert(err, IsNil)
		c.Assert(got.TxArray, HasLen, 1)
		return got.TxArray[0].Memo
	}
	c.Check(readMemo(150), Equals, "OUT:150")
	c.Check(readMemo(250), Equals, "OUT:250")

	// writing the current height keeps both
	c.Assert(k.AppendTxOut(windowed, 200, types.TxOutItem{Chain: common.BTCChain, Memo: "OUT:new"}), IsNil)
	c.Check(readMemo(150), Equals, "OUT:150")
	c.Check(readMemo(250), Equals, "OUT:250")

	// a scheduled outbound, even in a discarded cache context, disables the
	// scheduled window only
	cacheCtx, _ := windowed.CacheContext()
	c.Assert(k.AppendTxOut(cacheCtx, 260, types.TxOutItem{Chain: common.BTCChain, Memo: "OUT:delayed"}), IsNil)
	c.Check(readMemo(150), Equals, "OUT:150")
	c.Check(readMemo(250), Equals, "OUT:changed")
	_, _, ok := future.Get(250)
	c.Check(ok, Equals, false)

	// and a past write the past one
	c.Assert(k.SetTxOut(windowed, windowTxOut(120, "OUT:120")), IsNil)
	c.Check(readMemo(150), Equals, "OUT:changed")
	c.Check(keeper.TxOutWindowFromContext(windowed).Covering(150), IsNil)
}
