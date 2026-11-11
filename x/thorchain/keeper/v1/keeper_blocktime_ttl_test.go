package keeperv1

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	storetypes "cosmossdk.io/store/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type KeeperBlockTimeTTLSuite struct{}

var _ = Suite(&KeeperBlockTimeTTLSuite{})

const ttlConvertHeight = int64(1000)

func ttlTx(i int) common.TxID { return common.TxID(fmt.Sprintf("%064X", i)) }

// convertLimitSwapTTLsOneByOne is the conversion as it was, adding each moved
// hash with AddToLimitSwapTTL: the oracle for the store bytes.
func convertLimitSwapTTLsOneByOne(ctx cosmos.Context, k KVStore, convert func(int64) int64) {
	type entry struct {
		height int64
		hashes []common.TxID
	}
	var entries []entry
	prefix := string(k.GetKey(prefixAdvSwapQueueTTL, ""))
	iter := k.getIterator(ctx, prefixAdvSwapQueueTTL)
	for ; iter.Valid(); iter.Next() {
		h, err := strconv.ParseInt(strings.TrimPrefix(string(iter.Key()), prefix), 10, 64)
		if err != nil || convert(h) == h {
			continue
		}
		var record ProtoStrings
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			continue
		}
		e := entry{height: h}
		for _, s := range record.Value {
			if hash, err := common.NewTxID(s); err == nil {
				e.hashes = append(e.hashes, hash)
			}
		}
		entries = append(entries, e)
	}
	_ = iter.Close()
	for _, e := range entries {
		k.RemoveLimitSwapTTL(ctx, e.height)
	}
	for _, e := range entries {
		for _, hash := range e.hashes {
			_ = k.AddToLimitSwapTTL(ctx, convert(e.height), hash)
		}
	}
}

func dumpLimitSwapTTLs(ctx cosmos.Context, k KVStore) [][2][]byte {
	var out [][2][]byte
	iter := k.getIterator(ctx, prefixAdvSwapQueueTTL)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		out = append(out, [2][]byte{append([]byte(nil), iter.Key()...), append([]byte(nil), iter.Value()...)})
	}
	return out
}

// Converting the limit swap TTL buckets reads and writes each destination
// once, and leaves the same store bytes as adding the moved hashes one at a
// time: the destination's own hashes first, then each source's in key order
// ("10000" before "1001"), without duplicates, including a lower-case copy.
func (KeeperBlockTimeTTLSuite) TestConvertLimitSwapTTLsMatchesOneByOne(c *C) {
	lower := common.TxID(strings.ToLower(ttlTx(3).String()))
	for _, tc := range []struct {
		name          string
		prevMs, newMs int64
		seed          func(ctx cosmos.Context, k KVStore)
	}{
		{name: "slowdown with duplicates and an existing destination", prevMs: 500, newMs: 1000, seed: func(ctx cosmos.Context, k KVStore) {
			c.Assert(k.SetLimitSwapTTL(ctx, 1000, []common.TxID{ttlTx(1), ttlTx(2)}), IsNil) // the current height stays, a destination of 1001
			c.Assert(k.SetLimitSwapTTL(ctx, 1001, []common.TxID{ttlTx(3), ttlTx(1)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 1002, []common.TxID{ttlTx(4), ttlTx(4)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 1003, []common.TxID{ttlTx(5), lower}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 999, []common.TxID{ttlTx(6)}), IsNil) // past, untouched
			c.Assert(k.SetLimitSwapTTL(ctx, 10000, []common.TxID{ttlTx(7), ttlTx(8)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 10001, []common.TxID{ttlTx(8), ttlTx(9)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 5500, []common.TxID{ttlTx(10), ttlTx(11)}), IsNil) // moved, and a destination
			c.Assert(k.SetLimitSwapTTL(ctx, 1004, nil), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 2001, []common.TxID{ttlTx(13), ttlTx(13)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 1500, []common.TxID{ttlTx(14)}), IsNil)
			c.Assert(k.SetLimitSwapTTL(ctx, 2002, []common.TxID{ttlTx(14), ttlTx(15)}), IsNil)
		}},
		{name: "4x slowdown", prevMs: 250, newMs: 1000, seed: func(ctx cosmos.Context, k KVStore) {
			for h := int64(1000); h < 1100; h++ {
				c.Assert(k.SetLimitSwapTTL(ctx, h, []common.TxID{ttlTx(int(h % 37)), ttlTx(int(h))}), IsNil)
			}
		}},
		{name: "4x speed-up", prevMs: 1000, newMs: 250, seed: func(ctx cosmos.Context, k KVStore) {
			for h := int64(1000); h < 1100; h++ {
				c.Assert(k.SetLimitSwapTTL(ctx, h, []common.TxID{ttlTx(int(h % 37)), ttlTx(int(h))}), IsNil)
			}
		}},
	} {
		comment := Commentf(tc.name)
		var dumps [2][][2][]byte
		for i, grouped := range []bool{false, true} {
			ctx, k := setupKeeperForTest(c)
			ctx = ctx.WithBlockHeight(ttlConvertHeight)
			tc.seed(ctx, k)
			convert := func(h int64) int64 { return constants.ConvertHeight(ttlConvertHeight, h, tc.prevMs, tc.newMs) }
			if grouped {
				k.convertLimitSwapTTLs(ctx, convert)
			} else {
				convertLimitSwapTTLsOneByOne(ctx, k, convert)
			}
			dumps[i] = dumpLimitSwapTTLs(ctx, k)
		}
		c.Assert(dumps[1], HasLen, len(dumps[0]), comment)
		for i := range dumps[0] {
			c.Check(bytes.Equal(dumps[1][i][0], dumps[0][i][0]), Equals, true, comment)
			c.Check(bytes.Equal(dumps[1][i][1], dumps[0][i][1]), Equals, true, Commentf("%s: %s", tc.name, dumps[0][i][0]))
		}
	}

	// the example in the order spelled out above
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(ttlConvertHeight)
	c.Assert(k.SetLimitSwapTTL(ctx, 1000, []common.TxID{ttlTx(1), ttlTx(2)}), IsNil)
	c.Assert(k.SetLimitSwapTTL(ctx, 1001, []common.TxID{ttlTx(3), ttlTx(1)}), IsNil)
	c.Assert(k.SetLimitSwapTTL(ctx, 10000, []common.TxID{ttlTx(7), ttlTx(8)}), IsNil)
	c.Assert(k.SetLimitSwapTTL(ctx, 10001, []common.TxID{ttlTx(8), ttlTx(9)}), IsNil)
	k.convertLimitSwapTTLs(ctx, func(h int64) int64 { return constants.ConvertHeight(ttlConvertHeight, h, 500, 1000) })
	got, err := k.GetLimitSwapTTL(ctx, 1000)
	c.Assert(err, IsNil)
	c.Check(got, DeepEquals, []common.TxID{ttlTx(1), ttlTx(2), ttlTx(3)})
	got, err = k.GetLimitSwapTTL(ctx, 5500)
	c.Assert(err, IsNil)
	c.Check(got, DeepEquals, []common.TxID{ttlTx(7), ttlTx(8), ttlTx(9)})
}

// Orders can share one expiry height in any number. Moving 4096 of them in
// one bucket must cost a read and a write of the bucket, not one of each per
// hash: adding them one at a time burned about 1.8e10 gas (and 1.9 GB) in one
// BeginBlock.
func (KeeperBlockTimeTTLSuite) TestConvertLimitSwapTTLsLargeBucketIsLinear(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(ttlConvertHeight)
	hashes := make([]common.TxID, 4096)
	for i := range hashes {
		hashes[i] = ttlTx(i)
	}
	c.Assert(k.SetLimitSwapTTL(ctx, 5000, hashes), IsNil)

	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	convert := func(h int64) int64 { return constants.ConvertHeight(ttlConvertHeight, h, 500, 1000) }
	k.convertLimitSwapTTLs(ctx, convert)
	c.Check(ctx.GasMeter().GasConsumed() < 100_000_000, Equals, true,
		Commentf("gas %d", ctx.GasMeter().GasConsumed()))

	got, err := k.GetLimitSwapTTL(ctx, convert(5000))
	c.Assert(err, IsNil)
	c.Check(got, DeepEquals, hashes)
	old, err := k.GetLimitSwapTTL(ctx, 5000)
	c.Assert(err, IsNil)
	c.Check(old, HasLen, 0)
}
