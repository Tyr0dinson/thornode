//go:build !mocknet

package thorchain

import (
	. "gopkg.in/check.v1"
)

// the BLOCKTIMEMS hard limits on real networks: 1500ms to 10000ms, 0 = off
func (HandlerMimirSuite) TestBlockTimeMsBoundsOnMainnet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewMimirHandler(mgr)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	mkMsg := func(key string, value int64) MsgMimir {
		return MsgMimir{Key: key, Value: value, Signer: na.NodeAddress}
	}

	for _, v := range []int64{0, 1500, 2000, 10_000} {
		c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", v)), IsNil, Commentf("%d", v))
	}
	for _, v := range []int64{1, 200, 1499, 10_001} {
		c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", v)), NotNil, Commentf("%d", v))
	}
	c.Assert(h.validate(ctx, mkMsg("blocktimems", 1499)), NotNil)
	// a negative vote passes validation like any other mimir: it never sets
	// an effective value (handle ignores negative supermajorities), so it
	// cannot engage the hook, which also treats <= 0 as off
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", -1)), IsNil)
}
