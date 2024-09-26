package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type HandlerLpRedeemSuite struct{}

var _ = Suite(&HandlerLpRedeemSuite{})

func (s *HandlerLpRedeemSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerLpRedeemSuite) TestLpRedeemHandler(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()

	// happy path
	mh := NewLpMintHandler(mgr)
	h := NewLpRedeemHandler(mgr)
	ah := NewAddLiquidityHandler(mgr)
	tx := common.NewTx(
		GetRandomTxHash(),
		runeAddr,
		ethAddr,
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*5))},
		common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
		"add:ETH",
	)
	addMsg := NewMsgAddLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(100*common.One),
		cosmos.ZeroUint(),
		runeAddr,
		ethAddr,
		common.NoAddress, cosmos.ZeroUint(),
		activeNodeAccount.NodeAddress)
	_, err = ah.Run(ctx, addMsg)
	c.Assert(err, IsNil)
	addMsg.RuneAmount = cosmos.ZeroUint()
	addMsg.AssetAmount = cosmos.NewUint(100 * common.One)
	_, err = ah.Run(ctx, addMsg)
	c.Assert(err, IsNil)

	accAddr := cosmos.AccAddress(runeAddr)
	tx = common.NewTx(GetRandomTxHash(), runeAddr, ethAddr,
		common.Coins{},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
		"lp-:ETH",
	)

	mintMsg := NewMsgLpMint(
		common.ETHAsset, runeAddr,
		cosmos.NewUint(25*common.One),
		accAddr, activeNodeAccount.NodeAddress,
		tx.ID,
	)

	_, err = mh.Run(ctx, mintMsg)
	c.Assert(err, IsNil)

	// Above setup from mint_test.go
	msg := NewMsgLpRedeem(common.ETHAsset, runeAddr,
		cosmos.NewUint(30*common.One),
		activeNodeAccount.NodeAddress,
		tx.ID,
	)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Above setup from mint_test.go
	msg = NewMsgLpRedeem(common.ETHAsset, runeAddr,
		cosmos.NewUint(20*common.One),
		activeNodeAccount.NodeAddress,
		tx.ID,
	)
	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil)

	pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	provider, err := mgr.Keeper().GetLiquidityProvider(ctx, common.ETHAsset, runeAddr)
	balance := mgr.coinKeeper.GetBalance(ctx, accAddr, "lp.eth.eth")

	c.Assert(err, IsNil)
	c.Assert(pool.LPUnits.String(), Equals, "9500000000")
	c.Assert(provider.Units.String(), Equals, "9500000000")
	c.Assert(balance.Amount.String(), Equals, "500000000")
}
