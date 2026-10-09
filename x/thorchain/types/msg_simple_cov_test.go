package types

import (
	"errors"
	"strings"

	sdkmath "cosmossdk.io/math"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgSimpleCovSuite struct{}

var _ = Suite(&MsgSimpleCovSuite{})

func thorTxCov(coins common.Coins) common.Tx {
	return common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: GetRandomTHORAddress(),
		ToAddress:   GetRandomTHORAddress(),
		Coins:       coins,
		Gas:         common.Gas{common.NewCoin(common.RuneNative, cosmos.NewUint(2000000))},
	}
}

func (s *MsgSimpleCovSuite) TestMsgRunePoolDeposit(c *C) {
	signer := GetRandomBech32Addr()
	rune100 := common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(100)))
	msg := NewMsgRunePoolDeposit(signer, thorTxCov(rune100))
	c.Assert(msg.Signer.Equals(signer), Equals, true)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	btcTx := thorTxCov(rune100)
	btcTx.Chain = common.BTCChain
	twoCoins := thorTxCov(common.NewCoins(
		common.NewCoin(common.RuneNative, cosmos.NewUint(1)),
		common.NewCoin(common.TCY, cosmos.NewUint(1)),
	))
	cases := []struct {
		msg *MsgRunePoolDeposit
		err error
	}{
		{NewMsgRunePoolDeposit(signer, btcTx), se.ErrUnauthorized},
		{NewMsgRunePoolDeposit(signer, thorTxCov(nil)), se.ErrInvalidCoins},
		{NewMsgRunePoolDeposit(signer, twoCoins), se.ErrInvalidCoins},
		{NewMsgRunePoolDeposit(signer, thorTxCov(common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))})), se.ErrInvalidCoins},
		{NewMsgRunePoolDeposit(signer, thorTxCov(common.Coins{common.NewCoin(common.TCY, cosmos.NewUint(1))})), se.ErrInvalidCoins},
		{NewMsgRunePoolDeposit(cosmos.AccAddress{}, thorTxCov(rune100)), se.ErrInvalidAddress},
		{NewMsgRunePoolDeposit(signer, thorTxCov(common.Coins{common.NewCoin(common.RuneNative, cosmos.ZeroUint())})), se.ErrUnknownRequest},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d: %v", i, tc.msg.ValidateBasic()))
	}
}

func (s *MsgSimpleCovSuite) TestMsgRunePoolWithdraw(c *C) {
	signer := GetRandomBech32Addr()
	aff := GetRandomTHORAddress()
	tx := thorTxCov(nil)
	msg := NewMsgRunePoolWithdraw(signer, tx, cosmos.NewUint(5000), aff, cosmos.NewUint(10))
	c.Assert(msg.BasisPoints.Uint64(), Equals, uint64(5000))
	c.Assert(msg.AffiliateAddress, Equals, aff)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})
	// no affiliate is fine
	c.Assert(NewMsgRunePoolWithdraw(signer, tx, cosmos.NewUint(1), common.NoAddress, cosmos.ZeroUint()).ValidateBasic(), IsNil)

	cases := []struct {
		msg *MsgRunePoolWithdraw
		err error
	}{
		{NewMsgRunePoolWithdraw(signer, thorTxCov(common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(1)))), cosmos.NewUint(1), aff, cosmos.ZeroUint()), se.ErrInvalidCoins},
		{NewMsgRunePoolWithdraw(cosmos.AccAddress{}, tx, cosmos.NewUint(1), aff, cosmos.ZeroUint()), se.ErrInvalidAddress},
		{NewMsgRunePoolWithdraw(signer, tx, cosmos.ZeroUint(), aff, cosmos.ZeroUint()), se.ErrUnknownRequest},
		{NewMsgRunePoolWithdraw(signer, tx, cosmos.NewUint(10001), aff, cosmos.ZeroUint()), se.ErrUnknownRequest},
		{NewMsgRunePoolWithdraw(signer, tx, cosmos.NewUint(1), aff, cosmos.NewUint(10001)), se.ErrUnknownRequest},
		{NewMsgRunePoolWithdraw(signer, tx, cosmos.NewUint(1), common.NoAddress, cosmos.NewUint(1)), se.ErrInvalidAddress},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgSolNonceAttest(c *C) {
	const (
		nonceAcct = "463gZNUrNwF285KgUTRdrMEUHANWPgQGm16spxLYUU7a"
		nonce     = "66zvntcj2zeBysHExBU78PLYKqKCqhpGuaEdndfZz7tn"
	)
	signer := GetRandomBech32Addr()
	pk := GetRandomPubKey()
	lastTx := GetRandomTxHash()
	msg := NewMsgSolNonceAttest(pk, nonceAcct, nonce, 5, lastTx, signer)
	c.Assert(msg.VaultPubKey, Equals, pk)
	c.Assert(msg.Slot, Equals, int64(5))
	c.Assert(msg.LastTx, Equals, lastTx)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	cases := []struct {
		msg   *MsgSolNonceAttest
		match string
	}{
		{NewMsgSolNonceAttest(common.EmptyPubKey, nonceAcct, nonce, 5, lastTx, signer), ".*vault pubkey is empty.*"},
		{NewMsgSolNonceAttest(pk, nonceAcct, nonce, 5, common.TxID("bogus"), signer), ".*invalid last tx.*"},
		{NewMsgSolNonceAttest(pk, "", nonce, 5, lastTx, signer), ".*nonce_account cannot be empty.*"},
		{NewMsgSolNonceAttest(pk, nonceAcct, "0OIl", 5, lastTx, signer), ".*nonce must be a 32-byte base58 value.*"},
		{NewMsgSolNonceAttest(pk, nonceAcct, nonce, 0, lastTx, signer), ".*slot must be positive.*"},
		{NewMsgSolNonceAttest(pk, nonceAcct, nonce, 5, lastTx, cosmos.AccAddress{}), ".*invalid sender.*"},
	}
	for i, tc := range cases {
		c.Check(tc.msg.ValidateBasic(), ErrorMatches, tc.match, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgSwitch(c *C) {
	acc := GetRandomBech32Addr()
	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	msg := NewMsgSwitch(common.BTCAsset, cosmos.NewUint(10), acc, signer, tx)
	c.Assert(msg.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(msg.Address.Equals(acc), Equals, true)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	noID := tx
	noID.ID = ""
	cases := []struct {
		msg *MsgSwitch
		err error
	}{
		{NewMsgSwitch(common.EmptyAsset, cosmos.NewUint(10), acc, signer, tx), se.ErrUnknownRequest},
		{NewMsgSwitch(common.RuneNative, cosmos.NewUint(10), acc, signer, tx), se.ErrUnknownRequest},
		{NewMsgSwitch(common.BTCAsset, cosmos.ZeroUint(), acc, signer, tx), se.ErrUnknownRequest},
		{NewMsgSwitch(common.BTCAsset, cosmos.NewUint(10), cosmos.AccAddress{}, signer, tx), se.ErrInvalidAddress},
		{NewMsgSwitch(common.BTCAsset, cosmos.NewUint(10), acc, cosmos.AccAddress{}, tx), se.ErrInvalidAddress},
		{NewMsgSwitch(common.BTCAsset, cosmos.NewUint(10), acc, signer, noID), se.ErrUnknownRequest},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgMaint(c *C) {
	node := GetRandomBech32Addr()
	signer := GetRandomBech32Addr()
	msg := NewMsgMaint(node, signer)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "maint")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})
	bz := msg.GetSignBytes()
	c.Assert(len(bz) > 0, Equals, true)
	c.Assert(strings.Contains(string(bz), node.String()), Equals, true)
	c.Assert(strings.Contains(string(bz), signer.String()), Equals, true)

	c.Assert(errors.Is(NewMsgMaint(node, cosmos.AccAddress{}).ValidateBasic(), se.ErrInvalidAddress), Equals, true)
	c.Assert(errors.Is(NewMsgMaint(cosmos.AccAddress{}, signer).ValidateBasic(), se.ErrInvalidAddress), Equals, true)
}

func (s *MsgSimpleCovSuite) TestMsgSend(c *C) {
	from := GetRandomBech32Addr()
	to := GetRandomBech32Addr()
	amt := cosmos.NewCoins(cosmos.NewCoin("rune", sdkmath.NewInt(10)))
	msg := NewMsgSend(from, to, amt)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{from})

	// zero amounts are permitted (coin-level validity only)
	zero := cosmos.Coins{cosmos.Coin{Denom: "rune", Amount: sdkmath.ZeroInt()}}
	c.Assert(NewMsgSend(from, to, zero).ValidateBasic(), IsNil)

	neg := cosmos.Coins{cosmos.Coin{Denom: "rune", Amount: sdkmath.NewInt(-1)}}
	c.Assert(errors.Is(NewMsgSend(from, to, neg).ValidateBasic(), se.ErrInvalidCoins), Equals, true)
	c.Assert(errors.Is(NewMsgSend(cosmos.AccAddress{}, to, amt).ValidateBasic(), se.ErrInvalidAddress), Equals, true)
	c.Assert(errors.Is(NewMsgSend(from, cosmos.AccAddress{}, amt).ValidateBasic(), se.ErrInvalidAddress), Equals, true)
}

func (s *MsgSimpleCovSuite) TestMsgTCYStake(c *C) {
	signer := GetRandomBech32Addr()
	tcy := common.NewCoins(common.NewCoin(common.TCY, cosmos.NewUint(100)))
	msg := NewMsgTCYStake(thorTxCov(tcy), signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	btcTx := thorTxCov(tcy)
	btcTx.Chain = common.BTCChain
	cases := []struct {
		msg *MsgTCYStake
		err error
	}{
		{NewMsgTCYStake(btcTx, signer), se.ErrUnauthorized},
		{NewMsgTCYStake(thorTxCov(nil), signer), se.ErrInvalidCoins},
		{NewMsgTCYStake(thorTxCov(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(1))}), signer), se.ErrInvalidCoins},
		{NewMsgTCYStake(thorTxCov(tcy), cosmos.AccAddress{}), se.ErrInvalidAddress},
		{NewMsgTCYStake(thorTxCov(common.Coins{common.NewCoin(common.TCY, cosmos.ZeroUint())}), signer), se.ErrUnknownRequest},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgTCYUnstake(c *C) {
	signer := GetRandomBech32Addr()
	tx := thorTxCov(nil)
	msg := NewMsgTCYUnstake(tx, cosmos.NewUint(10000), signer)
	c.Assert(msg.BasisPoints.Uint64(), Equals, uint64(10000))
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	btcFrom := thorTxCov(nil)
	btcFrom.FromAddress = GetRandomBTCAddress()
	cases := []struct {
		msg *MsgTCYUnstake
		err error
	}{
		{NewMsgTCYUnstake(tx, cosmos.NewUint(1), cosmos.AccAddress{}), se.ErrInvalidAddress},
		{NewMsgTCYUnstake(tx, cosmos.ZeroUint(), signer), se.ErrUnknownRequest},
		{NewMsgTCYUnstake(tx, cosmos.NewUint(10001), signer), se.ErrUnknownRequest},
		{NewMsgTCYUnstake(btcFrom, cosmos.NewUint(1), signer), se.ErrInvalidAddress},
		{NewMsgTCYUnstake(thorTxCov(common.NewCoins(common.NewCoin(common.TCY, cosmos.NewUint(1)))), cosmos.NewUint(1), signer), se.ErrInvalidCoins},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgTCYClaim(c *C) {
	signer := GetRandomBech32Addr()
	runeAddr := GetRandomTHORAddress()
	l1 := GetRandomBTCAddress()
	msg := NewMsgTCYClaim(runeAddr, l1, signer)
	c.Assert(msg.RuneAddress, Equals, runeAddr)
	c.Assert(msg.L1Address, Equals, l1)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	for i, m := range []*MsgTCYClaim{
		NewMsgTCYClaim(runeAddr, l1, cosmos.AccAddress{}),
		NewMsgTCYClaim(common.NoAddress, l1, signer),
		NewMsgTCYClaim(runeAddr, common.NoAddress, signer),
	} {
		c.Check(errors.Is(m.ValidateBasic(), se.ErrInvalidAddress), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgSimpleCovSuite) TestMsgOperatorRotate(c *C) {
	signer := GetRandomBech32Addr()
	op := GetRandomBech32Addr()
	zero := common.NewCoin(common.RuneNative, cosmos.ZeroUint())
	msg := NewMsgOperatorRotate(signer, op, zero)
	c.Assert(msg.OperatorAddress.Equals(op), Equals, true)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	cases := []struct {
		msg   *MsgOperatorRotate
		match string
	}{
		{NewMsgOperatorRotate(cosmos.AccAddress{}, op, zero), ".*signer cannot be empty.*"},
		{NewMsgOperatorRotate(signer, cosmos.AccAddress{}, zero), ".*operator address cannot be empty.*"},
		{NewMsgOperatorRotate(signer, op, common.NewCoin(common.RuneNative, cosmos.OneUint())), ".*coin amount must be zero.*"},
	}
	for i, tc := range cases {
		c.Check(tc.msg.ValidateBasic(), ErrorMatches, tc.match, Commentf("case %d", i))
	}
}
