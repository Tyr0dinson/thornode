//go:build chainnet
// +build chainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// MigrationsChainnetCovSuite exercises the chainnet-only store migrations
// (migrations_chainnet.go): every step but 13to14 is a no-op.
type MigrationsChainnetCovSuite struct{}

var _ = Suite(&MigrationsChainnetCovSuite{})

func (s *MigrationsChainnetCovSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *MigrationsChainnetCovSuite) TestNoopMigrations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	m := NewMigrator(mgr)
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	supplyBefore := mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())
	for _, fn := range []func(cosmos.Context) error{
		m.Migrate1to2, m.Migrate2to3, m.Migrate3to4, m.Migrate4to5, m.Migrate5to6,
		m.Migrate6to7, m.Migrate8to9, m.Migrate9to10, m.Migrate10to11,
		m.Migrate11to12, m.Migrate12to13, m.Migrate14to15, m.Migrate15to16,
		m.Migrate16to17, m.Migrate17to18,
	} {
		c.Assert(fn(ctx), IsNil)
	}
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
	c.Assert(mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset()).Equal(supplyBefore), Equals, true)
	v, err := mgr.Keeper().GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(-1))
}

func (s *MigrationsChainnetCovSuite) TestMigrate13to14BurnsReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// setupManagerForTest funds the Reserve with 100M RUNE
	c.Assert(NewMigrator(mgr).Migrate13to14(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(9_300_000_00000000))
	v, err := mgr.Keeper().GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset()).Uint64()))
}

func (s *MigrationsChainnetCovSuite) TestScheduledMigrationChainnetIsNoop(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(42)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyScheduledMigration, 42)
	sm := newScheduledMigrationMgr(mgr)
	c.Assert(sm.EndBlock(ctx, mgr), IsNil)
	c.Assert(sm.processMigration(ctx, mgr), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(100_000_000*common.One))
}
