//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type Migrate20to21GaiaSuite struct{}

var _ = Suite(&Migrate20to21GaiaSuite{})

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

// The reimbursement debits the Reserve and credits tcso by exactly the hardcoded
// amount, run only through its registered migration slot, and leaves the vault,
// the GAIA.ATOM pool and Asgard untouched — the write-down is gone because the
// top-up already restored the vault's backing.
func (s *Migrate20to21GaiaSuite) TestReimburseCreditsTcso(c *C) {
	const (
		vaultAtom = uint64(819_582_000_000)
		poolAsset = uint64(7_899_731_887_692)
		poolRune  = uint64(21_220_555_008_924)
	)

	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, (gaiaReimburseRune+tronRefundRune+ltcRefundRune)*2)

	vaultKey := GetRandomPubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, vaultKey, []string{common.GAIAChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(vaultAtom))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.ATOMAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(poolAsset)
	pool.BalanceRune = cosmos.NewUint(poolRune)
	pool.LPUnits = cosmos.NewUint(12_407_276_441_639)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	asgardBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)
	recipient, err := cosmos.AccAddressFromBech32(gaiaReimburseRecipient)
	c.Assert(err, IsNil)
	c.Assert(acctRune(ctx, k, recipient), Equals, uint64(0))

	run, count := gaiaReimburseSlotRun(mgr)
	c.Assert(count, Equals, 1)
	c.Assert(run, NotNil)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(run(ctx), IsNil)

	// Reserve debited and tcso credited by exactly the three hardcoded amounts.
	total := gaiaReimburseRune + tronRefundRune + ltcRefundRune
	c.Check(reserveBefore.Sub(k.GetRuneBalanceOfModule(ctx, ReserveName)).Uint64(), Equals, total)
	c.Check(acctRune(ctx, k, recipient), Equals, total)

	// Vault ATOM, the GAIA.ATOM pool and Asgard are untouched (no write-down).
	vaultAfter, err := k.GetVault(ctx, vaultKey)
	c.Assert(err, IsNil)
	c.Check(vaultAfter.GetCoin(common.ATOMAsset).Amount.Uint64(), Equals, vaultAtom, Commentf("vault moved"))
	poolAfter, err := k.GetPool(ctx, common.ATOMAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.Uint64(), Equals, poolAsset, Commentf("pool asset moved"))
	c.Check(poolAfter.BalanceRune.Uint64(), Equals, poolRune, Commentf("pool rune moved"))
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgardBefore), Equals, true, Commentf("asgard moved"))
	for _, e := range ctx.EventManager().Events() {
		c.Check(e.Type, Not(Equals), "pool_balance_change", Commentf("reimbursement emitted a pool event"))
	}
}

// Soft-skip when the Reserve cannot cover the amount: returns nil, moves nothing.
func (s *Migrate20to21GaiaSuite) TestReimburseSoftSkipsReserveShort(c *C) {
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

// reimburseTcso returns an error and moves nothing on an unparsable recipient,
// so the migration abandons the whole batch.
func (s *Migrate20to21GaiaSuite) TestReimburseTcsoErrorsOnBadRecipient(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, gaiaReimburseRune*2)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reimburseTcso(ctx, k, "thor1notavalidaddress", gaiaReimburseRune, gaiaReimburseTopUpTx, "GAIA vault solvency top-up"), NotNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

// All-or-none: if the Reserve covers the first two payments but not the third,
// the cached context is abandoned and tcso receives nothing.
func (s *Migrate20to21GaiaSuite) TestReimburseAtomicAllOrNone(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	// one short of the full total: enough for GAIA + TRON, not the LTC payment
	setReserveRune(c, ctx, k, gaiaReimburseRune+tronRefundRune+ltcRefundRune-1)

	recipient, err := cosmos.AccAddressFromBech32(gaiaReimburseRecipient)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	run, _ := gaiaReimburseSlotRun(mgr)
	c.Assert(run(ctx), IsNil)

	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true, Commentf("Reserve debited despite the batch aborting"))
	c.Check(acctRune(ctx, k, recipient), Equals, uint64(0), Commentf("tcso credited despite the batch aborting"))
}
