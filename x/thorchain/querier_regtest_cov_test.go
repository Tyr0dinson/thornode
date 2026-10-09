//go:build regtest
// +build regtest

package thorchain

import (
	"encoding/json"

	"github.com/blang/semver"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// QuerierRegtestCovSuite covers the regtest-only querier hooks
// (querier_regtest.go): queries load the managers and the export query
// returns the full genesis state.
type QuerierRegtestCovSuite struct{}

var _ = Suite(&QuerierRegtestCovSuite{})

func (s *QuerierRegtestCovSuite) TestInitManagerStoresVersion(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(5)
	// an older stored version is bumped to the lowest active version
	mgr.Keeper().SetVersionWithCtx(ctx, semver.MustParse("0.1.0"))
	initManager(ctx, mgr)
	v, ok := mgr.Keeper().GetVersionWithCtx(ctx)
	c.Assert(ok, Equals, true, Commentf("loading the managers records the active version"))
	c.Assert(v.Equals(mgr.Keeper().GetLowestActiveVersion(ctx)), Equals, true)
	c.Assert(mgr.GetVersion().Equals(v), Equals, true)
	c.Assert(v.GT(semver.MustParse("0.1.0")), Equals, true)
}

func (s *QuerierRegtestCovSuite) TestQueryExportReturnsGenesis(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(123 * common.One)
	pool.BalanceAsset = cosmos.NewUint(7 * common.One)
	pool.LPUnits = cosmos.NewUint(55)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.Keeper().SetMimir(ctx, "REGTESTCOVKEY", 9)

	bz, err := queryExport(ctx, mgr)
	c.Assert(err, IsNil)

	var raw map[string]json.RawMessage
	c.Assert(json.Unmarshal(bz, &raw), IsNil)
	c.Assert(raw["pools"], NotNil)

	var gs GenesisState
	c.Assert(json.Unmarshal(bz, &gs), IsNil)
	c.Assert(gs.Pools, HasLen, 1)
	c.Assert(gs.Pools[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(gs.Pools[0].BalanceRune.Uint64(), Equals, uint64(123*common.One))
	found := false
	for _, m := range gs.Mimirs {
		if m.Key == "REGTESTCOVKEY" {
			found = true
			c.Assert(m.Value, Equals, int64(9))
		}
	}
	c.Assert(found, Equals, true)
}
