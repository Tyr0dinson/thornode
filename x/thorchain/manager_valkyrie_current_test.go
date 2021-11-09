package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

type ValkyrieManagerV74Suite struct{}

var _ = Suite(&ValkyrieManagerV74Suite{})

func (s *ValkyrieManagerV74Suite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s ValkyrieManagerV74Suite) TestFund(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.BNBAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	mgr := NewDummyMgr()

	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	vmgr := newValkyrieMgrV74(k)

	// Fund should return nil if node is not vault node
	validatorNa := GetRandomValidatorNode(NodeActive)
	validatorNa.Bond = cosmos.NewUint(common.One * 1000000)
	c.Assert(k.SetNodeAccount(ctx, validatorNa), IsNil)

	err := vmgr.Fund(ctx, mgr, constAccessor, validatorNa)
	c.Assert(err, IsNil)

	// Fund should create new vault if none exists
	vaultNa := GetRandomVaultNode(NodeActive)
	vaultNa.Bond = cosmos.NewUint(common.One * 1000000)
	c.Assert(k.SetNodeAccount(ctx, vaultNa), IsNil)

	err1 := vmgr.Fund(ctx, mgr, constAccessor, vaultNa)
	c.Assert(err1, IsNil)
	valkVault, err2 := vmgr.keeper.GetVault(ctx, vaultNa.PubKeySet.Secp256k1)
	c.Assert(err2, IsNil)

	c.Assert(valkVault.IsValkyrie(), Equals, true)

	// // setup 6 active nodes
	// for i := 0; i < 6; i++ {
	// 	na := GetRandomValidatorNode(NodeActive)
	// 	na.Bond = cosmos.NewUint(common.One * 1000000)
	// 	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	// }
	// ver := GetCurrentVersion()
	// constAccessor := constants.GetConstantValues(ver)
	// ymgr := newYggMgrV63(k)
	// err := ymgr.Fund(ctx, mgr, constAccessor)
	// c.Assert(err, IsNil)
	// na1 := GetRandomValidatorNode(NodeActive)
	// na1.Bond = cosmos.NewUint(1000000 * common.One)
	// c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	// bnbPool := NewPool()
	// bnbPool.Asset = common.BNBAsset
	// bnbPool.BalanceAsset = cosmos.NewUint(100000 * common.One)
	// bnbPool.BalanceRune = cosmos.NewUint(100000 * common.One)
	// c.Assert(k.SetPool(ctx, bnbPool), IsNil)
	// err1 := ymgr.Fund(ctx, mgr, constAccessor)
	// c.Assert(err1, IsNil)
	// items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	// c.Assert(err, IsNil)
	// c.Assert(items, HasLen, 1)
}
