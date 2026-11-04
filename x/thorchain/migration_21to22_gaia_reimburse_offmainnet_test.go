//go:build !mainnet

package thorchain

import (
	. "gopkg.in/check.v1"
)

type GaiaReimburseOffMainnetSuite struct{}

var _ = Suite(&GaiaReimburseOffMainnetSuite{})

// Off mainnet the reimbursement migration is a no-op: migrate21to22Mainnet is a
// stub, so running the registered slot moves no Reserve RUNE even with the
// Reserve funded well above any plausible amount.
func (s *GaiaReimburseOffMainnetSuite) TestReimburseNoOpOffMainnet(c *C) {
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
