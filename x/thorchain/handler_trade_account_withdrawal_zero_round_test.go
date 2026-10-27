package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// zly (!5273): an outbound of a token with fewer than 8 decimals is rounded to the
// token's precision AFTER the last zero check, so an amount left after the outbound fee
// that is below one base unit (here 50, under 0.000001 USDC) is scheduled as a ZERO
// outbound. Bifrost cannot sign it and, on a durable vault, it holds the vault's later
// outbounds. Through the trade withdrawal handler: the withdrawal must fail before any
// accounting, leaving the trade balance and the outbound queue untouched.
func (s TxOutStoreSuite) TestSOLTokenTradeWithdrawalRoundingToZeroIsRejected(c *C) {
	f := newSOLTokenRentFixture(c)
	ctx, mgr := f.w.ctx, f.w.mgr
	f.vault(c, 1_000*common.One, 10*common.One)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)

	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, f.usdc, false)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, false)
	amount := fee.AddUint64(50) // 50 left after the fee: zero at 6 decimals
	c.Assert(cosmos.RoundToDecimal(amount.Sub(fee), 6).IsZero(), Equals, true)

	owner := GetRandomBech32Addr()
	txID, err := common.NewTxID("B" + strings.Repeat("0", 63))
	c.Assert(err, IsNil)
	tx := common.Tx{ID: txID}
	_, err = NewTradeAccountDepositHandler(mgr).Run(ctx, NewMsgTradeAccountDeposit(f.usdc, amount, owner, owner, tx))
	c.Assert(err, IsNil)
	c.Assert(mgr.TradeAccountManager().BalanceOf(ctx, f.usdc, owner).String(), Equals, amount.String())

	to, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	// Through the internal handler, as in production: its cache context discards the
	// handler's changes when it fails.
	_, err = NewInternalHandler(mgr)(ctx, NewMsgTradeAccountWithdrawal(f.usdc.GetTradeAsset(), amount, to, owner, tx))

	items, qErr := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(qErr, IsNil)
	for _, item := range items {
		c.Check(item.Coin.Amount.IsZero(), Equals, false, Commentf("zero outbound scheduled: %s", item.Coin))
	}
	c.Check(err, NotNil, Commentf("a withdrawal that rounds to nothing must fail"))
	c.Check(items, HasLen, 0)
	c.Check(mgr.TradeAccountManager().BalanceOf(ctx, f.usdc, owner).String(), Equals, amount.String(),
		Commentf("the trade balance must be left untouched"))
}

// The same rounding through prepareTxOutItem directly, for an outbound with no
// withdrawal behind it (a swap output): it must be dropped, not scheduled as zero.
func (s TxOutStoreSuite) TestSOLTokenOutboundRoundingToZeroIsDropped(c *C) {
	f := newSOLTokenRentFixture(c)
	f.vault(c, 1_000*common.One, 10*common.One)
	fee, err := f.w.mgr.GasMgr().GetAssetOutboundFee(f.w.ctx, f.usdc, false)
	c.Assert(err, IsNil)
	out := f.outbound(c)
	out.Coin = common.NewCoin(f.usdc, fee.AddUint64(50))

	outputs, _, err := f.store.prepareTxOutItem(f.w.ctx, out)
	c.Assert(err, IsNil)
	for _, o := range outputs {
		c.Check(o.Coin.Amount.IsZero(), Equals, false, Commentf("zero outbound scheduled: %s", o.Coin))
	}
	c.Check(outputs, HasLen, 0)
}
