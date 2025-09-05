package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

type MsgReferenceMemoSuite struct{}

var _ = Suite(&MsgReferenceMemoSuite{})

func (mas *MsgReferenceMemoSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (MsgReferenceMemoSuite) TestMsgReferenceMemo(c *C) {
	signerAddr := GetRandomBech32Addr()
	var blankHash common.TxID
	msgApply := NewMsgReferenceMemo(blankHash, common.BTCChain, "test memo", signerAddr)
	c.Assert(msgApply.ValidateBasic(), IsNil)
	c.Assert(msgApply.Route(), Equals, RouterKey)
	c.Assert(msgApply.Type(), Equals, "ref_memo")
	c.Assert(msgApply.GetSignBytes(), NotNil)
	c.Assert(len(msgApply.GetSigners()), Equals, 1)
	c.Assert(msgApply.GetSigners()[0].Equals(signerAddr), Equals, true)

	// failure cases
	c.Check(NewMsgReferenceMemo(blankHash, common.EmptyChain, "test memo", signerAddr).ValidateBasic(), NotNil)
	c.Check(NewMsgReferenceMemo(blankHash, common.BTCChain, "", signerAddr).ValidateBasic(), NotNil)
}
