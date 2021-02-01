package types

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	common "gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

type MsgCompositeSuite struct{}

var _ = Suite(&MsgCompositeSuite{})

func (MsgCompositeSuite) TestMsgMintCompositeSuite(c *C) {
	acc1 := GetRandomBech32Addr()
	c.Assert(acc1.Empty(), Equals, false)

	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One))
	id := "hello"
	msg := NewMsgMintComposite(coin, id, acc1)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "mint_composite")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(len(msg.GetSignBytes()) > 0, Equals, true)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc1.String())

	// ensure non-native assets are blocked
	coin = common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One))
	msg = NewMsgMintComposite(coin, id, acc1)
	c.Assert(msg.ValidateBasic(), NotNil)

	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One))
	msg1 := NewMsgMintComposite(coin, "memo", cosmos.AccAddress{})
	err1 := msg1.ValidateBasic()
	c.Assert(err1, NotNil)
	c.Assert(errors.Is(err1, se.ErrInvalidAddress), Equals, true)

	msg2 := NewMsgMintComposite(common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()),
		"fake id", acc1)
	err2 := msg2.ValidateBasic()
	c.Assert(err2, NotNil)
	c.Assert(errors.Is(err2, se.ErrUnknownRequest), Equals, true)

}

func (MsgCompositeSuite) TestMsgBurnCompositeSuite(c *C) {
	acc1 := GetRandomBech32Addr()
	c.Assert(acc1.Empty(), Equals, false)

	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One))
	msg := NewMsgBurnComposite(coin, acc1)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "burn_composite")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(len(msg.GetSignBytes()) > 0, Equals, true)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc1.String())

	// ensure non-native assets are blocked
	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One))
	msg = NewMsgBurnComposite(coin, acc1)
	c.Assert(msg.ValidateBasic(), NotNil)

	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One))
	msg1 := NewMsgBurnComposite(coin, cosmos.AccAddress{})
	err1 := msg1.ValidateBasic()
	c.Assert(err1, NotNil)
	c.Assert(errors.Is(err1, se.ErrInvalidAddress), Equals, true)
}
