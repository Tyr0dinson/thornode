//go:build !mainnet

package thorchain

import (
	. "gopkg.in/check.v1"
)

type Migrate20to21GaiaOffMainnetSuite struct{}

var _ = Suite(&Migrate20to21GaiaOffMainnetSuite{})

// Off mainnet Migrate20to21 is a no-op: migrate20to21Mainnet is a stub, so
// running the registered slot moves no Reserve RUNE even with the Reserve
// funded.
func (s *Migrate20to21GaiaOffMainnetSuite) TestMigrate20to21NoOpOffMainnet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	FundModule(c, ctx, k, ReserveName, 500_000_000_000)

	run, count := gaiaReimburseSlotRun(mgr)
	c.Assert(count, Equals, 1)
	c.Assert(run, NotNil)

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(run(ctx), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}
