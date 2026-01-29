package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type KeeperMempoolSwapUsageSuite struct{}

var _ = Suite(&KeeperMempoolSwapUsageSuite{})

func (s *KeeperMempoolSwapUsageSuite) TestGetMempoolSwapUsageTOR(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Should return zero when not set
	usage := k.GetMempoolSwapUsageTOR(ctx)
	c.Assert(usage.IsZero(), Equals, true)

	// Set and verify retrieval
	k.SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(1000))
	usage = k.GetMempoolSwapUsageTOR(ctx)
	c.Assert(usage.Uint64(), Equals, uint64(1000))

	// Update and verify
	k.SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(5000))
	usage = k.GetMempoolSwapUsageTOR(ctx)
	c.Assert(usage.Uint64(), Equals, uint64(5000))
}

func (s *KeeperMempoolSwapUsageSuite) TestAddMempoolSwapUsageTOR(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Add to empty storage
	newTotal := k.AddMempoolSwapUsageTOR(ctx, cosmos.NewUint(100))
	c.Assert(newTotal.Uint64(), Equals, uint64(100))
	c.Assert(k.GetMempoolSwapUsageTOR(ctx).Uint64(), Equals, uint64(100))

	// Add more
	newTotal = k.AddMempoolSwapUsageTOR(ctx, cosmos.NewUint(250))
	c.Assert(newTotal.Uint64(), Equals, uint64(350))
	c.Assert(k.GetMempoolSwapUsageTOR(ctx).Uint64(), Equals, uint64(350))

	// Add zero should not change
	newTotal = k.AddMempoolSwapUsageTOR(ctx, cosmos.ZeroUint())
	c.Assert(newTotal.Uint64(), Equals, uint64(350))
}

func (s *KeeperMempoolSwapUsageSuite) TestSubMempoolSwapUsageTOR(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set initial value
	k.SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(1000))

	// Subtract partial amount
	newTotal := k.SubMempoolSwapUsageTOR(ctx, cosmos.NewUint(300))
	c.Assert(newTotal.Uint64(), Equals, uint64(700))
	c.Assert(k.GetMempoolSwapUsageTOR(ctx).Uint64(), Equals, uint64(700))

	// Subtract more
	newTotal = k.SubMempoolSwapUsageTOR(ctx, cosmos.NewUint(200))
	c.Assert(newTotal.Uint64(), Equals, uint64(500))

	// Subtract zero should not change
	newTotal = k.SubMempoolSwapUsageTOR(ctx, cosmos.ZeroUint())
	c.Assert(newTotal.Uint64(), Equals, uint64(500))

	// Subtract entire amount
	newTotal = k.SubMempoolSwapUsageTOR(ctx, cosmos.NewUint(500))
	c.Assert(newTotal.IsZero(), Equals, true)
}

func (s *KeeperMempoolSwapUsageSuite) TestSubMempoolSwapUsageTORUnderflow(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set initial value
	k.SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(100))

	// Subtract more than available - should not underflow (SafeSub)
	newTotal := k.SubMempoolSwapUsageTOR(ctx, cosmos.NewUint(500))
	c.Assert(newTotal.IsZero(), Equals, true)
	c.Assert(k.GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	// Subtract from zero should stay zero
	newTotal = k.SubMempoolSwapUsageTOR(ctx, cosmos.NewUint(100))
	c.Assert(newTotal.IsZero(), Equals, true)
}
