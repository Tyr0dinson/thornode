package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

type MsgLedgerSuite struct{}

var _ = Suite(&MsgLedgerSuite{})

func (s MsgLedgerSuite) TestMsgLedger(c *C) {
	coins := common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100)),
	}
	ledger := Ledger{
		PubKey: GetRandomPubKey(),
		Coins:  coins,
	}
	msg := NewMsgLedger(1, common.BNBChain, []Ledger{ledger}, GetRandomBech32Addr())
	c.Check(msg.Type(), Equals, "set_ledger")
	EnsureMsgBasicCorrect(msg, c)
	c.Assert(msg.ValidateBasic(), IsNil)

	msg = NewMsgLedger(0, common.BNBChain, []Ledger{ledger}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgLedger(1, "", []Ledger{ledger}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgLedger(1, common.BNBChain, []Ledger{}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgLedger(1, common.BNBChain, []Ledger{ledger}, cosmos.AccAddress{})
	c.Assert(msg.ValidateBasic(), NotNil)
}
