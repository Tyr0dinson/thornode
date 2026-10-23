//go:build mocknet

package keeperv1

import (
	storetypes "cosmossdk.io/store/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func (s *KeeperConfigSuite) TestGetBlockTimeMs(c *C) {
	ctx, k := setupKeeperForTest(c)
	ref := constants.ThorchainBlockTime.Milliseconds()
	c.Assert(ref, Equals, int64(1000))

	// unset and zero use the default block time, and store nothing
	c.Check(k.GetBlockTimeMs(ctx), Equals, ref)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	k.UpdateBlockTime(ctx)
	c.Check(k.GetBlockTimeMs(ctx), Equals, ref)
	c.Check(k.has(ctx, k.GetKey(prefixBlockTime, "ms")), Equals, false)

	// a new target takes effect when latched at the start of the next block
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	c.Check(k.GetBlockTimeMs(ctx), Equals, ref)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	k.UpdateBlockTime(ctx)
	c.Check(k.GetBlockTimeMs(ctx), Equals, int64(500))
	height, prevMs := k.GetBlockTimeChange(ctx)
	c.Check(height, Equals, ctx.BlockHeight())
	c.Check(prevMs, Equals, ref)

	// an unchanged target leaves the change record alone
	k.UpdateBlockTime(ctx.WithBlockHeight(ctx.BlockHeight() + 1))
	height, _ = k.GetBlockTimeChange(ctx)
	c.Check(height, Equals, ctx.BlockHeight())

	// stored values outside the hard limits are clamped
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 1)
	c.Check(k.blockTimeTargetMs(ctx), Equals, int64(constants.BlockTimeMsMin))
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 60_000)
	c.Check(k.blockTimeTargetMs(ctx), Equals, int64(constants.BlockTimeMsMax))
}

// Away from the default, the block time moves at most half or double per
// ChurnInterval of wall-clock time; back toward the default it moves at once.
func (s *KeeperConfigSuite) TestBlockTimeRateLimit(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(constants.ThorchainBlockTime.Milliseconds(), Equals, int64(1000))
	k.SetMimir(ctx, constants.ChurnInterval.String(), 50)
	latch := func(h int64) int64 {
		hctx := ctx.WithBlockHeight(h)
		k.UpdateBlockTime(hctx)
		return k.GetBlockTimeMs(hctx)
	}

	// the first change halves at most
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 200)
	c.Check(latch(100), Equals, int64(500))
	// then waits 50 reference blocks: 100 blocks of 500ms
	c.Check(latch(101), Equals, int64(500))
	c.Check(latch(199), Equals, int64(500))
	c.Check(latch(200), Equals, int64(250))
	c.Check(latch(201), Equals, int64(250))
	// 50 reference blocks at 250ms are 200 blocks
	c.Check(latch(400), Equals, int64(200))

	// back to the default at once
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	c.Check(latch(401), Equals, int64(1000))

	// up from the default doubles at most, after the wait since the revert
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 4000)
	c.Check(latch(402), Equals, int64(1000))
	c.Check(latch(451), Equals, int64(2000))
	c.Check(latch(475), Equals, int64(2000))
	c.Check(latch(476), Equals, int64(4000))
	// down toward the default at once, but not past it
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 1500)
	c.Check(latch(477), Equals, int64(1500))
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	c.Check(latch(478), Equals, int64(1500))
	c.Check(latch(511), Equals, int64(750))
	c.Check(k.GetBlockTimeHistory(ctx), DeepEquals, constants.BlockTimeHistory{
		{Height: 100, Ms: 500},
		{Height: 200, Ms: 250},
		{Height: 400, Ms: 200},
		{Height: 401, Ms: 1000},
		{Height: 451, Ms: 2000},
		{Height: 476, Ms: 4000},
		{Height: 477, Ms: 1500},
		{Height: 511, Ms: 750},
	})
}

func (s *KeeperConfigSuite) TestGetConfigInt64Scaled(c *C) {
	ctx, k := setupKeeperForTest(c)
	consts := k.GetConstants()
	churn := consts.GetInt64Value(constants.ChurnInterval)
	jail := consts.GetInt64Value(constants.JailTimeKeysign)
	rate := consts.GetInt64Value(constants.TxOutDelayRate)
	validators := consts.GetInt64Value(constants.DesiredValidatorSet)

	// identity while BLOCKTIMEMS is unset
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, churn)
	c.Check(k.GetConfigInt64(ctx, constants.TxOutDelayRate), Equals, rate)
	c.Check(k.RefBlocks(ctx, 600), Equals, int64(600))

	// half the default block time: durations double, rates halve, counts stay
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx)
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, churn*2)
	c.Check(k.GetConfigInt64(ctx, constants.JailTimeKeysign), Equals, jail*2)
	c.Check(k.GetConfigInt64(ctx, constants.TxOutDelayRate), Equals, rate/2)
	c.Check(k.GetConfigInt64(ctx, constants.DesiredValidatorSet), Equals, validators)
	c.Check(k.RefBlocks(ctx, 600), Equals, int64(1200))
	c.Check(k.ScaleConfigInt64(ctx, constants.ChurnInterval, 7), Equals, int64(14))
	c.Check(k.ScaleConfigInt64(ctx, constants.ArtificialRagnarokBlockHeight, 7), Equals, int64(7))

	// governance values are scaled the same way, and 0 (off) stays off
	k.SetMimir(ctx, constants.ChurnInterval.String(), 100)
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, int64(200))
	k.SetMimir(ctx, constants.ChurnInterval.String(), 0)
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, int64(0))

	// setting it back undoes everything
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	k.UpdateBlockTime(ctx)
	k.SetMimir(ctx, constants.ChurnInterval.String(), 100)
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, int64(100))
	c.Check(k.GetConfigInt64(ctx, constants.TxOutDelayRate), Equals, rate)
}

// scaling reads BLOCKTIMEMS without charging gas, so gas_used is the same
// whether or not a value scales
func (s *KeeperConfigSuite) TestGetConfigInt64ScalingChargesNoGas(c *C) {
	ctx, k := setupKeeperForTest(c)
	gasOf := func(f func(cosmos.Context)) storetypes.Gas {
		gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		f(gctx)
		return gctx.GasMeter().GasConsumed()
	}
	for _, blockMs := range []int64{0, 500} {
		k.SetMimir(ctx, constants.BlockTimeMs.String(), blockMs)
		k.UpdateBlockTime(ctx)
		mimirOnly := gasOf(func(ctx cosmos.Context) { _, _ = k.GetMimir(ctx, constants.ChurnInterval.String()) })
		scaled := gasOf(func(ctx cosmos.Context) { k.GetConfigInt64(ctx, constants.ChurnInterval) })
		c.Check(scaled, Equals, mimirOnly, Commentf("BLOCKTIMEMS %d", blockMs))
		c.Check(gasOf(func(ctx cosmos.Context) { k.RefBlocks(ctx, 600) }), Equals, storetypes.Gas(0))
	}
}

func (s *KeeperConfigSuite) TestTHORNameFees(c *C) {
	ctx, k := setupKeeperForTest(c)
	perBlock := k.GetConstants().GetInt64Value(constants.TNSFeePerBlock)
	c.Check(k.GetTHORNamePerBlockFee(ctx).Uint64(), Equals, uint64(perBlock))
	c.Check(k.GetTHORNameRefBlockFee(ctx).Uint64(), Equals, uint64(perBlock))

	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx)
	c.Check(k.GetTHORNamePerBlockFee(ctx).Uint64(), Equals, uint64(perBlock/2))
	c.Check(k.GetTHORNameRefBlockFee(ctx).Uint64(), Equals, uint64(perBlock))
}

func (s *KeeperConfigSuite) TestBlockTimeHistory(c *C) {
	ctx, k := setupKeeperForTest(c)
	ref := constants.ThorchainBlockTime.Milliseconds()
	ctx = ctx.WithBlockHeight(100)

	// nothing recorded while the block time never changed: the windows are
	// plain reference-block arithmetic
	c.Check(k.GetBlockTimeHistory(ctx), HasLen, 0)
	c.Check(k.RefWindowStart(ctx, 30), Equals, int64(70))
	c.Check(k.RefWindowEnd(ctx, 30), Equals, int64(130))

	// each change is appended
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx)
	ctx = ctx.WithBlockHeight(200)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	k.UpdateBlockTime(ctx)
	k.UpdateBlockTime(ctx.WithBlockHeight(201)) // unchanged: not recorded
	c.Check(k.GetBlockTimeHistory(ctx), DeepEquals, constants.BlockTimeHistory{
		{Height: 100, Ms: 500},
		{Height: 200, Ms: ref},
	})
	height, prevMs := k.GetBlockTimeChange(ctx)
	c.Check(height, Equals, int64(200))
	c.Check(prevMs, Equals, int64(500))

	// after going back to the default block time at 200, a 30-reference-block
	// window reaches back over 60 half-length blocks...
	ctx = ctx.WithBlockHeight(205)
	// blocks 200-204 at 1000 ms, then 25 s more at 500 ms: 50 blocks before 200
	c.Check(k.RefWindowStart(ctx, 30), Equals, int64(150))
	// ...and an outbound scheduled 60 blocks ahead at block 199 is still ahead
	c.Check(k.RefWindowEnd(ctx, 30), Equals, int64(199+60))
	ctx = ctx.WithBlockHeight(300)
	c.Check(k.RefWindowStart(ctx, 30), Equals, int64(270))
	c.Check(k.RefWindowEnd(ctx, 30), Equals, int64(330))

	// reading the history charges no gas
	ctx = ctx.WithGasMeter(storetypes.NewGasMeter(1_000_000))
	k.GetBlockTimeHistory(ctx)
	k.RefWindowStart(ctx, 30)
	k.RefWindowEnd(ctx, 30)
	c.Check(ctx.GasMeter().GasConsumed(), Equals, storetypes.Gas(0))
}

// After the block time goes back up, outbounds scheduled under the faster block
// time stay pending until LackSigning would reschedule them, and ones scheduled
// further ahead than the current offset window are still found.
func (s *KeeperConfigSuite) TestGetPendingOutboundsAcrossBlockTimeRevert(c *C) {
	ctx, k := setupKeeperForTest(c)
	consts := k.GetConstants()
	c.Assert(consts.GetInt64Value(constants.SigningTransactionPeriod), Equals, int64(300))
	c.Assert(consts.GetInt64Value(constants.MaxTxOutOffset), Equals, int64(720))
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx.WithBlockHeight(1000))
	const revert = 5000
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	k.UpdateBlockTime(ctx.WithBlockHeight(revert))
	ctx = ctx.WithBlockHeight(revert + 10)

	item := func(height int64, amount uint64) {
		txOut := NewTxOut(height)
		txOut.TxArray = []TxOutItem{{
			Chain:       common.BTCChain,
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(amount)),
		}}
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	// 499 half blocks + 11 blocks old = 260.5 s: inside the 300 s period,
	// though 510 blocks back
	item(revert-500, 1)
	// 699 half blocks + 11 blocks old = 360.5 s: rescheduled already
	item(revert-700, 2)
	// scheduled 1400 half blocks ahead just before the revert, after a run of
	// empty heights (the revert already ran, so this one is not converted:
	// it stands for one an earlier, faster block time left beyond the offset)
	item(revert-1+1400, 4)

	var total uint64
	for _, toi := range k.GetPendingOutbounds(ctx, common.BTCAsset) {
		total += toi.Coin.Amount.Uint64()
	}
	c.Check(total, Equals, uint64(1+4))
}

// A block time change converts the deadlines stored as future heights so each
// keeps the wall-clock time it had left, and lockups count wall-clock time.
func (s *KeeperConfigSuite) TestBlockTimeChangeConvertsStoredHeights(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx.WithBlockHeight(100))
	ctx = ctx.WithBlockHeight(150)

	// deadlines set at 500 ms
	jailed, released := GetRandomBech32Addr(), GetRandomBech32Addr()
	c.Assert(k.SetNodeAccountJail(ctx, jailed, 220, "test"), IsNil)
	c.Assert(k.SetNodeAccountJail(ctx, released, 150, "test"), IsNil)
	k.SetTHORName(ctx, NewTHORName("pending", 400, nil))
	k.SetTHORName(ctx, NewTHORName("expired", 180, nil))
	var inHashes []common.TxID
	for _, h := range []int64{210, 211} {
		inHash := GetRandomTxHash()
		inHashes = append(inHashes, inHash)
		c.Assert(k.AppendTxOut(ctx, h, TxOutItem{
			Chain:     common.BTCChain,
			ToAddress: GetRandomBTCAddress(),
			InHash:    inHash,
			Coin:      common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		}), IsNil)
		voter := NewObservedTxVoter(inHash, nil)
		voter.OutboundHeight = h
		k.SetObservedTxInVoter(ctx, voter)
	}
	swapHash := GetRandomTxHash()
	c.Assert(k.AddToLimitSwapTTL(ctx, 260, swapHash), IsNil)
	k.SetMimir(ctx, "NodePauseChainGlobal", 230)

	// back to 1000 ms at 200: what was left halves in blocks,
	// D -> 199 + ceil((D - 199) / 2)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	ctx = ctx.WithBlockHeight(200)
	k.UpdateBlockTime(ctx)

	jail, err := k.GetNodeAccountJail(ctx, jailed)
	c.Assert(err, IsNil)
	c.Check(jail.ReleaseHeight, Equals, int64(210))
	jail, err = k.GetNodeAccountJail(ctx, released)
	c.Assert(err, IsNil)
	c.Check(jail.ReleaseHeight, Equals, int64(150))

	name, err := k.GetTHORName(ctx, "pending")
	c.Assert(err, IsNil)
	c.Check(name.ExpireBlockHeight, Equals, int64(300))
	// expired names are hidden at the current height: read it before expiry
	name, err = k.GetTHORName(ctx.WithBlockHeight(170), "expired")
	c.Assert(err, IsNil)
	c.Check(name.ExpireBlockHeight, Equals, int64(180))

	// both queued outbounds land at 205; their voters follow
	for _, h := range []int64{210, 211} {
		txOut, err := k.GetTxOut(ctx, h)
		c.Assert(err, IsNil)
		c.Check(txOut.TxArray, HasLen, 0)
	}
	txOut, err := k.GetTxOut(ctx, 205)
	c.Assert(err, IsNil)
	c.Check(txOut.TxArray, HasLen, 2)
	for _, inHash := range inHashes {
		voter, err := k.GetObservedTxInVoter(ctx, inHash)
		c.Assert(err, IsNil)
		c.Check(voter.OutboundHeight, Equals, int64(205))
	}

	hashes, err := k.GetLimitSwapTTL(ctx, 260)
	c.Assert(err, IsNil)
	c.Check(hashes, HasLen, 0)
	hashes, err = k.GetLimitSwapTTL(ctx, 230)
	c.Assert(err, IsNil)
	c.Check(hashes, DeepEquals, []common.TxID{swapHash})

	pause, err := k.GetMimir(ctx, "NodePauseChainGlobal")
	c.Assert(err, IsNil)
	c.Check(pause, Equals, int64(215))

	// from 150: 49 blocks of 500 ms then 11 of 1000 ms by 210
	ctx = ctx.WithBlockHeight(210)
	c.Check(k.RefBlocksSince(ctx, 150), Equals, int64(35))
	c.Check(k.RefHeightAfter(ctx, 150, 40), Equals, int64(215))
	c.Check(k.RefBlocksSince(ctx, 205), Equals, int64(5))
}

func (s *KeeperConfigSuite) TestLockupHelpersWithoutBlockTimeChange(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(500)
	c.Check(k.RefBlocksSince(ctx, 120), Equals, int64(380))
	c.Check(k.RefBlocksSince(ctx, 510), Equals, int64(-10))
	c.Check(k.RefHeightAfter(ctx, 120, 300), Equals, int64(420))
}

// A change also moves the queued observation tallies and the refund cleanup
// marker expiries, keeping the wall-clock time each had left.
func (s *KeeperConfigSuite) TestBlockTimeChangeConvertsTalliesAndRefundCleanups(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx.WithBlockHeight(100))
	ctx = ctx.WithBlockHeight(150)

	txID := GetRandomTxHash().String()
	k.QueueObservationTally(ctx, types.ObservationTallyEntry{DueHeight: 160, ConsensusHeight: 150, Kind: "IN", Chain: common.BTCChain, Ref: []string{txID}})
	k.QueueObservationTally(ctx, types.ObservationTallyEntry{DueHeight: 220, ConsensusHeight: 150, Kind: "SOLVENCY", Chain: common.ETHChain, Ref: []string{txID}})
	k.SetRefundCleanup(ctx, "marker")
	// 432000 reference blocks at 500 ms
	c.Assert(k.HasRefundCleanup(ctx.WithBlockHeight(150+864000), "marker"), Equals, true)
	c.Assert(k.HasRefundCleanup(ctx.WithBlockHeight(150+864001), "marker"), Equals, false)

	// back to 1000 ms at 200: what was left halves in blocks
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 0)
	k.UpdateBlockTime(ctx.WithBlockHeight(200))

	// the one already due before the change is left alone
	entries, err := k.PopObservationTallies(ctx, 199)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Check(entries[0].DueHeight, Equals, int64(160))
	entries, err = k.PopObservationTallies(ctx, 209)
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 0)
	// 220 - 199 = 21 half blocks: 11 blocks
	entries, err = k.PopObservationTallies(ctx, 210)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Check(entries[0].DueHeight, Equals, int64(210))
	c.Check(entries[0].Kind, Equals, "SOLVENCY")
	c.Check(entries[0].Chain, Equals, common.ETHChain)
	c.Check(entries[0].Ref, DeepEquals, []string{txID})

	// 150 + 864000 - 199 half blocks: 431976 (rounded up) blocks after 199
	expiry := int64(199 + 431976)
	c.Check(k.HasRefundCleanup(ctx.WithBlockHeight(expiry), "marker"), Equals, true)
	c.Check(k.HasRefundCleanup(ctx.WithBlockHeight(expiry+1), "marker"), Equals, false)
	k.PruneRefundCleanups(ctx.WithBlockHeight(expiry))
	c.Check(k.HasRefundCleanup(ctx.WithBlockHeight(expiry), "marker"), Equals, false)
}

// When the block time goes down, the queue is converted without opening gaps
// past the offset: a contiguous run there stays contiguous, so scans that stop
// at the first empty height past the offset still find every outbound.
func (s *KeeperConfigSuite) TestBlockTimeChangeKeepsQueueContiguous(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.MaxTxOutOffset.String(), 5)
	const h = int64(100)
	ctx = ctx.WithBlockHeight(h - 1)
	// queued at the default block time: within the offset (101-105) and past it
	for d := int64(1); d <= 8; d++ {
		c.Assert(k.AppendTxOut(ctx, h+d, TxOutItem{
			Chain:     common.BTCChain,
			ToAddress: GetRandomBTCAddress(),
			InHash:    GetRandomTxHash(),
			Coin:      common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(d)*common.One)),
		}), IsNil)
	}
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	ctx = ctx.WithBlockHeight(h)
	k.UpdateBlockTime(ctx)
	c.Assert(k.GetBlockTimeMs(ctx), Equals, int64(500))

	// the offset now reaches 110; within it each height keeps its time, past
	// it the run continues from 111 rather than spreading to 113, 115, ...
	var heights []int64
	for hh := h + 1; hh <= h+20; hh++ {
		txOut, err := k.GetTxOut(ctx, hh)
		c.Assert(err, IsNil)
		if len(txOut.TxArray) > 0 {
			heights = append(heights, hh)
		}
	}
	c.Check(heights, DeepEquals, []int64{103, 105, 107, 109, 111, 112, 113, 114})
	c.Check(k.GetPendingOutbounds(ctx, common.BTCAsset), HasLen, 8)
}
