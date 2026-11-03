package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// runGuardCycles brings one network fee voter on chain to consensus per block from
// ctx's height for cycles cycles of miss, miss, pass (nodes 17-19 miss), scoring
// every block, then scores the rest. It returns the context after the last score.
func runGuardCycles(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, chain common.Chain, cycles int) cosmos.Context {
	start := ctx.BlockHeight()
	for i := 0; i < 3*cycles; i++ {
		signers := indexes(0, 17)
		if i%3 == 2 {
			signers = indexes(0, 20)
		}
		nf := &common.NetworkFee{Chain: chain, Height: start + int64(i), TransactionRate: 7, TransactionSize: 9}
		signNetworkFee(c, ctx, mgr, nas, nf, signers...)
		c.Assert(processObservationTallies(ctx, mgr), IsNil)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	}
	for i := 0; i < 100; i++ {
		c.Assert(processObservationTallies(ctx, mgr), IsNil)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	}
	return ctx
}

// The grace a run of guarded voters uses, and what has refilled of it, are kept
// to the millisecond across runs. Rounded to whole reference blocks, at faster
// blocks a recovery of one block erased the debt of a two-block run every time,
// so a group missing two voters in three was spared forever.
func (s *ObservationTallySuite) TestGuardDebtAccumulatesAtFasterBlocks(c *C) {
	ref := constants.ThorchainBlockTime.Milliseconds()
	// Each cycle uses two blocks of grace and refills one. The guard spares the
	// same wall-clock time at either block time, 39 reference blocks: 39
	// voters at the reference block time, 79 at half of it. Rounding spared all
	// 180 half-length ones (missed 0, total 60).
	for _, tc := range []struct {
		ms            int64
		cycles        int
		missed, total int64
	}{
		{ref, 30, 21, 51},
		{ref / 2, 60, 41, 101}, // the same wall-clock time
	} {
		ctx, mgr, nas := setupObservationTallyTest(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 10)
		if tc.ms != ref {
			k.SetBlockTimeHistory(ctx, tc.ms, constants.BlockTimeHistory{{Height: ctx.BlockHeight(), Ms: tc.ms}})
		}
		ctx = runGuardCycles(c, ctx, mgr, nas, common.BSCChain, tc.cycles)
		for _, i := range []int{17, 18, 19} {
			checkTally(c, ctx, mgr, nas[i], common.BSCChain, tc.missed, tc.total)
		}
		checkTally(c, ctx, mgr, nas[0], common.BSCChain, 0, tc.total)
	}
}
