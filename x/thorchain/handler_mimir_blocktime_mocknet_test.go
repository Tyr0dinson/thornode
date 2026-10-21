//go:build mocknet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
)

func (HandlerMimirSuite) TestBlockTimeMsValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewMimirHandler(mgr)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	mkMsg := func(key string, value int64) MsgMimir {
		return MsgMimir{Key: key, Value: value, Signer: na.NodeAddress}
	}

	// BLOCKTIMEMS is enabled on mocknet: zero (disengage) and the bounds are
	// accepted, in any key casing
	for _, v := range []int64{0, constants.BlockTimeMsMin, 3000, constants.BlockTimeMsMax} {
		c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", v)), IsNil, Commentf("%d", v))
	}
	c.Assert(h.validate(ctx, mkMsg("BlockTimeMs", 2000)), IsNil)
	// values outside the range are rejected (BlockTimeMsMin is 1500 on
	// mainnet, so 1499 is rejected there; mocknet lowers it to 200)
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", constants.BlockTimeMsMin-1)), NotNil)
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", 10_000)), IsNil)
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", 10_001)), NotNil)
	c.Assert(h.validate(ctx, mkMsg("blocktimems", 1)), NotNil)
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", 1500)), IsNil)
	// a negative vote passes validation like any other mimir: it never sets
	// an effective value (handle ignores negative supermajorities), so it
	// cannot engage the hook, which also treats <= 0 as off
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", -1)), IsNil)
}
