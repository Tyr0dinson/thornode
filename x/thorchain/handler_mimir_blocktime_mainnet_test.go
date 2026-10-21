//go:build !mocknet

package thorchain

import (
	. "gopkg.in/check.v1"
)

func (HandlerMimirSuite) TestBlockTimeMsDisabledOnMainnet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewMimirHandler(mgr)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	mkMsg := func(key string, value int64) MsgMimir {
		return MsgMimir{Key: key, Value: value, Signer: na.NodeAddress}
	}

	// BLOCKTIMEMS is not enabled until block-count settings scale with it:
	// every nonzero value is rejected, in range or not
	for _, v := range []int64{1499, 1500, 2000, 10_000, 10_001, -1} {
		err := h.validate(ctx, mkMsg("BLOCKTIMEMS", v))
		c.Assert(err, ErrorMatches, ".*BLOCKTIMEMS is not enabled on this network yet.*", Commentf("%d", v))
	}
	c.Assert(h.validate(ctx, mkMsg("blocktimems", 2000)), NotNil)
	// zero (off) is still accepted
	c.Assert(h.validate(ctx, mkMsg("BLOCKTIMEMS", 0)), IsNil)
}
