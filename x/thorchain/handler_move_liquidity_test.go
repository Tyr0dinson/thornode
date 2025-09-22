package thorchain

import (
	"crypto/rand"
	"math/big"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type HandlerMoveLiquiditySuite struct{}

var _ = Suite(&HandlerMoveLiquiditySuite{})

func (s *HandlerMoveLiquiditySuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerMoveLiquiditySuite) TestMoveLiquidityHandlerDual(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.ETHChain.String())
	err = mgr.K.SetVault(ctx, vault)
	c.Assert(err, IsNil)

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.K.SetNodeAccount(ctx, nodeAccount), IsNil)

	FundModule(c, ctx, mgr.K, BondName, nodeAccount.Bond.Uint64())

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.Status = PoolAvailable
	err = mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	runeAddr1 := GetRandomRUNEAddress()
	runeAddr2 := GetRandomRUNEAddress()
	runeAddr3 := GetRandomRUNEAddress()

	ethAddr1 := GetRandomETHAddress()
	ethAddr2 := GetRandomETHAddress()
	ethAddr3 := GetRandomETHAddress()

	addHandler := NewAddLiquidityHandler(mgr)
	moveHandler := NewMoveLiquidityHandler(mgr)

	// add RUNE
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.NewUint(100*common.One+1), // add 1 for rounding tests
		cosmos.ZeroUint(),
		runeAddr1,
		ethAddr1,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	// add ETH
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(common.One),
		runeAddr1,
		ethAddr1,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	pool, err = mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.String(), Equals, "10000000001")
	c.Assert(pool.BalanceAsset.String(), Equals, "100000000")
	c.Assert(pool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(pool.PendingInboundRune.String(), Equals, "0")

	tx := GetRandomTx()
	tx.FromAddress = runeAddr1
	tx.Coins = common.Coins{}

	// Test 1: No addresses
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(5000),
		common.NoAddress,
		common.NoAddress,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 2: No RuneAddress
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(5000),
		common.NoAddress,
		ethAddr2,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 3: No AssetAddress
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(5000),
		runeAddr2,
		common.NoAddress,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 4: Move to address 2
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(9999),
		runeAddr2,
		ethAddr2,
		GetRandomBech32Addr(),
	))
	c.Assert(err, IsNil)

	// Test 5: Move to address 3
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(8000),
		runeAddr3,
		ethAddr3,
		GetRandomBech32Addr(),
	))
	c.Assert(err, IsNil)

	providers := getLiquidityProviders(ctx, mgr.K, common.ETHAsset)
	c.Assert(providers, HasLen, 3)

	for _, lp := range providers {
		switch lp.RuneAddress {
		case runeAddr1:
			c.Assert(lp.RuneDepositValue.String(), Equals, "200001")
			c.Assert(lp.AssetDepositValue.String(), Equals, "2000")
			c.Assert(lp.Units.String(), Equals, "200001")
		case runeAddr2:
			c.Assert(lp.RuneDepositValue.String(), Equals, "9999000000")
			c.Assert(lp.AssetDepositValue.String(), Equals, "99990000")
			c.Assert(lp.Units.String(), Equals, "9999000000")
		case runeAddr3:
			c.Assert(lp.RuneDepositValue.String(), Equals, "800000")
			c.Assert(lp.AssetDepositValue.String(), Equals, "8000")
			c.Assert(lp.Units.String(), Equals, "800000")
		}
	}

	// Test 6: Move 99.9% to address 3, keeps 0.00000002 Asset
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(9990),
		runeAddr3,
		ethAddr3,
		GetRandomBech32Addr(),
	))
	c.Assert(err, IsNil)

	lp1, err := mgr.K.GetLiquidityProvider(ctx, common.ETHAsset, runeAddr1)
	c.Assert(err, IsNil)
	c.Assert(lp1.RuneDepositValue.String(), Equals, "201")
	c.Assert(lp1.AssetDepositValue.String(), Equals, "2")
	c.Assert(lp1.Units.String(), Equals, "201")

	// Test 7: Move 50% to address 3, 0.00000001 Asset, tx is treated as 100%
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(5000),
		runeAddr3,
		ethAddr3,
		GetRandomBech32Addr(),
	))
	c.Assert(err, IsNil)

	c.Assert(getLiquidityProviders(ctx, mgr.K, common.ETHAsset), HasLen, 2)

	// Test 8: Move random amounts from 2 to 1, verify the total hasn't changed
	tx = GetRandomTx()
	tx.Coins = common.Coins{}
	tx.FromAddress = runeAddr2
	for i := 0; i < len(providers); i++ {
		amount, err := rand.Int(rand.Reader, big.NewInt(10_000))
		c.Assert(err, IsNil)

		_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
			tx,
			common.ETHAsset,
			cosmos.NewUintFromBigInt(amount),
			runeAddr1,
			ethAddr1,
			GetRandomBech32Addr(),
		))
		c.Assert(err, IsNil)
	}

	providers = getLiquidityProviders(ctx, mgr.K, common.ETHAsset)
	c.Assert(providers, HasLen, 3)

	checkLpVsPool(ctx, mgr, common.ETHAsset, c)

	// Single Sided

	runeAddr4 := GetRandomRUNEAddress()
	runeAddr5 := GetRandomRUNEAddress()

	ethAddr4 := GetRandomETHAddress()
	ethAddr5 := GetRandomETHAddress()

	// add RUNE (single sided)
	// ------------------------------------------------------------------------
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.NewUint(50*common.One),
		cosmos.ZeroUint(),
		runeAddr4,
		"",
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	tx.FromAddress = runeAddr4

	// Test 9: Move single to double sided => Fail
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(1000),
		runeAddr5,
		ethAddr5,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 10: Move single to switched single sided => Fail
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(1000),
		"",
		ethAddr5,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 11: Move single to single sided => OK
	for i := 0; i < 10; i++ {
		amount, err := rand.Int(rand.Reader, big.NewInt(10_000))
		c.Assert(err, IsNil)

		_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
			tx,
			common.ETHAsset,
			cosmos.NewUintFromBigInt(amount),
			runeAddr5,
			"",
			GetRandomBech32Addr(),
		))
		c.Assert(err, IsNil)
	}

	checkLpVsPool(ctx, mgr, common.ETHAsset, c)

	// add ETH (single sided)
	// ------------------------------------------------------------------------
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(common.One),
		"",
		ethAddr4,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	tx.FromAddress = ethAddr4

	// Test 12: Move single to double sided => Fail
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(1000),
		runeAddr5,
		ethAddr5,
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 13: Move single to switched single sided => Fail
	_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(1000),
		runeAddr5,
		"",
		GetRandomBech32Addr(),
	))
	c.Assert(err, NotNil)

	// Test 14: Move single to single sided => OK
	for i := 0; i < 10; i++ {
		amount, err := rand.Int(rand.Reader, big.NewInt(10_000))
		c.Assert(err, IsNil)

		_, err = moveHandler.Run(ctx, NewMsgMoveLiquidity(
			tx,
			common.ETHAsset,
			cosmos.NewUintFromBigInt(amount),
			"",
			ethAddr5,
			GetRandomBech32Addr(),
		))
		c.Assert(err, IsNil)
	}

	c.Assert(getLiquidityProviders(ctx, mgr.K, common.ETHAsset), HasLen, 7)

	checkLpVsPool(ctx, mgr, common.ETHAsset, c)
}

func checkLpVsPool(ctx cosmos.Context, mgr *Mgrs, asset common.Asset, c *C) {
	pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)

	totalRune := cosmos.ZeroUint()
	totalAsset := cosmos.ZeroUint()
	totalUnits := cosmos.ZeroUint()

	for _, lp := range getLiquidityProviders(ctx, mgr.K, common.ETHAsset) {
		if lp.IsSingleSided() {
			if lp.AssetAddress.IsEmpty() {
				totalRune = totalRune.Add(lp.RuneDepositValue.Mul(cosmos.NewUint(2)))
			}
			if lp.RuneAddress.IsEmpty() {
				totalAsset = totalAsset.Add(lp.AssetDepositValue.Mul(cosmos.NewUint(2)))
			}
		} else {
			totalRune = totalRune.Add(lp.RuneDepositValue)
			totalAsset = totalAsset.Add(lp.AssetDepositValue)
		}
		totalUnits = totalUnits.Add(lp.Units)
	}

	c.Assert(totalRune.Equal(pool.BalanceRune), Equals, true)
	c.Assert(totalAsset.Equal(pool.BalanceAsset), Equals, true)
	c.Assert(totalUnits.Equal(pool.LPUnits), Equals, true)
}
