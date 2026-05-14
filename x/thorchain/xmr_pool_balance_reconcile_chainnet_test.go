//go:build chainnet
// +build chainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type XMRPoolBalanceReconcileSuite struct{}

var _ = Suite(&XMRPoolBalanceReconcileSuite{})

func (s *XMRPoolBalanceReconcileSuite) TestChainnetXMRPoolBalanceReconcile(c *C) {
	ctx, mgr := setupManagerForTest(c)

	activeA := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil)
	activeA.PubKeyFrost = common.PubKey("cthorpub1zcjduepqh5p72qfjl9rv4twt93lqlsddx4gp8pe2jl9mq033qsf76n98p25szzc9z2")
	activeA.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100)))
	c.Assert(mgr.Keeper().SetVault(ctx, activeA), IsNil)

	activeB := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil)
	activeB.PubKeyFrost = common.PubKey("cthorpub1zcjduepq5yf9tavdaru7rs5aaypppgn8hl3vflrw7nsutullerlmjx6vngasv8vhw3")
	activeB.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(200)))
	c.Assert(mgr.Keeper().SetVault(ctx, activeB), IsNil)

	inactive := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil)
	inactive.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(999)))
	c.Assert(mgr.Keeper().SetVault(ctx, inactive), IsNil)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.BalanceAsset = cosmos.NewUint(1)
	pool.BalanceRune = cosmos.NewUint(50)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	stats, err := reconcileChainnetXMRPoolBalance(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(stats.changed, Equals, true)
	c.Assert(stats.activeVaults, Equals, 2)
	c.Assert(stats.vaultChanges, Equals, 2)
	c.Assert(stats.oldPool.Equal(cosmos.NewUint(1)), Equals, true)
	c.Assert(stats.newPool.Equal(cosmos.NewUint(315164242)), Equals, true)

	updated, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Assert(updated.BalanceAsset.Equal(cosmos.NewUint(315164242)), Equals, true)
	c.Assert(updated.BalanceRune.Equal(cosmos.NewUint(50)), Equals, true)
	updatedVaultA, err := mgr.Keeper().GetVault(ctx, activeA.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVaultA.GetCoin(common.XMRAsset).Amount.Equal(cosmos.NewUint(159549601)), Equals, true)
	updatedVaultB, err := mgr.Keeper().GetVault(ctx, activeB.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVaultB.GetCoin(common.XMRAsset).Amount.Equal(cosmos.NewUint(155614641)), Equals, true)

	err = runChainnetXMRPoolBalanceReconcileOnce(ctx, mgr)
	c.Assert(err, IsNil)
	doneAt, err := mgr.Keeper().GetMimir(ctx, xmrPoolBalanceReconcileMimirKey)
	c.Assert(err, IsNil)
	c.Assert(doneAt, Equals, ctx.BlockHeight())

	updated.BalanceAsset = cosmos.NewUint(123)
	c.Assert(mgr.Keeper().SetPool(ctx, updated), IsNil)
	err = runChainnetXMRPoolBalanceReconcileOnce(ctx, mgr)
	c.Assert(err, IsNil)

	unchanged, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Assert(unchanged.BalanceAsset.Equal(cosmos.NewUint(123)), Equals, true)
}
