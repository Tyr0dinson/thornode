package thorchain

import (
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	stypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

type CompositeManagerSuite struct{}

var _ = Suite(&CompositeManagerSuite{})

func MakeCodec() codec.ProtoCodecMarshaler {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(interfaceRegistry)
	stypes.RegisterInterfaces(interfaceRegistry)
	return codec.NewProtoCodec(interfaceRegistry)
}

type TestRebalanceKeeper struct {
	keeper.KVStoreDummy
	pools []Pool
}

func (k TestRebalanceKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	for _, pool := range k.pools {
		if pool.Asset.Equals(asset) {
			return pool, nil
		}
	}
	pool := NewPool()
	pool.Asset = asset
	return pool, nil
}

func (k TestRebalanceKeeper) MintToModule(ctx cosmos.Context, _ string, _ common.Coin) error {
	return nil
}

func (k TestRebalanceKeeper) BurnFromModule(ctx cosmos.Context, _ string, _ common.Coin) error {
	return nil
}

func (k TestRebalanceKeeper) GetPoolIterator(ctx cosmos.Context) cosmos.Iterator {
	iter := keeper.NewDummyIterator()
	cdc := MakeCodec()
	for _, pool := range k.pools {
		iter.AddItem([]byte(pool.Asset.String()), cdc.MustMarshalBinaryBare(&pool))
	}
	return iter
}

func (s CompositeManagerSuite) TestRebalance(c *C) {
	keeper := TestRebalanceKeeper{
		pools: []Pool{
			{
				BalanceRune:  cosmos.NewUint(100).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(100).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        common.BNBAsset,
			},
			{
				BalanceRune:  cosmos.NewUint(100).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(100).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        common.BTCAsset,
			},
			{
				BalanceRune:  cosmos.NewUint(100).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(100).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        common.ETHAsset,
			},
		},
	}
	ctx, _ := setupKeeperForTest(c)
	cmgr := NewCompMgrV1(keeper)

	// test that we rebalance uneven holdings to be even 5-5-5
	comp := Composite{
		Holdings: common.NewCoins(
			common.NewCoin(common.BNBAsset, cosmos.NewUint(7)),
			common.NewCoin(common.BTCAsset, cosmos.NewUint(5)),
			common.NewCoin(common.ETHAsset, cosmos.NewUint(3)),
		),
	}
	err := cmgr.Rebalance(ctx, []common.Asset{common.BNBAsset, common.BTCAsset, common.ETHAsset}, &comp)
	c.Assert(err, IsNil)
	c.Check(comp.Holdings.GetCoin(common.BNBAsset).Amount.Uint64(), Equals, uint64(5))
	c.Check(comp.Holdings.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(5))
	c.Check(comp.Holdings.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(5))

	// test that we get rid of a position and rebalance to the other coins correctly
	comp.Holdings = common.NewCoins(
		common.NewCoin(common.BNBAsset, cosmos.NewUint(20)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(4)),
	)
	err = cmgr.Rebalance(ctx, []common.Asset{common.BTCAsset, common.ETHAsset}, &comp)
	c.Assert(err, IsNil)
	c.Check(comp.Holdings.GetCoin(common.BNBAsset).Amount.Uint64(), Equals, uint64(0))
	c.Check(comp.Holdings.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(17))
	c.Check(comp.Holdings.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(17))

	// test that we can add a new position
	comp.Holdings = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(8)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(4)),
	)
	err = cmgr.Rebalance(ctx, []common.Asset{common.BNBAsset, common.BTCAsset, common.ETHAsset}, &comp)
	c.Assert(err, IsNil)
	c.Check(comp.Holdings.GetCoin(common.BNBAsset).Amount.Uint64(), Equals, uint64(4))
	c.Check(comp.Holdings.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(4))
	c.Check(comp.Holdings.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(4))
}

func (s CompositeManagerSuite) TestFindSimilarValuedAssets(c *C) {
	busdAsset, err := common.NewAsset("BNB.BUSD-BD1")
	c.Assert(err, IsNil)
	usdtAsset, err := common.NewAsset("BNB.USDT-6D8")
	c.Assert(err, IsNil)
	usdcAsset, err := common.NewAsset("ETH.USDC-0xabc")
	c.Assert(err, IsNil)
	keeper := TestRebalanceKeeper{
		pools: []Pool{
			{
				BalanceRune:  cosmos.NewUint(300).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(100).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        busdAsset,
			},
			{
				BalanceRune:  cosmos.NewUint(310).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(101).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        usdtAsset,
			},
			{
				BalanceRune:  cosmos.NewUint(300).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(100).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        usdcAsset,
			},
			{
				BalanceRune:  cosmos.NewUint(1000).MulUint64(common.One),
				BalanceAsset: cosmos.NewUint(78).MulUint64(common.One),
				Status:       PoolAvailable,
				Asset:        common.BNBAsset,
			},
		},
	}
	ctx, _ := setupKeeperForTest(c)
	cmgr := NewCompMgrV1(keeper)

	// test that we rebalance uneven holdings to be even 5-5-5
	comp := Composite{
		Name: "USD",
		Type: DynamicComposite,
	}

	assets, err := cmgr.findDynamicAssetsForUSDStableCoin(ctx, &comp)
	c.Assert(err, IsNil)
	c.Assert(assets, HasLen, 3)
	c.Check(assets[0].Equals(busdAsset), Equals, true)
	c.Check(assets[1].Equals(usdtAsset), Equals, true)
	c.Check(assets[2].Equals(usdcAsset), Equals, true)
}
