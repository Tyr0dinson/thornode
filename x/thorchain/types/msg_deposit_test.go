package types

import (
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	common "gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type MsgDepositSuite struct{}

var _ = Suite(&MsgDepositSuite{})

func (MsgDepositSuite) TestMsgDepositSuite(c *C) {
	acc1 := GetRandomBech32Addr()
	c.Assert(acc1.Empty(), Equals, false)

	coins := common.Coins{
		common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One)),
	}
	memo := "hello"
	msg := NewMsgDeposit(coins, memo, acc1)
	msg.Salt = []byte("salt")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc1.String())
	c.Assert(msg.GetSalt(), DeepEquals, []byte("salt"))

	// ensure non-native assets are blocked
	coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(12*common.One)),
	}
	msg = NewMsgDeposit(coins, memo, acc1)
	c.Assert(msg.ValidateBasic(), NotNil)

	msg1 := NewMsgDeposit(coins, "memo", cosmos.AccAddress{})
	err1 := msg1.ValidateBasic()
	c.Assert(err1, NotNil)
	c.Assert(errors.Is(err1, se.ErrInvalidAddress), Equals, true)

	msg2 := NewMsgDeposit(common.Coins{
		common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()),
	}, "memo", acc1)
	err2 := msg2.ValidateBasic()
	c.Assert(err2, NotNil)
	c.Assert(errors.Is(err2, se.ErrUnknownRequest), Equals, true)

	msg3 := NewMsgDeposit(common.Coins{
		common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One)),
	}, "asdfsdkljadslfasfaqcvbncvncvbncvbncvbncvbncvbncvbncvbncvbncvbnsdfasdfasfasdfkjqwerqlkwerqlerqwlkerjqlwkerjqwlkerjqwlkerjqlkwerjklqwerjqwlkerjqlwkerjwqelrasdfsdkljadslfasfaqcvbncvncvbncvbncvbncvbncvbncvbncvbncvbncvbnsdfasdfasfasdfkjqwerqlkwerqlerqwlkerjqlwkerjqwlkerjqwlkerjqlkwerjklqwerjqwlkerjqlwkerjwqelr", acc1)
	err3 := msg3.ValidateBasic()
	c.Assert(err3, NotNil)
	c.Assert(errors.Is(err3, se.ErrUnknownRequest), Equals, true)

	msg4 := NewMsgDeposit(common.Coins{
		common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One)),
	}, "memo", acc1)
	msg4.Salt = []byte(strings.Repeat("s", constants.MaxDepositSaltSize+1))
	err4 := msg4.ValidateBasic()
	c.Assert(err4, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)
}

func (MsgDepositSuite) TestMsgDepositMemoCharacters(c *C) {
	acc := GetRandomBech32Addr()
	coins := common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))}
	dest := "0x1111111111111111111111111111111111111111"

	// memos THORChain executes must still pass, including the empty memo of a
	// memoless native deposit, the registration of a memoless memo and free
	// text after the "|" data separator
	for _, memo := range []string{
		"",
		" ",
		"r:00001",
		"=:ETH.ETH:" + dest + ":1e6/1/0:t:10",
		"REFERENCE:BTC.BTC:=:ETH.ETH:" + dest + "::t:1",
		"=:ETH.ETH:" + dest + "|hello world",
		"=:ETH.ETH:" + dest + "|\u00E9t\u00E9 \u2192 \U0001F680",
	} {
		c.Check(NewMsgDeposit(coins, memo, acc).ValidateBasic(), IsNil, Commentf("memo %q", memo))
	}

	// a control character or invalid UTF-8 anywhere is refused before the
	// ante handler takes the fee, so the tx never reaches a block
	for _, memo := range []string{
		"\x00",
		"REFERENCE:BTC.BTC:=:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4\x00::t:1",
		"=:ETH.ETH:" + dest + "|\x00",
		"=:ETH.ETH:" + dest + ":0:t:0:::0:\x00",
		"NOOP:\x00",
		"REFERENCE:BTC.BTC:=:ETH.ETH:" + dest + "\nOUT:1",
		"=:ETH.ETH:" + dest + "\r\n",
		"\n",
		"\t",
		"a\x7f",
		"a\u0085",
		"a\xff",
		"a\xc0\x80",
	} {
		err := NewMsgDeposit(coins, memo, acc).ValidateBasic()
		c.Check(err, NotNil, Commentf("memo %q", memo))
		c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true, Commentf("memo %q: %v", memo, err))
		c.Check(err, ErrorMatches, "memo (contains control character|is not valid UTF-8) .*", Commentf("memo %q", memo))
	}
}
