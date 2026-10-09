//go:build stagenet
// +build stagenet

package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// MigrationsStagenetCovSuite exercises the stagenet-only store migrations
// (migrations_stagenet.go).
type MigrationsStagenetCovSuite struct{}

var _ = Suite(&MigrationsStagenetCovSuite{})

func (s *MigrationsStagenetCovSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *MigrationsStagenetCovSuite) TestNoopMigrations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	m := NewMigrator(mgr)
	poolsBefore, err := mgr.Keeper().GetPools(ctx)
	c.Assert(err, IsNil)
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	for _, fn := range []func(cosmos.Context) error{
		m.Migrate2to3, m.Migrate5to6, m.Migrate6to7, m.Migrate9to10,
		m.Migrate10to11, m.Migrate11to12, m.Migrate12to13, m.Migrate14to15,
		m.Migrate15to16, m.Migrate16to17, m.Migrate17to18,
	} {
		c.Assert(fn(ctx), IsNil)
	}
	poolsAfter, err := mgr.Keeper().GetPools(ctx)
	c.Assert(err, IsNil)
	c.Assert(poolsAfter, HasLen, len(poolsBefore))
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

func (s *MigrationsStagenetCovSuite) TestMigrate1to2DeletesStoreVersionKey(c *C) {
	ctx, mgr := setupManagerForTest(c)
	store := ctx.KVStore(keyThorchain)
	key := []byte("_ver//")
	store.Set(key, []byte("1"))
	c.Assert(NewMigrator(mgr).Migrate1to2(ctx), IsNil)
	c.Assert(store.Has(key), Equals, false)
}

func (s *MigrationsStagenetCovSuite) TestMigrate3to4AddsBaseToVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pk, err := common.NewPubKey("sthorpub1addwnpepqw354sg3dj8xffqjyznnmqh7rzfv528vr5wdsh3644485hzll6a2zf203v6")
	c.Assert(err, IsNil)

	// vault missing -> error
	c.Assert(NewMigrator(mgr).Migrate3to4(ctx), ErrorMatches, "fail to get vault.*")

	vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{common.ETHChain.String()}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(NewMigrator(mgr).Migrate3to4(ctx), IsNil)

	got, err := mgr.Keeper().GetVault(ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(got.Chains, DeepEquals, []string{common.ETHChain.String(), common.BASEChain.String()})
	c.Assert(got.Routers, HasLen, 1)
	c.Assert(got.Routers[0].Chain.Equals(common.BASEChain), Equals, true)
	c.Assert(got.Routers[0].Router.String(), Equals, "0xe36dcbf3c0284f756935811d9b9e80829d39bdc5")
}

// Migrate4to5 mints 210M TCY. NOTE: the claiming module is credited 208.2M and
// then pays the 1.8M treasury share out of it, so 1.8M TCY is left in the
// thorchain module and the claiming module ends with 206.4M (see report).
func (s *MigrationsStagenetCovSuite) TestMigrate4to5MintsTCY(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	mgr.Keeper().SetMimir(ctx, "HALTBNBCHAIN", 1)

	c.Assert(NewMigrator(mgr).Migrate4to5(ctx), IsNil)

	c.Assert(k.GetTotalSupply(ctx, common.TCY).Uint64(), Equals, uint64(210_000_000_00000000))
	c.Assert(k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native()).Uint64(), Equals, uint64(206_400_000_00000000))
	c.Assert(k.GetBalanceOfModule(ctx, ModuleName, common.TCY.Native()).Uint64(), Equals, uint64(1_800_000_00000000))
	treasury, err := cosmos.AccAddressFromBech32("sthor1hjpct8pd9d48vyqltaqunltwx9twm57l9e8tjr")
	c.Assert(err, IsNil)
	c.Assert(k.GetBalanceOf(ctx, treasury, common.TCY).Amount.Uint64(), Equals, uint64(1_800_000_00000000))

	// stagenet claim list was loaded
	iter := k.GetTCYClaimerIterator(ctx)
	c.Assert(iter.Valid(), Equals, true)
	iter.Close()

	// obsolete mimirs cleared
	v, err := k.GetMimir(ctx, "HALTBNBCHAIN")
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(-1))
}

func (s *MigrationsStagenetCovSuite) TestMigrate8to9(c *C) {
	ctx, mgr := setupManagerForTest(c)
	c.Assert(NewMigrator(mgr).Migrate8to9(ctx), IsNil)
	v, err := mgr.Keeper().GetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(25000000))
	v, err = mgr.Keeper().GetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(100))
}

func (s *MigrationsStagenetCovSuite) TestMigrate13to14BurnsReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// setupManagerForTest funds the Reserve with 100M RUNE
	c.Assert(NewMigrator(mgr).Migrate13to14(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(9_300_000_00000000))
	v, err := mgr.Keeper().GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset()).Uint64()))
}

var errStagenetCovInjected = errors.New("injected keeper failure")

// stagenetCovFaultKeeper fails the selected keeper calls.
type stagenetCovFaultKeeper struct {
	keeper.Keeper
	setVault, mint bool
	send           bool
	sendAcc        bool
}

func (k stagenetCovFaultKeeper) SetVault(ctx cosmos.Context, v Vault) error {
	if k.setVault {
		return errStagenetCovInjected
	}
	return k.Keeper.SetVault(ctx, v)
}

func (k stagenetCovFaultKeeper) MintToModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if k.mint {
		return errStagenetCovInjected
	}
	return k.Keeper.MintToModule(ctx, module, coin)
}

func (k stagenetCovFaultKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	if k.send {
		return errStagenetCovInjected
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

func (k stagenetCovFaultKeeper) SendFromModuleToAccount(ctx cosmos.Context, from string, to cosmos.AccAddress, coins common.Coins) error {
	if k.sendAcc {
		return errStagenetCovInjected
	}
	return k.Keeper.SendFromModuleToAccount(ctx, from, to, coins)
}

func (s *MigrationsStagenetCovSuite) TestMigrate3to4SetVaultFault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pk, err := common.NewPubKey("sthorpub1addwnpepqw354sg3dj8xffqjyznnmqh7rzfv528vr5wdsh3644485hzll6a2zf203v6")
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, NewVault(1, ActiveVault, AsgardVault, pk, nil, nil)), IsNil)
	mgr.K = stagenetCovFaultKeeper{Keeper: mgr.K, setVault: true}
	c.Assert(NewMigrator(mgr).Migrate3to4(ctx), ErrorMatches, "fail to set vault: injected keeper failure")
}

func (s *MigrationsStagenetCovSuite) TestMigrate4to5KeeperFaults(c *C) {
	for _, fk := range []stagenetCovFaultKeeper{{mint: true}, {send: true}, {sendAcc: true}} {
		ctx, mgr := setupManagerForTest(c)
		fk.Keeper = mgr.K
		mgr.K = fk
		c.Assert(NewMigrator(mgr).Migrate4to5(ctx), ErrorMatches, "injected keeper failure")
	}
}

// On stagenet the scheduled-migration hook is a no-op.
func (s *MigrationsStagenetCovSuite) TestScheduledMigrationStagenetIsNoop(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(42)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyScheduledMigration, 42)
	sm := newScheduledMigrationMgr(mgr)
	c.Assert(sm.EndBlock(ctx, mgr), IsNil)
	c.Assert(sm.processMigration(ctx, mgr), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(100_000_000*common.One))
}
