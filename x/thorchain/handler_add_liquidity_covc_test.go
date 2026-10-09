package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerAddLiquidityCovCESuite struct{}

var _ = Suite(&HandlerAddLiquidityCovCESuite{})

func covCESetPool(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, status PoolStatus, rune, bal uint64) {
	pool := NewPool()
	pool.Asset = asset
	pool.Status = status
	pool.BalanceRune = cosmos.NewUint(rune)
	pool.BalanceAsset = cosmos.NewUint(bal)
	pool.LPUnits = cosmos.NewUint(rune)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
}

func covCEAddLiqSetup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	covCESetPool(c, ctx, mgr, common.BTCAsset, PoolAvailable, 1000*common.One, 10*common.One)
	covCESetPool(c, ctx, mgr, common.ETHAsset, PoolAvailable, 1000*common.One, 100*common.One)
	return ctx, mgr
}

// covCEInternalAdd builds an add liquidity msg with a blank tx id (internal),
// which skips ValidateBasic so the stateful checks can be reached directly.
func covCEInternalAdd(asset common.Asset, runeAmt, assetAmt uint64, runeAddr, assetAddr common.Address, coin common.Coin, signer cosmos.AccAddress) MsgAddLiquidity {
	tx := common.Tx{ID: common.BlankTxID, Chain: coin.Asset.GetChain(), Coins: common.Coins{coin}, FromAddress: assetAddr}
	return *NewMsgAddLiquidity(tx, asset, cosmos.NewUint(runeAmt), cosmos.NewUint(assetAmt), runeAddr, assetAddr, common.NoAddress, cosmos.ZeroUint(), signer)
}

func (s *HandlerAddLiquidityCovCESuite) TestValidateCovCE(c *C) {
	ctx, mgr := covCEAddLiqSetup(c)
	h := NewAddLiquidityHandler(mgr)
	signer := GetRandomBech32Addr()
	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()
	btcCoin := common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))
	runeCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	type tc struct {
		name string
		msg  MsgAddLiquidity
		err  string
	}
	derived := common.BTCAsset.GetDerivedAsset()
	usdcSynth := common.Asset{Chain: common.ETHChain, Symbol: "USDC-0X123", Ticker: "USDC", Synth: true}
	signerRune := common.Address(signer.String())
	cases := []tc{
		{"trade", covCEInternalAdd(common.BTCAsset.GetTradeAsset(), 0, 1, runeAddr, btcAddr, btcCoin, signer), "asset cannot be a trade asset"},
		// derived assets live on THORChain, whose gas asset pool never exists
		{"derived", covCEInternalAdd(derived, 1, 0, runeAddr, common.NoAddress, runeCoin, signer), ".*gas asset pool \\(THOR.RUNE\\) does not exist yet"},
		{"non gas synth", covCEInternalAdd(usdcSynth, 0, 1, common.NoAddress, GetRandomETHAddress(), runeCoin, signer), "asset must be a gas asset or TOR anchor pool"},
		{"secured signer", covCEInternalAdd(common.BTCAsset.GetSecuredAsset(), 0, 1, runeAddr, runeAddr, runeCoin, signer), "signer .* not authorized to withdraw secured assets.*"},
		{"secured asset addr", covCEInternalAdd(common.BTCAsset.GetSecuredAsset(), 0, 1, signerRune, btcAddr, runeCoin, signer), errAddLiquidityMismatchAddr.Error()},
		{"cross chain asset addr", covCEInternalAdd(common.BTCAsset, 0, 1, runeAddr, GetRandomETHAddress(), btcCoin, signer), errAddLiquidityMismatchAddr.Error()},
		{"swap non synth", covCEInternalAdd(common.ETHAsset, 0, 1, runeAddr, GetRandomETHAddress(), btcCoin, signer), "swap & add liquidity is only available for synthetic pools"},
		{"swap wrong l1", covCEInternalAdd(common.BTCAsset.GetSyntheticAsset(), 0, 1, common.NoAddress, btcAddr, common.NewCoin(common.ETHAsset, cosmos.NewUint(1)), signer), "deposit asset must be the layer1 equivalent for the synthetic asset"},
	}
	for _, t := range cases {
		c.Check(h.validate(ctx, t.msg), ErrorMatches, t.err, Commentf("case %s", t.name))
	}

	ok := covCEInternalAdd(common.BTCAsset, 0, 1, runeAddr, btcAddr, btcCoin, signer)
	c.Assert(h.validate(ctx, ok), IsNil)

	mgr.Keeper().SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)
	c.Check(h.validate(ctx, ok), ErrorMatches, "cannot add liquidity to Ragnaroked pool.*")
	mgr.Keeper().SetMimir(ctx, "RAGNAROK-BTC-BTC", 0)

	mgr.Keeper().SetMimir(ctx, "PauseLPBTC", 1)
	c.Check(h.validate(ctx, ok), ErrorMatches, "unable to add liquidity while chain has paused LP actions")
	mgr.Keeper().SetMimir(ctx, "PauseLPBTC", 0)

	mgr.Keeper().SetMimir(ctx, "PauseLPDeposit-BTC-BTC", 1)
	c.Check(h.validate(ctx, ok), ErrorMatches, "unable to add liquidity, deposits are paused.*")
}

func (s *HandlerAddLiquidityCovCESuite) TestAffiliateAddCovCE(c *C) {
	ctx, mgr := covCEAddLiqSetup(c)
	h := NewAddLiquidityHandler(mgr)
	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()

	for _, aff := range []common.Address{GetRandomRUNEAddress(), GetRandomBTCAddress()} {
		tx := GetRandomTx()
		tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
		msg := NewMsgAddLiquidity(tx, common.BTCAsset, cosmos.NewUint(100*common.One), cosmos.NewUint(common.One),
			runeAddr, btcAddr, aff, cosmos.NewUint(1000), GetRandomBech32Addr())
		c.Assert(h.handle(ctx, *msg), IsNil)

		affLP, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, aff)
		c.Assert(err, IsNil)
		c.Check(affLP.Units.IsZero(), Equals, false, Commentf("affiliate %s", aff))
	}
	userLP, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, runeAddr)
	c.Assert(err, IsNil)
	c.Check(userLP.Units.IsZero(), Equals, false)
}

func (s *HandlerAddLiquidityCovCESuite) TestSwapPathCovCE(c *C) {
	ctx, mgr := covCEAddLiqSetup(c)
	h := NewAddLiquidityHandler(mgr)
	btcAddr := GetRandomBTCAddress()

	tx := GetRandomTx()
	tx.Chain = common.BTCChain
	tx.FromAddress = btcAddr
	tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	synth := common.BTCAsset.GetSyntheticAsset()
	msg := NewMsgAddLiquidity(tx, synth, cosmos.ZeroUint(), cosmos.NewUint(common.One), common.NoAddress, btcAddr, common.NoAddress, cosmos.ZeroUint(), GetRandomBech32Addr())

	// illegal separator in the affiliate address
	bad := *msg
	bad.AffiliateAddress = common.Address("thor1:bad")
	c.Check(h.swap(ctx, bad), ErrorMatches, "illegal character")

	c.Assert(h.handle(ctx, *msg), IsNil)
	queued, err := mgr.Keeper().GetSwapQueueItem(ctx, tx.ID, 0)
	c.Assert(err, IsNil)
	c.Check(queued.TargetAsset.Equals(synth), Equals, true)
	c.Check(queued.Tx.Memo, Equals, "+:BTC/BTC:::0")

	// the same tx hash cannot be queued twice
	c.Check(h.handle(ctx, *msg), ErrorMatches, "txn hash conflict")
}

func (s *HandlerAddLiquidityCovCESuite) TestUnitMathCovCE(c *C) {
	// vault units with an empty pool but existing units resets to the deposit
	p, l := calculateVaultUnits(cosmos.NewUint(100), cosmos.ZeroUint(), cosmos.NewUint(7))
	c.Check(p.Uint64(), Equals, uint64(7))
	c.Check(l.Uint64(), Equals, uint64(7))
	// zero deposit leaves units unchanged
	p, l = calculateVaultUnits(cosmos.NewUint(100), cosmos.NewUint(50), cosmos.ZeroUint())
	c.Check(p.Uint64(), Equals, uint64(100))
	c.Check(l.IsZero(), Equals, true)
	// proportional share
	p, l = calculateVaultUnits(cosmos.NewUint(100), cosmos.NewUint(50), cosmos.NewUint(50))
	c.Check(p.Uint64(), Equals, uint64(200))
	c.Check(l.Uint64(), Equals, uint64(100))

	// asset-only first deposit into a pool with no asset balance uses the asset amount
	p, l, err := calculatePoolUnits(cosmos.ZeroUint(), cosmos.NewUint(10), cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.NewUint(33))
	c.Assert(err, IsNil)
	c.Check(p.Uint64(), Equals, uint64(33))
	c.Check(l.Uint64(), Equals, uint64(33))
}

func (s *HandlerAddLiquidityCovCESuite) TestTotalLiquidityRuneCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	covCESetPool(c, ctx, mgr, common.BTCAsset, PoolAvailable, 100, 1)
	covCESetPool(c, ctx, mgr, common.ETHAsset, PoolSuspended, 1000, 1)
	covCESetPool(c, ctx, mgr, common.BTCAsset.GetSyntheticAsset(), PoolAvailable, 10000, 1)
	covCESetPool(c, ctx, mgr, common.BTCAsset.GetDerivedAsset(), PoolAvailable, 100000, 1)
	h := NewAddLiquidityHandler(mgr)
	total, err := h.getTotalLiquidityRUNE(ctx)
	c.Assert(err, IsNil)
	// only the available L1 pool counts
	c.Check(total.Uint64(), Equals, uint64(100))

	mgr.K = newCovCEFailKeeper(mgr.K, "GetPools")
	_, err = NewAddLiquidityHandler(mgr).getTotalLiquidityRUNE(ctx)
	c.Check(err, ErrorMatches, "fail to get pools from data store: kaboom")
}

func (s *HandlerAddLiquidityCovCESuite) TestAddLiquidityFailuresCovCE(c *C) {
	ctx, mgr := covCEAddLiqSetup(c)
	k := mgr.Keeper()
	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()
	consts := mgr.GetConstants()

	// staged rune / asset with a failing pool save
	mgr.K = newCovCEFailKeeper(k, "SetPool")
	h := NewAddLiquidityHandler(mgr)
	cctx, _ := ctx.CacheContext()
	err := h.addLiquidity(cctx, common.BTCAsset, cosmos.NewUint(common.One), cosmos.ZeroUint(), runeAddr, btcAddr, GetRandomTxHash(), true, consts)
	c.Check(err, ErrorMatches, "(?s).*fail to save pool pending inbound rune.*")
	cctx, _ = ctx.CacheContext()
	err = h.addLiquidity(cctx, common.BTCAsset, cosmos.ZeroUint(), cosmos.NewUint(common.One), runeAddr, btcAddr, GetRandomTxHash(), true, consts)
	c.Check(err, ErrorMatches, "(?s).*fail to save pool pending inbound asset.*")
	cctx, _ = ctx.CacheContext()
	err = h.addLiquidity(cctx, common.BTCAsset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddr, btcAddr, GetRandomTxHash(), false, consts)
	c.Check(err, ErrorMatches, "(?s).*fail to save pool.*")
	mgr.K = newCovCEFailKeeper(k, "GetPool")
	err = NewAddLiquidityHandler(mgr).addLiquidity(ctx, common.BTCAsset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddr, btcAddr, GetRandomTxHash(), false, consts)
	c.Check(err, ErrorMatches, "(?s).*kaboom.*")
	mgr.K = k

	// event emission failures surface as internal errors
	orig := mgr.eventMgr
	for _, stage := range []struct {
		r, a  uint64
		stage bool
	}{{common.One, 0, true}, {0, common.One, true}, {common.One, common.One, false}} {
		mgr.eventMgr = &covCEFailEventMgr{EventManager: orig, failOn: 1}
		sctx, _ := ctx.CacheContext()
		err = NewAddLiquidityHandler(mgr).addLiquidity(sctx, common.BTCAsset, cosmos.NewUint(stage.r), cosmos.NewUint(stage.a),
			GetRandomRUNEAddress(), GetRandomBTCAddress(), GetRandomTxHash(), stage.stage, consts)
		c.Check(err, ErrorMatches, "(?s).*fail to emit .*add liquidity event.*", Commentf("%+v", stage))
	}
	mgr.eventMgr = orig

	// LP pending amounts larger than the pool's pending totals are an accounting error
	lp := LiquidityProvider{
		Asset: common.BTCAsset, RuneAddress: runeAddr, AssetAddress: btcAddr,
		PendingRune: cosmos.NewUint(5), PendingAsset: cosmos.ZeroUint(), Units: cosmos.ZeroUint(),
		RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)
	err = NewAddLiquidityHandler(mgr).addLiquidity(ctx, common.BTCAsset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddr, btcAddr, GetRandomTxHash(), false, consts)
	c.Check(err, ErrorMatches, "(?s).*pending rune accounting error.*")
	lp.PendingRune = cosmos.ZeroUint()
	lp.PendingAsset = cosmos.NewUint(5)
	k.SetLiquidityProvider(ctx, lp)
	err = NewAddLiquidityHandler(mgr).addLiquidity(ctx, common.BTCAsset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddr, btcAddr, GetRandomTxHash(), false, consts)
	c.Check(err, ErrorMatches, "(?s).*pending asset accounting error.*")

	// a synth vault with units but no asset balance cannot accept deposits
	synth := common.BTCAsset.GetSyntheticAsset()
	pool := NewPool()
	pool.Asset = synth
	pool.Status = PoolAvailable
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	err = NewAddLiquidityHandler(mgr).addLiquidity(ctx, synth, cosmos.ZeroUint(), cosmos.NewUint(common.One), common.NoAddress, btcAddr, GetRandomTxHash(), false, consts)
	c.Check(err, ErrorMatches, "cannot add liquidity: pool has non-zero units with zero asset balance")
}
