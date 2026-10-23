package signer

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
)

type SigningWindowSuite struct{}

var _ = Suite(&SigningWindowSuite{})

func (s *SigningWindowSuite) TestSigningWindowExpired(c *C) {
	ref := constants.ThorchainBlockTime.Milliseconds()

	// without a block time change: blockHeight - window > height - buffer
	for _, height := range []int64{849, 850, 851} {
		c.Check(signingWindowExpired(height, 1000, 300, 150, ref, nil), Equals, 1000-300 > height-150)
	}

	// After the block time halves at 900, the window is 600 blocks and the
	// buffer is 150 reference blocks of time. At 1000 an outbound from 800 is
	// 99 slow blocks plus 101 fast ones old (149.5 reference blocks), one from
	// 799 is 150.5: past it. A block count against the new window would keep
	// signing everything from 550 on, past THORChain's reschedule.
	half := constants.BlockTimeHistory{{Height: 900, Ms: ref / 2}}
	c.Check(signingWindowExpired(800, 1000, 600, 150, ref/2, half), Equals, false)
	c.Check(signingWindowExpired(799, 1000, 600, 150, ref/2, half), Equals, true)
	c.Check(signingWindowExpired(798, 1000, 600, 150, ref/2, half), Equals, true)
	c.Check(signingWindowExpired(600, 1000, 600, 150, ref/2, half), Equals, true)

	// After the block time doubles back at 900, the window is 300 blocks. An
	// outbound from 801 is 98 fast blocks plus 101 slow ones old at 1000:
	// exactly 150 reference blocks, still signable as without a change; one
	// from 800 is past it.
	double := constants.BlockTimeHistory{{Height: 100, Ms: ref / 2}, {Height: 900, Ms: ref}}
	c.Check(signingWindowExpired(801, 1000, 300, 150, ref, double), Equals, false)
	c.Check(signingWindowExpired(800, 1000, 300, 150, ref, double), Equals, true)
}

// Round-7 retry attempts count signing periods of wall-clock time, as THORNode
// does, so bifrost does not give up on an outbound THORNode still expects.
func (s *SigningWindowSuite) TestRound7Attempts(c *C) {
	ref := constants.ThorchainBlockTime.Milliseconds()
	// without a block time change: (blockHeight - height) / window
	c.Check(round7Attempts(100, 1000, 300, ref, nil), Equals, int64(3))
	c.Check(round7Attempts(100, 999, 300, ref, nil), Equals, int64(2))

	// the block time quadruples at 10100 after 10000 blocks at a quarter of it:
	// those are 2500 reference blocks, 8.3 periods, where counting blocks at the
	// new 75-block period would make 133
	quarter := constants.BlockTimeHistory{{Height: 100, Ms: ref / 4}, {Height: 10100, Ms: ref}}
	c.Check(round7Attempts(100, 10100, 300, ref, quarter), Equals, int64(8))
	c.Check(round7Attempts(100, 10200, 300, ref, quarter), Equals, int64(8))
	c.Check(round7Attempts(100, 10200+200, 300, ref, quarter), Equals, int64(9))
}
