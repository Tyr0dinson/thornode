//go:build !mocknet

package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// on mainnet the reference block is 6s, so a 2s target triples durations; the
// rate limit gets there in two steps a ChurnInterval of wall-clock time apart
func (s *KeeperConfigSuite) TestBlockTimeMsMainnetScale(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(constants.ThorchainBlockTime.Milliseconds(), Equals, int64(6000))
	churn := k.GetConfigInt64(ctx, constants.ChurnInterval)
	rate := k.GetConfigInt64(ctx, constants.TxOutDelayRate)
	latch := func(h int64) int64 {
		hctx := ctx.WithBlockHeight(h)
		k.UpdateBlockTime(hctx)
		return k.GetBlockTimeMs(hctx)
	}

	// the first change halves at most
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 2000)
	start := ctx.BlockHeight()
	c.Check(latch(start), Equals, int64(3000))
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, churn*2)

	// a ChurnInterval of wall-clock time at 3s is churn*2 blocks
	c.Check(latch(start+churn*2-1), Equals, int64(3000))
	c.Check(latch(start+churn*2), Equals, int64(2000))
	c.Check(k.GetConfigInt64(ctx, constants.ChurnInterval), Equals, churn*3)
	c.Check(k.GetConfigInt64(ctx, constants.TxOutDelayRate), Equals, (rate+1)/3)
	c.Check(k.RefBlocks(ctx, 600), Equals, int64(1800))

	// below the mainnet floor a stored value is clamped to 1500
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 1000)
	c.Check(latch(start+churn*5), Equals, int64(1500))
}
