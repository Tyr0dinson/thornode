package keeperv1

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type InvariantsAdvSwapCovSuite struct{}

var _ = Suite(&InvariantsAdvSwapCovSuite{})

func (s *InvariantsAdvSwapCovSuite) TestInvariantRoutes(c *C) {
	_, k := setupKeeperForTest(c)
	routes := k.InvariantRoutes()
	c.Assert(routes, HasLen, 10)
	names := make([]string, 0, len(routes))
	for _, r := range routes {
		names = append(names, r.Route)
		c.Check(r.Invariant, NotNil)
	}
	c.Check(strings.Join(names, ","), Equals,
		"asgard,bond,thorchain,affiliate_collector,pools,streaming_swaps,runepool,tcy_claim,tcy_stake,vault_deficit")
}

func (s *InvariantsAdvSwapCovSuite) TestAffiliateCollectorInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)
	inv := AffilliateCollectorInvariant(k)

	msg, broken := inv(ctx)
	c.Check(broken, Equals, false)
	c.Check(msg, HasLen, 0)

	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "aff", RuneAmount: cosmos.NewUint(100)}), IsNil)
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, DeepEquals, []string{"insolvent: 100rune"})

	FundModule(c, ctx, k, AffiliateCollectorName, 150)
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, DeepEquals, []string{"oversolvent: 50rune"})

	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "aff2", RuneAmount: cosmos.NewUint(50)}), IsNil)
	_, broken = inv(ctx)
	c.Check(broken, Equals, false)

	// a corrupt collector with a funded module is reported
	corruptKeyCov(ctx, k, k.GetKey(prefixAffiliateCollector, "bad"))
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, HasLen, 1)
}

func (s *InvariantsAdvSwapCovSuite) TestAffiliateCollectorInvariantCorruptUnfunded(c *C) {
	ctx, k := setupKeeperForTest(c)
	corruptKeyCov(ctx, k, k.GetKey(prefixAffiliateCollector, "bad"))
	msg, broken := AffilliateCollectorInvariant(k)(ctx)
	c.Check(broken, Equals, false)
	c.Check(msg, IsNil)
}

func (s *InvariantsAdvSwapCovSuite) TestPoolsInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)
	inv := PoolsInvariant(k)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.LPUnits = cosmos.NewUint(100)
	pool.PendingInboundRune = cosmos.NewUint(5)
	pool.PendingInboundAsset = cosmos.NewUint(1)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	// native pools are ignored
	native := NewPool()
	native.Asset = common.TCY
	native.LPUnits = cosmos.NewUint(999)
	c.Assert(k.SetPool(ctx, native), IsNil)

	msg, broken := inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, DeepEquals, []string{
		"BTC.BTC oversolvent: 100 units",
		"BTC.BTC oversolvent: 5 pending rune",
		"BTC.BTC oversolvent: 1 pending asset",
	})

	lp := LiquidityProvider{
		Asset:        common.BTCAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomBTCAddress(),
		Units:        cosmos.NewUint(150),
		PendingRune:  cosmos.NewUint(5),
		PendingAsset: cosmos.NewUint(1),
	}
	k.SetLiquidityProvider(ctx, lp)
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, DeepEquals, []string{"BTC.BTC insolvent: 50 units"})

	lp.Units = cosmos.NewUint(100)
	k.SetLiquidityProvider(ctx, lp)
	_, broken = inv(ctx)
	c.Check(broken, Equals, false)

	corruptKeyCov(ctx, k, k.GetKey(prefixPool, common.ETHAsset.String()))
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg[0], Matches, "fail to get pools.*")
}

func (s *InvariantsAdvSwapCovSuite) TestRUNEPoolInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)
	inv := RUNEPoolInvariant(k)

	_, broken := inv(ctx)
	c.Check(broken, Equals, false)

	rp := NewRUNEProvider(GetRandomBech32Addr())
	rp.Units = cosmos.NewUint(10)
	rp.DepositAmount = cosmos.NewUint(20)
	rp.WithdrawAmount = cosmos.NewUint(5)
	k.SetRUNEProvider(ctx, rp)
	msg, broken := inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, DeepEquals, []string{
		"pool units 0 != provider units 10",
		"rune deposited 0 != provider rune deposited 20",
		"rune withdrawn 0 != provider rune withdrawn 5",
	})

	pool := NewRUNEPool()
	pool.PoolUnits = cosmos.NewUint(10)
	pool.RuneDeposited = cosmos.NewUint(20)
	pool.RuneWithdrawn = cosmos.NewUint(5)
	k.SetRUNEPool(ctx, pool)
	_, broken = inv(ctx)
	c.Check(broken, Equals, false)

	corruptKeyCov(ctx, k, k.GetKey(prefixRUNEPool, ""))
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg, HasLen, 1)
}

func swapCov(swapType types.SwapType, amt uint64, target common.Asset) MsgSwap {
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(amt)))
	return MsgSwap{
		Tx:          tx,
		TargetAsset: target,
		TradeTarget: cosmos.NewUint(amt),
		SwapType:    swapType,
		Version:     types.SwapVersion_v2,
	}
}

func (s *InvariantsAdvSwapCovSuite) TestAsgardInvariantSwaps(c *C) {
	ctx, k := setupKeeperForTest(c)
	inv := AsgardInvariant(k)

	// a queued swap with two coins is reported
	bad := swapCov(types.SwapType_market, 10, common.BTCAsset)
	bad.Tx.Coins = append(bad.Tx.Coins, common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	k.setMsgSwap(ctx, k.GetKey(prefixAdvSwapQueueItem, formatSwapQueueItemKey(bad.Tx.ID, 0)), bad)
	msg, broken := inv(ctx)
	c.Check(broken, Equals, true)
	found := false
	for _, m := range msg {
		if strings.HasPrefix(m, "wrong number of coins for swap: 2") {
			found = true
		}
	}
	c.Check(found, Equals, true)
	k.del(ctx, k.GetKey(prefixAdvSwapQueueItem, formatSwapQueueItemKey(bad.Tx.ID, 0)))

	// advanced swap state: In is deducted from the swap coin and Out of a
	// native target added. Asgard holds the initial 100M RUNE; send 40 rune
	// worth of "in" to the pool by recording it in state.
	swp := swapCov(types.SwapType_market, 100, common.TCY)
	swp.State = &types.SwapState{
		Quantity: 2, Deposit: cosmos.NewUint(100),
		In: cosmos.NewUint(40), Out: cosmos.NewUint(7),
		Withdrawn: cosmos.ZeroUint(),
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swp), IsNil)
	msg, broken = inv(ctx)
	c.Check(broken, Equals, true)
	// asgard holds 100M rune but only 60 rune (100 - 40 in) is expected, plus 7 tcy out
	c.Check(strings.Join(msg, ";"), Matches, ".*oversolvent: 9999999999999940rune.*")
	c.Check(strings.Join(msg, ";"), Matches, ".*insolvent: 7tcy.*")
}

func (s *InvariantsAdvSwapCovSuite) TestAsgardInvariantPools(c *C) {
	ctx, k := setupKeeperForTest(c)

	// synth pools count the asset balance as synth coins
	synthPool := NewPool()
	synthPool.Asset = common.BTCAsset.GetSyntheticAsset()
	synthPool.BalanceAsset = cosmos.NewUint(5)
	c.Assert(k.SetPool(ctx, synthPool), IsNil)
	// TCY pool counts TCY asset balance
	tcyPool := NewPool()
	tcyPool.Asset = common.TCY
	tcyPool.BalanceRune = cosmos.NewUint(100 * common.One)
	tcyPool.BalanceAsset = cosmos.NewUint(3)
	c.Assert(k.SetPool(ctx, tcyPool), IsNil)

	msg, broken := AsgardInvariant(k)(ctx)
	c.Check(broken, Equals, true)
	all := strings.Join(msg, ";")
	c.Check(all, Matches, ".*insolvent: 3tcy.*")
	c.Check(all, Matches, ".*insolvent: 5btc/btc.*")

	corruptKeyCov(ctx, k, k.GetKey(prefixPool, common.ETHAsset.String()))
	msg, broken = AsgardInvariant(k)(ctx)
	c.Check(broken, Equals, true)
	c.Check(msg[0], Matches, "fail to get pools.*")
}

func (s *InvariantsAdvSwapCovSuite) TestStreamingSwapsInvariantCov(c *C) {
	ctx, k := setupKeeperForTest(c)
	inv := StreamingSwapsInvariant(k)

	// v2: deposit mismatch, count > quantity and in > deposit
	m := swapCov(types.SwapType_market, 100, common.BTCAsset)
	m.State = &types.SwapState{
		Quantity: 2, Count: 3, Deposit: cosmos.NewUint(90),
		In: cosmos.NewUint(95), Out: cosmos.ZeroUint(), Withdrawn: cosmos.ZeroUint(),
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, m), IsNil)
	// limit swaps check the success count
	l := swapCov(types.SwapType_limit, 100, common.BTCAsset)
	l.State = &types.SwapState{
		Quantity: 2, Count: 3, FailedSwaps: []uint64{1}, Deposit: cosmos.NewUint(100),
		In: cosmos.ZeroUint(), Out: cosmos.ZeroUint(), Withdrawn: cosmos.ZeroUint(),
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, l), IsNil)
	// v2 swap without coins (written directly)
	nc := swapCov(types.SwapType_market, 100, common.BTCAsset)
	nc.Tx.Coins = nil
	nc.State = &types.SwapState{Quantity: 2, Deposit: cosmos.NewUint(1), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint(), Withdrawn: cosmos.ZeroUint()}
	k.setMsgSwap(ctx, k.GetKey(prefixAdvSwapQueueItem, formatSwapQueueItemKey(nc.Tx.ID, 0)), nc)

	// v1: stream without a swap, and a stream inconsistent with its swap
	orphan := NewStreamingSwap(GetRandomTxHash(), 2, 1, cosmos.ZeroUint(), cosmos.NewUint(10))
	k.SetStreamingSwap(ctx, orphan)
	v1 := swapCov(types.SwapType_market, 50, common.BTCAsset)
	v1.Version = types.SwapVersion_v1
	v1.StreamInterval = 1
	v1.StreamQuantity = 2
	c.Assert(k.SetSwapQueueItem(ctx, v1, 0), IsNil)
	ss := NewStreamingSwap(v1.Tx.ID, 2, 1, cosmos.ZeroUint(), cosmos.NewUint(40))
	ss.Count = 3
	ss.In = cosmos.NewUint(45)
	k.SetStreamingSwap(ctx, ss)
	// v1 limit swaps are skipped
	v1l := swapCov(types.SwapType_limit, 50, common.BTCAsset)
	v1l.Version = types.SwapVersion_v1
	v1l.StreamInterval = 1
	c.Assert(k.SetSwapQueueItem(ctx, v1l, 0), IsNil)

	msg, broken := inv(ctx)
	c.Check(broken, Equals, true)
	all := strings.Join(msg, "\n")
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: swap.coin 100 != state.deposit 90.*", m.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: state.count 3 > state.quantity 2.*", m.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: state.in 95 > state.deposit 90.*", m.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: advanced swap missing coins.*", nc.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*swap not found for stream: %s.*", orphan.TxID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: swap.coin 50 != stream.deposit 40.*", v1.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: stream.count 3 > stream.quantity 2.*", v1.Tx.ID))
	c.Check(all, Matches, fmt.Sprintf("(?s).*%s: stream.in 45 > stream.deposit 40.*", v1.Tx.ID))
	// the limit swap's success count (3-1=2) does not exceed its quantity
	c.Check(strings.Contains(all, l.Tx.ID.String()), Equals, false)
}

func (s *InvariantsAdvSwapCovSuite) TestAdvSwapQueueItems(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(types.AdvSwapQueueModeDisabled))
	c.Check(k.AdvSwapQueueEnabled(ctx), Equals, false)
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	c.Check(k.AdvSwapQueueEnabled(ctx), Equals, true)

	msg := swapCov(types.SwapType_market, 100, common.BTCAsset)
	noCoins := msg
	noCoins.Tx.Coins = nil
	c.Check(k.SetAdvSwapQueueItem(ctx, noCoins), ErrorMatches, "incorrect number of coins.*")
	c.Check(k.SetAdvSwapQueueItemState(ctx, noCoins), ErrorMatches, "want exactly 1 coin.*")
	noID := msg
	noID.Tx.ID = ""
	c.Check(k.SetAdvSwapQueueItem(ctx, noID), ErrorMatches, "invalid tx hash")
	c.Check(k.SetAdvSwapQueueItemState(ctx, noID), ErrorMatches, "invalid tx hash")

	c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
	msg.State = &types.SwapState{Quantity: 1, Count: 1, Deposit: cosmos.NewUint(100), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint(), Withdrawn: cosmos.ZeroUint()}
	c.Assert(k.SetAdvSwapQueueItemState(ctx, msg), IsNil)
	got, err := k.GetAdvSwapQueueItem(ctx, msg.Tx.ID, 0)
	c.Assert(err, IsNil)
	c.Check(got.State.Count, Equals, uint64(1))

	c.Assert(k.RemoveAdvSwapQueueItemMsg(ctx, msg), IsNil)
	c.Check(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, 0), Equals, false)
	ok, err := k.HasAdvSwapQueueIndex(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, false)

	// market and unknown swap types have no index iterator
	c.Check(k.GetAdvSwapQueueIndexIterator(ctx, types.SwapType_market, common.RuneAsset(), common.BTCAsset), IsNil)
	c.Check(k.GetAdvSwapQueueIndexIterator(ctx, types.SwapType(99), common.RuneAsset(), common.BTCAsset), IsNil)
	c.Check(k.getAdvSwapQueueIndexKey(ctx, MsgSwap{SwapType: types.SwapType(99)}), DeepEquals, []byte{})

	// limit index iterator walks populated buckets
	lim := swapCov(types.SwapType_limit, 100, common.BTCAsset)
	c.Assert(k.SetAdvSwapQueueItem(ctx, lim), IsNil)
	iter := k.GetAdvSwapQueueLimitIndexIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	// removing a limit entry by key
	limKey := k.getAdvSwapQueueIndexKey(ctx, lim)
	c.Assert(k.RemoveLimitAdvSwapQueueIndexEntry(ctx, limKey, lim.Tx.ID, 0), IsNil)
	c.Check(k.has(ctx, limKey), Equals, false)

	// RemoveAdvSwapQueueIndex rejects messages without coins
	c.Check(k.RemoveAdvSwapQueueIndex(ctx, noCoins), ErrorMatches, "cannot remove swap queue index.*")
}

func (s *InvariantsAdvSwapCovSuite) TestAdvSwapQueueIndexEntries(c *C) {
	ctx, k := setupKeeperForTest(c)
	h1, h2 := GetRandomTxHash(), GetRandomTxHash()
	marketKey := k.GetKey(prefixAdvSwapQueueMarketIndex, "")

	// malformed entries are skipped while parsing the index
	k.setStrings(ctx, marketKey, []string{
		formatSwapQueueItemKey(h1, 0),
		"nohyphen",
		"-5",
		"abc-",
		"!!!-1",
		h2.String() + "-x",
		formatSwapQueueItemKey(h2, 3),
	})
	items, err := k.GetAdvSwapQueueIndex(ctx, MsgSwap{SwapType: types.SwapType_market})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 2)
	c.Check(items[0].TxID, Equals, h1)
	c.Check(items[1].Index, Equals, 3)

	// remove a single market entry, then a missing one, then the last
	c.Assert(k.RemoveMarketAdvSwapQueueIndexEntry(ctx, h1, 0), IsNil)
	c.Assert(k.RemoveMarketAdvSwapQueueIndexEntry(ctx, GetRandomTxHash(), 0), IsNil)
	items, err = k.GetAdvSwapQueueIndex(ctx, MsgSwap{SwapType: types.SwapType_market})
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)
	k.setStrings(ctx, marketKey, []string{formatSwapQueueItemKey(h2, 3)})
	c.Assert(k.RemoveMarketAdvSwapQueueIndexEntry(ctx, h2, 3), IsNil)
	c.Check(k.has(ctx, marketKey), Equals, false)

	// corrupt index propagates errors
	corruptKeyCov(ctx, k, marketKey)
	_, err = k.GetAdvSwapQueueIndex(ctx, MsgSwap{SwapType: types.SwapType_market})
	c.Check(err, NotNil)
	c.Check(k.RemoveMarketAdvSwapQueueIndexEntry(ctx, h1, 0), NotNil)
	m := swapCov(types.SwapType_market, 1, common.BTCAsset)
	c.Check(k.RemoveAdvSwapQueueIndex(ctx, m), NotNil)
	c.Check(k.SetAdvSwapQueueIndex(ctx, m), NotNil)
	_, err = k.HasAdvSwapQueueIndex(ctx, m)
	c.Check(err, NotNil)
}

func (s *InvariantsAdvSwapCovSuite) TestLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)
	h1, h2 := GetRandomTxHash(), GetRandomTxHash()

	c.Check(k.AddToLimitSwapTTL(ctx, 0, h1), ErrorMatches, "invalid block height.*")
	_, err := k.GetLimitSwapTTL(ctx, -1)
	c.Check(err, ErrorMatches, "invalid block height.*")

	c.Assert(k.AddToLimitSwapTTL(ctx, 50, h1), IsNil)
	c.Assert(k.AddToLimitSwapTTL(ctx, 50, h1), IsNil) // duplicate ignored
	c.Assert(k.AddToLimitSwapTTL(ctx, 50, h2), IsNil)
	hashes, err := k.GetLimitSwapTTL(ctx, 50)
	c.Assert(err, IsNil)
	c.Check(hashes, DeepEquals, []common.TxID{h1, h2})

	// invalid stored hashes are skipped
	k.setStrings(ctx, k.GetKey(prefixAdvSwapQueueTTL, "51"), []string{"", h1.String()})
	hashes, err = k.GetLimitSwapTTL(ctx, 51)
	c.Assert(err, IsNil)
	c.Check(hashes, DeepEquals, []common.TxID{h1})

	k.RemoveLimitSwapTTL(ctx, 0) // no-op
	k.RemoveLimitSwapTTL(ctx, 50)
	hashes, err = k.GetLimitSwapTTL(ctx, 50)
	c.Assert(err, IsNil)
	c.Check(hashes, HasLen, 0)

	corruptKeyCov(ctx, k, k.GetKey(prefixAdvSwapQueueTTL, "52"))
	c.Check(k.AddToLimitSwapTTL(ctx, 52, h1), ErrorMatches, "fail to get existing TTL list.*")
}

func (s *InvariantsAdvSwapCovSuite) TestRatioHelpers(c *C) {
	c.Check(rewriteRatio(5, "12"), Equals, "00012")
	c.Check(rewriteRatio(5, "1234567"), Equals, "12345")
	c.Check(rewriteRatio(3, "123"), Equals, "123")
	c.Check(getRatio(cosmos.NewUint(5), cosmos.ZeroUint()), Equals, "0")
	c.Check(getRatio(cosmos.NewUint(1), cosmos.NewUint(2)), Equals, "50000000")
	c.Check(removeString([]string{"a"}, 3), DeepEquals, []string{"a"})
	c.Check(removeString([]string{"a"}, -1), DeepEquals, []string{"a"})
	c.Check(removeString([]string{"a", "b", "c"}, 0), DeepEquals, []string{"c", "b"})
}
