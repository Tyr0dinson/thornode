package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type BlockTimeAgesSuite struct{}

var _ = Suite(&BlockTimeAgesSuite{})

// The observation flex window, reference memo TTL and clout reset are ages in
// wall-clock time: after the block time goes from 1500ms to 6000ms, the 1500ms
// blocks count for what they lasted.
func (s *BlockTimeAgesSuite) TestAgesAcrossBlockTimeIncrease(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(constants.ThorchainBlockTime.Milliseconds(), Equals, int64(1000))
	// a 1500ms to 6000ms change, past the rate limit
	k.SetBlockTimeHistory(ctx, 6000, constants.BlockTimeHistory{{Height: 100, Ms: 1500}, {Height: 200, Ms: 6000}})
	at := ctx.WithBlockHeight

	// from 180: 19 blocks of 1500ms (28.5) then 6000ms blocks
	c.Check(withinObservationFlex(at(199), k, 180, 28), Equals, true)
	c.Check(withinObservationFlex(at(200), k, 180, 28), Equals, false)
	c.Check(withinObservationFlex(at(200), k, 180, 34), Equals, true)
	c.Check(withinObservationFlex(at(201), k, 180, 34), Equals, false)
	// without a change it is height + flex >= now
	c.Check(withinObservationFlex(at(60), k, 50, 10), Equals, true)
	c.Check(withinObservationFlex(at(61), k, 50, 10), Equals, false)

	ref := ReferenceMemo{Height: 180}
	c.Check(referenceMemoExpired(at(200), k, ref, 34), Equals, false)
	c.Check(referenceMemoExpired(at(201), k, ref, 34), Equals, true)

	clout := SwapperClout{LastSpentHeight: 180, Spent: cosmos.NewUint(100), Reclaimed: cosmos.ZeroUint()}
	restoreClout(at(200), k, &clout, 34)
	c.Check(clout.Reclaimed.IsZero(), Equals, true)
	restoreClout(at(201), k, &clout, 34)
	c.Check(clout.Reclaimed.Equal(cosmos.NewUint(100)), Equals, true)
}
