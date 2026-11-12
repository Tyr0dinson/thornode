package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type SecuredAssetMgrSuite struct{}

var _ = Suite(&SecuredAssetMgrSuite{})

func (s *SecuredAssetMgrSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *SecuredAssetMgrSuite) TestDepositAndWithdraw(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BTCAsset
	securedAsset := asset.GetSecuredAsset()

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	// addr3 := GetRandomBech32Addr()

	_, err = mgr.Deposit(ctx, securedAsset, cosmos.NewUint(100*common.One), addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, NotNil)

	mintAmt, err := mgr.Deposit(ctx, asset, cosmos.NewUint(100*common.One), addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "10000000000btc-btc")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, cosmos.NewUint(100*common.One).String())

	mintAmt, err = mgr.Deposit(ctx, asset, cosmos.NewUint(50*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "5000000000btc-btc")

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(50*common.One).String())
	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, cosmos.NewUint(100*common.One).String())

	// withdrawal
	_, err = mgr.Withdraw(ctx, asset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, NotNil)

	withdrawAmt, err := mgr.Withdraw(ctx, securedAsset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "3000000000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(20*common.One).String())
	withdrawAmt, err = mgr.Withdraw(ctx, securedAsset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "2000000000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(0).String())
}

func (s *SecuredAssetMgrSuite) TestRateDecrease(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BCHAsset
	securedAsset := asset.GetSecuredAsset()

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()

	depositAmount1 := cosmos.NewUint(100 * common.One)
	depositAmount2 := cosmos.NewUint(200 * common.One)
	dilutionAmount := cosmos.NewUint(100 * common.One)
	dilutedAmount1 := cosmos.NewUint(75 * common.One)
	dilutedAmount2 := cosmos.NewUint(150 * common.One)

	mintAmt, err := mgr.Deposit(ctx, asset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "10000000000bch-bch")

	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount2, addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "20000000000bch-bch")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())

	// Simulate fee application logic.
	// Pool share tokens are minted without any corresponding token deposits to increase the pool depth
	// Account SDK token balance should remain fixed, `mgr.BalanceOf` should return the reduced amount

	coin := common.NewCoin(securedAsset, dilutionAmount)
	err = mgr.keeper.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	// Total issued supply = 100 + 200 + 100 = 400
	// Deposited BTC = 300
	// Share ratio = 0.75
	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, dilutedAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, dilutedAmount2.String())

	// Fee removed, module burns its own share tokens
	err = mgr.keeper.BurnFromModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())
}

func (s *SecuredAssetMgrSuite) TestRateDecreaseDeposit(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BTCAsset
	securedAsset := asset.GetSecuredAsset()
	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	addr3 := GetRandomBech32Addr()

	depositAmount1 := cosmos.NewUint(1000)
	depositAmount2 := cosmos.NewUint(2000)
	dilutionAmount := cosmos.NewUint(1000)

	mintAmt, err := mgr.Deposit(ctx, asset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "1000btc-btc")

	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount2, addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "2000btc-btc")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())

	// Simulate fee application logic.
	// Pool share tokens are minted without any corresponding token deposits to increase the pool depth
	// Account SDK token balance should remain fixed, `mgr.BalanceOf` should return the reduced amount

	coin := common.NewCoin(securedAsset, dilutionAmount)
	err = mgr.keeper.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	// Ensure that new deposits have enough shares issued that new deposits are worth 1:1
	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount1, addr3, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	// Issuance after a dilution will be greater than the deposit amount
	c.Check(mintAmt.String(), Equals, "1333btc-btc")
	bal = mgr.BalanceOf(ctx, asset, addr3)
	c.Check(bal.String(), Equals, depositAmount1.String())

	// Finally, check that Withdrawal logic allocates the right amount of the pool for given withdraw amount
	withdrawAmt, err := mgr.Withdraw(ctx, securedAsset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "750 BTC.BTC")

	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, "0")

	withdrawAmt, err = mgr.Withdraw(ctx, securedAsset, depositAmount1, addr3, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "1000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr3)
	c.Check(bal.String(), Equals, "0")
}

func (s *SecuredAssetMgrSuite) TestWithdrawDuringPoolRagnarok(c *C) {
	s.checkWithdrawDuringPoolRagnarok(c, common.BTCAsset, GetRandomBTCAddress)
	s.checkWithdrawDuringPoolRagnarok(c, common.ETHAsset, GetRandomETHAddress)
}

func (s *SecuredAssetMgrSuite) checkWithdrawDuringPoolRagnarok(c *C, asset common.Asset, newAddress func() common.Address) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	ctx = ctx.WithBlockHeight(1000 * k.GetConfigInt64(ctx, constants.FundMigrationInterval))

	securedAsset := asset.GetSecuredAsset()
	synthAsset := asset.GetSyntheticAsset()

	c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = ctx.BlockHeight() - 10
	vault.Coins = common.Coins{common.NewCoin(asset, cosmos.NewUint(500*common.One))}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	c.Assert(k.SaveNetworkFee(ctx, asset.GetChain(), NetworkFee{
		Chain: asset.GetChain(), TransactionSize: 80000, TransactionFeeRate: 30,
	}), IsNil)

	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)
	FundModule(c, ctx, k, AsgardName, pool.BalanceRune.Uint64())
	c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(synthAsset, cosmos.NewUint(common.One))), IsNil)

	// two providers, one handled per pass: the first pass cannot finalize
	k.SetMimir(ctx, constants.RagnarokProcessNumOfLPPerIteration.String(), 1)
	for i := 0; i < 2; i++ {
		k.SetLiquidityProvider(ctx, LiquidityProvider{
			Asset:             asset,
			RuneAddress:       GetRandomRUNEAddress(),
			AssetAddress:      newAddress(),
			LastAddHeight:     5,
			Units:             pool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		})
	}

	owner := GetRandomBech32Addr()
	deposited := cosmos.NewUint(5 * common.One)
	_, err := mgr.SecuredAssetManager().Deposit(ctx, asset, deposited, owner, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Assert(mgr.SecuredAssetManager().BalanceOf(ctx, asset, owner).String(), Equals, deposited.String())
	c.Assert(k.GetTotalSupply(ctx, synthAsset).IsZero(), Equals, false)

	// start the pool Ragnarok: redeeming the synths sets the marker
	k.SetMimir(ctx, "RAGNAROK-"+asset.MimirString(), 1)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.checkPoolRagnarok(ctx, mgr), IsNil)
	ragStart, err := k.GetPoolRagnarokStart(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(ragStart, Equals, ctx.BlockHeight())
	pool, err = k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(pool.Status, Equals, PoolStaged)
	c.Assert(k.GetTotalSupply(ctx, synthAsset).IsZero(), Equals, true)

	// the secured shares still count in full
	c.Check(mgr.SecuredAssetManager().GetShareSupply(ctx, securedAsset).String(), Equals, deposited.String())
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, asset, owner).String(), Equals, deposited.String())

	// the manager, as add liquidity and the stable reserve call it
	one := cosmos.NewUint(common.One)
	to := newAddress()
	coin, err := mgr.SecuredAssetManager().Withdraw(ctx, securedAsset, one, owner, to, common.BlankTxID)
	c.Check(err, IsNil)
	c.Check(coin.String(), Equals, common.NewCoin(asset, one).String())

	// the native deposit a holder sends to withdraw to layer 1
	withdrawn := cosmos.NewUint(3 * common.One)
	msg := NewMsgDeposit(common.NewCoins(common.NewCoin(securedAsset, withdrawn)), fmt.Sprintf("SECURE-:%s", to), owner)
	_, err = NewDepositHandler(mgr).Run(ctx, msg)
	c.Assert(err, IsNil)

	remaining := deposited.Sub(one).Sub(withdrawn)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, asset, owner).String(), Equals, remaining.String())
	c.Check(k.GetBalanceOf(ctx, owner, securedAsset).Amount.String(), Equals, remaining.String())
	state, err := k.GetSecuredAsset(ctx, securedAsset)
	c.Assert(err, IsNil)
	c.Check(state.Depth.String(), Equals, remaining.String())

	// one outbound pays the holder, less the outbound fee
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	var paid common.Coins
	for _, item := range items {
		if item.ToAddress.Equals(to) {
			paid = append(paid, item.Coin)
		}
	}
	c.Assert(paid, HasLen, 1)
	c.Check(paid[0].Asset.Equals(asset), Equals, true)
	c.Check(paid[0].Amount.IsZero(), Equals, false)
	c.Check(paid[0].Amount.LTE(withdrawn), Equals, true, Commentf("%s", paid[0]))

	// Cancelling Ragnarok does not delete its persisted marker. A fresh
	// secured manager must still read the remaining claims from the same store.
	k.SetMimir(ctx, "RAGNAROK-"+asset.MimirString(), 0)
	c.Assert(networkMgr.checkPoolRagnarok(ctx, mgr), IsNil)
	ragStart, err = k.GetPoolRagnarokStart(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(ragStart, Equals, ctx.BlockHeight())
	reloaded := newSecuredAssetMgr(k, mgr.EventMgr())
	c.Check(reloaded.GetShareSupply(ctx, securedAsset).String(), Equals, remaining.String())
	c.Check(reloaded.BalanceOf(ctx, asset, owner).String(), Equals, remaining.String())
}

func (s *SecuredAssetMgrSuite) TestPoolRagnarokKeepsSecuredSharePrice(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		ctx, k := setupKeeperForTest(c)
		ctx = ctx.WithBlockHeight(100)
		eventMgr, err := GetEventManager(GetCurrentVersion())
		c.Assert(err, IsNil)
		mgr := newSecuredAssetMgr(k, eventMgr)
		secured := asset.GetSecuredAsset()
		owner := GetRandomBech32Addr()
		other := GetRandomBech32Addr()
		incoming := GetRandomBech32Addr()
		_, err = mgr.Deposit(ctx, asset, cosmos.NewUint(100), owner, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		_, err = mgr.Deposit(ctx, asset, cosmos.NewUint(200), other, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		// Fee shares make the price 300 underlying units / 400 shares.
		c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(secured, cosmos.NewUint(100))), IsNil)
		c.Assert(mgr.BalanceOf(ctx, asset, owner).String(), Equals, "75")
		c.Assert(mgr.BalanceOf(ctx, asset, other).String(), Equals, "150")
		k.SetPoolRagnarokStart(ctx, asset)

		// Already-custodied payouts can credit secured assets during Ragnarok.
		// A 75-unit credit must mint 100 shares at the existing price.
		minted, err := mgr.Deposit(ctx, asset, cosmos.NewUint(75), incoming, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		c.Check(minted.Amount.String(), Equals, "100", Commentf("asset: %s", asset))
		c.Check(mgr.GetShareSupply(ctx, secured).String(), Equals, "500")
		c.Check(mgr.BalanceOf(ctx, asset, owner).String(), Equals, "75")
		c.Check(mgr.BalanceOf(ctx, asset, other).String(), Equals, "150")
		c.Check(mgr.BalanceOf(ctx, asset, incoming).String(), Equals, "75")

		withdrawn, err := mgr.Withdraw(ctx, secured, cosmos.NewUint(30), incoming, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		c.Check(withdrawn.Asset.Equals(asset), Equals, true)
		c.Check(withdrawn.Amount.String(), Equals, "30")
		c.Check(k.GetBalanceOf(ctx, incoming, secured).Amount.String(), Equals, "60")
		c.Check(mgr.BalanceOf(ctx, asset, incoming).String(), Equals, "45")
		c.Check(mgr.BalanceOf(ctx, asset, owner).String(), Equals, "75")
		c.Check(mgr.BalanceOf(ctx, asset, other).String(), Equals, "150")
		pool, supply, err := mgr.GetSecuredAssetStatus(ctx, secured)
		c.Assert(err, IsNil)
		c.Check(pool.Depth.String(), Equals, "345")
		c.Check(supply.String(), Equals, "460")
	}
}
