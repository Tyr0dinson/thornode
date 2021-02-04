package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

type HandlerCompositeSuite struct{}

var _ = Suite(&HandlerCompositeSuite{})

func (s *HandlerCompositeSuite) TestMintHandle(c *C) {
	var err error
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.OutboundTransactionFee: 1000_000,
	}, map[constants.ConstantName]bool{}, map[constants.ConstantName]string{})

	pool := NewPool()
	pool.Asset = common.BNBAsset
	pool.BalanceRune = cosmos.NewUint(99321097137189)
	pool.BalanceAsset = cosmos.NewUint(7067913299069)
	k.SetPool(ctx, pool)
	btcbAsset, _ := common.NewAsset("BNB.BTCB-1DE")
	pool.Asset = btcbAsset
	pool.BalanceRune = cosmos.NewUint(266945012751528)
	pool.BalanceAsset = cosmos.NewUint(27894503029)
	k.SetPool(ctx, pool)
	usdtAsset, _ := common.NewAsset("BNB.USDT-6D8")
	pool.Asset = usdtAsset
	pool.BalanceRune = cosmos.NewUint(44922569882915)
	pool.BalanceAsset = cosmos.NewUint(172284758807476)
	k.SetPool(ctx, pool)

	comp := Composite{
		Id:   "idx-test",
		Name: "test",
		Type: DynamicComposite,
		Holdings: common.NewCoins(
			common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(400*common.One)),
			common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(300*common.One)),
			common.NewCoin(common.BNBAsset.GetSyntheticAsset(), cosmos.NewUint(100*common.One)),
		),
	}
	c.Assert(k.SetComposite(ctx, comp), IsNil)

	dummyMgr := NewDummyMgr()
	dummyMgr.gasMgr = NewGasMgrV1(constAccessor, k)
	handler := NewMintCompositeHandler(k, dummyMgr)

	// create account and mint assets
	addr := GetRandomBech32Addr()
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(common.RuneAsset(), cosmos.NewUint(200*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)

	// happy path
	coin := common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(200*common.One))
	msg := NewMsgMintComposite(coin, comp.Id, addr)
	_, err = handler.handle(ctx, *msg, constants.SWVersion, constAccessor)
	c.Assert(err, IsNil)

	// mising comp
	msg = NewMsgMintComposite(coin, "idx-not-found", addr)
	_, err = handler.handle(ctx, *msg, constants.SWVersion, constAccessor)
	c.Assert(err, NotNil)

	// wrong synthetic in the holdings
	coin = common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(200*common.One))
	msg = NewMsgMintComposite(coin, comp.Id, addr)
	_, err = handler.handle(ctx, *msg, constants.SWVersion, constAccessor)
	c.Assert(err, NotNil)

	// composite doesn't hold this synthetic
	coin = common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(200*common.One))
	msg = NewMsgMintComposite(coin, comp.Id, addr)
	_, err = handler.handle(ctx, *msg, constants.SWVersion, constAccessor)
	c.Assert(err, NotNil)
}

func (s *HandlerCompositeSuite) TestBurnHandle(c *C) {
	var err error
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.OutboundTransactionFee: 1000_000,
	}, map[constants.ConstantName]bool{}, map[constants.ConstantName]string{})

	pool := NewPool()
	pool.Asset = common.BNBAsset
	pool.BalanceRune = cosmos.NewUint(99321097137189)
	pool.BalanceAsset = cosmos.NewUint(7067913299069)
	k.SetPool(ctx, pool)
	btcbAsset, _ := common.NewAsset("BNB.BTCB-1DE")
	pool.Asset = btcbAsset
	pool.BalanceRune = cosmos.NewUint(266945012751528)
	pool.BalanceAsset = cosmos.NewUint(27894503029)
	k.SetPool(ctx, pool)
	usdtAsset, _ := common.NewAsset("BNB.USDT-6D8")
	pool.Asset = usdtAsset
	pool.BalanceRune = cosmos.NewUint(44922569882915)
	pool.BalanceAsset = cosmos.NewUint(172284758807476)
	k.SetPool(ctx, pool)

	comp := Composite{
		Id:   "idx-test",
		Name: "test",
		Type: DynamicComposite,
		Holdings: common.NewCoins(
			common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(400*common.One)),
			common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(300*common.One)),
			common.NewCoin(common.BNBAsset.GetSyntheticAsset(), cosmos.NewUint(100*common.One)),
		),
	}
	c.Assert(k.SetComposite(ctx, comp), IsNil)
	c.Assert(k.MintToModule(ctx, CompositeName, common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(4000*common.One))), IsNil)
	c.Assert(k.MintToModule(ctx, CompositeName, common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(300*common.One))), IsNil)
	c.Assert(k.MintToModule(ctx, CompositeName, common.NewCoin(common.BNBAsset.GetSyntheticAsset(), cosmos.NewUint(100*common.One))), IsNil)

	dummyMgr := NewDummyMgr()
	dummyMgr.gasMgr = NewGasMgrV1(constAccessor, k)
	dummyMgr.compMgr = NewCompMgrV1(k)
	handler := NewBurnCompositeHandler(k, dummyMgr)

	// create account and mint assets
	addr := GetRandomBech32Addr()
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(common.RuneAsset(), cosmos.NewUint(200*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(btcbAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(usdtAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(500*common.One))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, addr, common.NewCoin(comp.Asset(), cosmos.NewUint(500*common.One))), IsNil)

	// happy path
	coin := common.NewCoin(comp.Asset(), cosmos.NewUint(30*common.One))
	msg := NewMsgBurnComposite(coin, addr)
	_, err = handler.handle(ctx, *msg, constants.SWVersion, constAccessor)
	c.Assert(err, IsNil)

	c.Check(k.GetTotalSupply(ctx, comp.Asset()).Uint64(), Equals, uint64(470*common.One))
	balance := k.GetBalance(ctx, addr)
	target := cosmos.NewCoin(comp.Id, cosmos.NewInt(470*common.One))
	c.Check(balance[0].IsEqual(target), Equals, true)
}
