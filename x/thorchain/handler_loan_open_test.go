package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type HandlerLoanSuite struct{}

var _ = Suite(&HandlerLoanSuite{})

type MockTxOutDummy struct {
	TxOutStoreDummy
	blockOut *TxOut
}

func (tos *MockTxOutDummy) TryAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, minOut cosmos.Uint) (bool, error) {
	tos.addToBlockOut(ctx, toi)
	return true, nil
}

func (tos *MockTxOutDummy) addToBlockOut(_ cosmos.Context, toi TxOutItem) {
	tos.blockOut.TxArray = append(tos.blockOut.TxArray, toi)
}

func (tos *MockTxOutDummy) GetOutboundItems(ctx cosmos.Context) ([]TxOutItem, error) {
	return tos.blockOut.TxArray, nil
}

func (s *HandlerLoanSuite) TestLoanValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BNBAsset
	pool.BalanceAsset = cosmos.NewUint(83830778633)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	pool = NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(83830778633)
	pool.BalanceRune = cosmos.NewUint(1022440798362209)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	owner := GetRandomBNBAddress()

	handler := NewLoanOpenHandler(mgr)

	// happy path
	msg := NewMsgLoanOpen(owner, common.BNBAsset, cosmos.NewUint(100), GetRandomBTCAddress(), common.BTCAsset, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", cosmos.ZeroUint(), GetRandomBech32Addr())
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// not supported collateral asset
	msg = NewMsgLoanOpen(owner, common.RuneERC20Asset, cosmos.NewUint(100), GetRandomBTCAddress(), common.BTCAsset, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", cosmos.ZeroUint(), GetRandomBech32Addr())
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// target asset doesn't have a pool
	msg = NewMsgLoanOpen(owner, common.BNBAsset, cosmos.NewUint(100), GetRandomBTCAddress(), common.RuneERC20Asset, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", cosmos.ZeroUint(), GetRandomBech32Addr())
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerLoanSuite) TestLoanOpenHandleToBTC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(128)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(83830778633)
	pool.BalanceRune = cosmos.NewUint(1022440798362209)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// generate derived asset pool for btc
	pool.Asset = pool.Asset.GetDerivedAsset()
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	busd, err := common.NewAsset("BNB.BUSD-BD1")
	c.Assert(err, IsNil)
	busdPool := NewPool()
	busdPool.Asset = busd
	busdPool.Status = PoolAvailable
	busdPool.BalanceAsset = cosmos.NewUint(433267688964312)
	busdPool.BalanceRune = cosmos.NewUint(314031308608965)
	c.Assert(mgr.Keeper().SetPool(ctx, busdPool), IsNil)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-BNB.BUSD-BD1", 1) // enable BUSD pool as a TOR anchor
	mgr.Keeper().SetMimir(ctx, "DerivedDepthBasisPts", 10_000)

	vault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	owner, _ := common.NewAddress("bcrt1q8ln0p2d4mwng7x20nl7hku25d282sjgf2v74nt")

	handler := NewLoanOpenHandler(mgr)

	// happy path
	receiver, _ := common.NewAddress("bcrt1qdn665723epwlg8u2mk7rg4yp7n72mzwqzuv9ye")
	signer, _ := cosmos.AccAddressFromBech32("tthor1qxcgl07dm3vvewwxag7u0q7nq2uk984v60xpl0")
	msg := NewMsgLoanOpen(owner, common.BTCAsset, cosmos.NewUint(1e8), receiver, common.BTCAsset, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", cosmos.ZeroUint(), signer)
	c.Assert(handler.handle(ctx, *msg), IsNil)
	c.Assert(mgr.SwapQ().EndBlock(ctx, mgr), IsNil)

	loan, err := mgr.Keeper().GetLoan(ctx, common.BTCAsset, owner)
	c.Assert(err, IsNil)
	c.Check(loan.DebtUp.Uint64(), Equals, uint64(16649202600), Commentf("%d", loan.DebtUp.Uint64()))
	c.Check(loan.CollateralUp.Uint64(), Equals, uint64(99761992), Commentf("%d", loan.CollateralUp.Uint64()))
	c.Check(loan.LastOpenHeight, Equals, int64(128), Commentf("%d", loan.LastOpenHeight))

	hash, _ := common.NewTxID("29FC8D032CF17380AA1DC86F85A479CA9433E85887A9317C5D70D87EF56EAFAA")
	swap, err := mgr.Keeper().GetSwapQueueItem(ctx, hash, 0)
	c.Assert(err, IsNil)
	c.Check(swap.Tx.ID.Equals(hash), Equals, true)
	c.Check(swap.Destination.Equals(receiver), Equals, true)
	c.Check(swap.TargetAsset.Equals(common.BTCAsset), Equals, true)
	c.Check(swap.TradeTarget.IsZero(), Equals, true)
	c.Check(swap.AffiliateAddress.IsEmpty(), Equals, true)
	c.Check(swap.AffiliateBasisPoints.IsZero(), Equals, true)

	totalCollateral, err := mgr.Keeper().GetTotalCollateral(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(totalCollateral.Uint64(), Equals, uint64(99761992))
}

func (s *HandlerLoanSuite) TestLoanOpenHandleToTOR(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(128)
	mockTxOut := MockTxOutDummy{
		blockOut: NewTxOut(ctx.BlockHeight()),
	}
	mgr.txOutStore = &mockTxOut

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(83830778633)
	pool.BalanceRune = cosmos.NewUint(1022440798362209)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	busd, err := common.NewAsset("BNB.BUSD-BD1")
	c.Assert(err, IsNil)
	busdPool := NewPool()
	busdPool.Asset = busd
	busdPool.Status = PoolAvailable
	busdPool.BalanceAsset = cosmos.NewUint(433267688964312)
	busdPool.BalanceRune = cosmos.NewUint(314031308608965)
	c.Assert(mgr.Keeper().SetPool(ctx, busdPool), IsNil)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-BNB.BUSD-BD1", 1) // enable BUSD pool as a TOR anchor
	mgr.Keeper().SetMimir(ctx, "EnableDerivedAssets", 1)    // enable derived assets
	mgr.Keeper().SetMimir(ctx, "DerivedDepthBasisPts", 10_000)

	vault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	owner, _ := common.NewAddress("bcrt1q8ln0p2d4mwng7x20nl7hku25d282sjgf2v74nt")

	handler := NewLoanOpenHandler(mgr)

	// happy path
	receiver := GetRandomTHORAddress()
	signer, _ := cosmos.AccAddressFromBech32("tthor1qxcgl07dm3vvewwxag7u0q7nq2uk984v60xpl0")
	msg := NewMsgLoanOpen(owner, common.BTCAsset, cosmos.NewUint(1e8), receiver, common.TOR, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", cosmos.ZeroUint(), signer)
	c.Assert(handler.handle(ctx, *msg), IsNil)
	c.Assert(mgr.SwapQ().EndBlock(ctx, mgr), IsNil)

	loan, err := mgr.Keeper().GetLoan(ctx, common.BTCAsset, owner)
	c.Assert(err, IsNil)
	c.Check(loan.DebtUp.Uint64(), Equals, uint64(16649202600), Commentf("%d", loan.DebtUp.Uint64()))
	c.Check(loan.CollateralUp.Uint64(), Equals, uint64(99761992), Commentf("%d", loan.CollateralUp.Uint64()))
	c.Check(loan.LastOpenHeight, Equals, int64(128), Commentf("%d", loan.LastOpenHeight))

	outs, err := mgr.txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outs, HasLen, 2, Commentf("Len %d", len(outs)))
	c.Check(outs[0].Coin.Asset.String(), Equals, "THOR.BTC")
	c.Check(outs[0].Coin.Amount.Uint64(), Equals, uint64(99761992), Commentf("%d", outs[0].Coin.Amount.Uint64()))
	c.Check(outs[1].Coin.Asset.Equals(common.TOR), Equals, true)
	c.Check(outs[1].Coin.Amount.Uint64(), Equals, uint64(16649202600), Commentf("%d", outs[1].Coin.Amount.Uint64()))

	totalCollateral, err := mgr.Keeper().GetTotalCollateral(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(totalCollateral.Uint64(), Equals, uint64(99761992))
}
