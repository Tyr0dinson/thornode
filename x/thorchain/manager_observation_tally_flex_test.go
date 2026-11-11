package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// observationFlexBoundary reaches consensus on a network fee at H with nodes
// 0-13 of 20, nodes 14-18 sign at H+1, and applies a block time change to
// changeMs at changeAt if one is given. It finds the last height a late
// signature is still refunded and checks the tally scores the voter in that
// block, after its handlers: node 19 signing then gets its slash points back
// and is not counted missed. With late, node 19 signs one block later and
// must get neither.
func observationFlexBoundary(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, changeAt, changeMs int64, late bool) {
	k := mgr.Keeper()
	H := ctx.BlockHeight()
	flex := k.GetConfigInt64Ref(ctx, constants.ObservationDelayFlexibility)
	nf := &common.NetworkFee{Chain: common.ETHChain, Height: 500, TransactionRate: 256, TransactionSize: 100}
	signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 14)...)
	signNetworkFee(c, ctx.WithBlockHeight(H+1), mgr, nas, nf, indexes(14, 19)...)
	if changeAt > 0 {
		k.SetMimir(ctx, constants.BlockTimeMs.String(), changeMs)
		k.UpdateBlockTime(ctx.WithBlockHeight(changeAt))
		c.Assert(k.GetBlockTimeMs(ctx), Equals, changeMs)
	}

	lastRefund := int64(0)
	for h := H + 1; h <= H+100; h++ {
		if withinObservationFlex(ctx.WithBlockHeight(h), k, H, flex) {
			lastRefund = h
		}
	}
	c.Assert(lastRefund, Not(Equals), int64(0))
	signAt := lastRefund
	if late {
		signAt++
	}
	scoredAt := int64(0)
	var before, after int64
	for h := H + 1; h <= signAt+5; h++ {
		// handlers run before EndBlock scores the tally
		if h == signAt {
			before, _ = k.GetNodeAccountSlashPoints(ctx, nas[19].NodeAddress)
			signNetworkFee(c, ctx.WithBlockHeight(h), mgr, nas, nf, 19)
			after, _ = k.GetNodeAccountSlashPoints(ctx, nas[19].NodeAddress)
		}
		c.Assert(processObservationTallies(ctx.WithBlockHeight(h), mgr), IsNil)
		if _, total, _ := k.GetObservationTally(ctx, nas[19].NodeAddress, common.ETHChain); total > 0 && scoredAt == 0 {
			scoredAt = h
		}
	}
	c.Check(scoredAt, Equals, lastRefund, Commentf("the tally must close the window the refund closes"))
	missed, total, err := k.GetObservationTally(ctx, nas[19].NodeAddress, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(total, Equals, observationTallyUnit)
	if late {
		c.Check(after, Equals, before, Commentf("no refund after the window"))
		c.Check(missed, Equals, observationTallyUnit)
	} else {
		c.Check(after < before, Equals, true, Commentf("a signature in the window is refunded"))
		c.Check(missed, Equals, int64(0))
	}
}

// The late-observation refund and the observation tally close the
// ObservationDelayFlexibility window at the same block. At half the reference
// block time the tally scores at H+20 (10 reference blocks), and a signature at
// H+21 used to be refunded while the node was still counted missed.
func (s *ObservationTallySuite) TestObservationFlexWindowMatchesTallyAtHalfBlockTime(c *C) {
	ref := constants.ThorchainBlockTime.Milliseconds()
	for _, late := range []bool{false, true} {
		ctx, mgr, nas := setupObservationTallyTest(c)
		H := ctx.BlockHeight()
		mgr.Keeper().SetBlockTimeHistory(ctx, ref/2, constants.BlockTimeHistory{{Height: H - 100, Ms: ref / 2}})
		observationFlexBoundary(c, ctx, mgr, nas, 0, 0, late)
	}
}

func (s *ObservationTallySuite) TestObservationFlexWindowMatchesTallyAtReference(c *C) {
	for _, late := range []bool{false, true} {
		ctx, mgr, nas := setupObservationTallyTest(c)
		observationFlexBoundary(c, ctx, mgr, nas, 0, 0, late)
	}
}

// The block time halves inside the window, and the tally's due height moves
// with it. From a block time that does not divide the window (0.7 of the
// reference), converting the stored due height rounds up and closes the tally
// blocks after the refund, so both must be the window recomputed over the
// history.
func (s *ObservationTallySuite) TestObservationFlexWindowMatchesTallyAcrossBlockTimeChange(c *C) {
	ref := constants.ThorchainBlockTime.Milliseconds()
	for _, tc := range []struct {
		name           string
		startMs, endMs int64
	}{
		{name: "reference to half", startMs: ref, endMs: ref / 2},
		{name: "0.7 to 0.35 of the reference", startMs: ref * 7 / 10, endMs: ref * 7 / 20},
	} {
		for _, late := range []bool{false, true} {
			c.Logf("%s, late=%v", tc.name, late)
			ctx, mgr, nas := setupObservationTallyTest(c)
			H := ctx.BlockHeight()
			if tc.startMs != ref {
				mgr.Keeper().SetBlockTimeHistory(ctx, tc.startMs, constants.BlockTimeHistory{{Height: H - 100, Ms: tc.startMs}})
			}
			observationFlexBoundary(c, ctx, mgr, nas, H+3, tc.endMs, late)
		}
	}
}
