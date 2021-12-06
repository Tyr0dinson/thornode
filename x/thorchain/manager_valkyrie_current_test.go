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

	vaultNa := GetRandomVaultNode(NodeActive)
	vaultNa.Bond = cosmos.NewUint(common.One * 1000000)
	c.Assert(k.SetNodeAccount(ctx, vaultNa), IsNil)

	// Fund should return nil if node is not vault node
	validatorNa := GetRandomValidatorNode(NodeActive)
	validatorNa.Bond = cosmos.NewUint(common.One * 1000000)
	c.Assert(k.SetNodeAccount(ctx, validatorNa), IsNil)

	err := vmgr.Fund(ctx, mgr, constAccessor)
	c.Assert(err, IsNil)

	// Fund should create new vault if none exists
	err1 := vmgr.Fund(ctx, mgr, constAccessor)
	c.Assert(err1, IsNil)
	valkVault, err2 := vmgr.keeper.GetVault(ctx, vaultNa.PubKeySet.Secp256k1)
	c.Assert(err2, IsNil)

	c.Assert(valkVault.IsValkyrie(), Equals, true)
}

func (s ValkyrieManagerV74Suite) TestCalcAssetForValkyrieVault(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.BNBAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(5000*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	vmgr := newValkyrieMgrV74(k)

	// Set up pools
	var pools []Pool
	p := NewPool()
	p.Asset = common.BNBAsset
	p.BalanceRune = cosmos.NewUint(1000 * common.One)
	p.BalanceAsset = cosmos.NewUint(500 * common.One)
	pools = append(pools, p)

	p = NewPool()
	p.Asset = common.BTCAsset
	p.BalanceRune = cosmos.NewUint(10000 * common.One)
	p.BalanceAsset = cosmos.NewUint(225 * common.One)
	pools = append(pools, p)

	p = NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(5000 * common.One)
	p.BalanceAsset = cosmos.NewUint(225 * common.One)
	pools = append(pools, p)

	// BTC is most abundant in Asgard, choose BTC
	asset, err := vmgr.calcAssetForValkyrieVault(ctx, pools)
	c.Assert(err, IsNil)
	c.Assert(asset.Equals(common.BTCAsset), Equals, true)
}
