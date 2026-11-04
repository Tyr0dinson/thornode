//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type MigrateGaiaReimburseSuite struct{}

var _ = Suite(&MigrateGaiaReimburseSuite{})

// setReserveRune sets the Reserve module's RUNE balance to exactly amt.
func setReserveRune(c *C, ctx cosmos.Context, k keeper.Keeper, amt uint64) {
	target := cosmos.NewUint(amt)
	bal := k.GetRuneBalanceOfModule(ctx, ReserveName)
	switch {
	case bal.GT(target):
		excess := common.NewCoins(common.NewCoin(common.RuneAsset(), bal.Sub(target)))
		c.Assert(k.SendFromModuleToAccount(ctx, ReserveName, GetRandomBech32Addr(), excess), IsNil)
	case bal.LT(target):
		FundModule(c, ctx, k, ReserveName, target.Sub(bal).Uint64())
	}
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(target), Equals, true)
}

func acctRune(ctx cosmos.Context, k keeper.Keeper, addr cosmos.AccAddress) uint64 {
	return k.GetBalance(ctx, addr).AmountOf(common.RuneAsset().Native()).Uint64()
}

// The reimbursement debits the Reserve and credits tcso by exactly the
// hardcoded amount, run only through its registered migration slot.
func (s *MigrateGaiaReimburseSuite) TestReimburseCreditsTreasury(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, gaiaReimburseRune*2)

	recipient, err := cosmos.AccAddressFromBech32(gaiaReimburseRecipient)
	c.Assert(err, IsNil)
	c.Assert(acctRune(ctx, k, recipient), Equals, uint64(0))

	run, count := gaiaReimburseSlotRun(mgr)
	c.Assert(count, Equals, 1)
	c.Assert(run, NotNil)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(run(ctx), IsNil)

	c.Check(reserveBefore.Sub(k.GetRuneBalanceOfModule(ctx, ReserveName)).Uint64(), Equals, gaiaReimburseRune)
	c.Check(acctRune(ctx, k, recipient), Equals, gaiaReimburseRune)
}

// Soft-skip when the Reserve cannot cover the amount: returns nil, moves nothing.
func (s *MigrateGaiaReimburseSuite) TestReimburseSoftSkipsReserveShort(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, gaiaReimburseRune-1)

	recipient, err := cosmos.AccAddressFromBech32(gaiaReimburseRecipient)
	c.Assert(err, IsNil)

	run, _ := gaiaReimburseSlotRun(mgr)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(run(ctx), IsNil)

	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
	c.Check(acctRune(ctx, k, recipient), Equals, uint64(0))
}

// Soft-skip on an unparseable recipient: returns nil, moves nothing.
func (s *MigrateGaiaReimburseSuite) TestReimburseSoftSkipsBadRecipient(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, gaiaReimburseRune*2)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reimburseGaiaVaultTopUp(ctx, k, "thor1notavalidaddress", gaiaReimburseRune, gaiaReimburseTopUpTx), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}
