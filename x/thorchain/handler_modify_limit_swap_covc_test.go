package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerModifyLimitSwapCovCESuite struct{}

var _ = Suite(&HandlerModifyLimitSwapCovCESuite{})

type covCELimitFixture struct {
	from   common.Address
	source common.Coin
	target common.Coin
	txID   common.TxID
	signer cosmos.AccAddress
}

func covCELimitSetup(c *C, ctx cosmos.Context, mgr *Mgrs) covCELimitFixture {
	fx := covCELimitFixture{
		from:   GetRandomBTCAddress(),
		source: common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		target: common.NewCoin(common.RuneAsset(), cosmos.NewUint(500*common.One)),
		txID:   GetRandomTxHash(),
		signer: GetRandomBech32Addr(),
	}
	tx := common.NewTx(fx.txID, fx.from, fx.from, common.Coins{fx.source}, common.Gas{}, "")
	swap := NewMsgSwap(tx, common.RuneAsset(), fx.from, fx.target.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, fx.signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *swap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *swap), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return fx
}

func (fx covCELimitFixture) msg(newTarget uint64, depAsset common.Asset, depAmt uint64) *MsgModifyLimitSwap {
	return types.NewMsgModifyLimitSwap(fx.from, fx.source, fx.target, cosmos.NewUint(newTarget), fx.signer, depAsset, cosmos.NewUint(depAmt))
}

func (s *HandlerModifyLimitSwapCovCESuite) TestDonationsCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	fx := covCELimitSetup(c, ctx, mgr)
	h := NewModifyLimitSwapHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// modification funds in an external asset are donated to the pool, and a
	// failing event emission is only logged
	mgr.eventMgr = &covCEFailEventMgr{EventManager: mgr.eventMgr, failOn: 1}
	_, err = h.Run(ctx, fx.msg(600*common.One, common.BTCAsset, 5))
	c.Assert(err, IsNil)
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(10*common.One+5))
	item, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fx.txID, 0)
	c.Assert(err, IsNil)
	c.Check(item.TradeTarget.Uint64(), Equals, uint64(600*common.One))

	// RUNE deposits stay in the reserve; the pool is untouched
	fx.target.Amount = cosmos.NewUint(600 * common.One)
	mgr.eventMgr = &covCEFailEventMgr{EventManager: mgr.eventMgr, failOn: 1}
	_, err = h.Run(ctx, fx.msg(700*common.One, common.RuneNative, 5))
	c.Assert(err, IsNil)
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(10*common.One+5))
	fx.target.Amount = cosmos.NewUint(700 * common.One)

	// native synth deposit is moved from reserve to asgard and donated
	synth := common.BTCAsset.GetSyntheticAsset()
	coin := common.NewCoin(synth, cosmos.NewUint(7))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(coin)), IsNil)
	_, err = h.Run(ctx, fx.msg(800*common.One, synth, 7))
	c.Assert(err, IsNil)
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(10*common.One+12))
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synth.Native()).Uint64(), Equals, uint64(7))
	fx.target.Amount = cosmos.NewUint(800 * common.One)

	// synth deposit with no reserve funds fails the whole modification (the
	// handler relies on the caller's cache context to roll back)
	cctx, _ := ctx.CacheContext()
	_, err = h.Run(cctx, fx.msg(900*common.One, synth, 7))
	c.Check(err, ErrorMatches, "fail to donate modification tx funds to pool: fail to transfer funds from reserve to asgard.*")

	// donation to a non-existent pool fails
	cctx, _ = ctx.CacheContext()
	_, err = h.Run(cctx, fx.msg(900*common.One, common.ETHAsset, 1))
	c.Check(err, ErrorMatches, ".*pool does not exist for asset ETH.ETH")
}

func (s *HandlerModifyLimitSwapCovCESuite) TestErrorsCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	fx := covCELimitSetup(c, ctx, mgr)
	k := mgr.Keeper()

	for _, tc := range []struct {
		method string
		msg    *MsgModifyLimitSwap
		err    string
	}{
		{"GetAdvSwapQueueIndex", fx.msg(600*common.One, common.EmptyAsset, 0), "kaboom"},
		{"GetAdvSwapQueueItem", fx.msg(600*common.One, common.EmptyAsset, 0), "could not find matching limit swap"},
		{"RemoveAdvSwapQueueIndex", fx.msg(600*common.One, common.EmptyAsset, 0), "kaboom"},
		{"SetAdvSwapQueueItem", fx.msg(600*common.One, common.EmptyAsset, 0), "kaboom"},
		{"GetPool", fx.msg(600*common.One, common.BTCAsset, 1), ".*fail to get pool: kaboom"},
		{"SetPool", fx.msg(600*common.One, common.BTCAsset, 1), ".*fail to save pool: kaboom"},
	} {
		mgr.K = newCovCEFailKeeper(k, tc.method)
		cctx, _ := ctx.CacheContext()
		err := NewModifyLimitSwapHandler(mgr).handle(cctx, *tc.msg)
		c.Check(err, ErrorMatches, tc.err, Commentf("method %s", tc.method))
	}
	mgr.K = k

	// iteration cap
	k.SetMimir(ctx, constants.ModifyLimitSwapMaxIterations.String(), 0)
	err := NewModifyLimitSwapHandler(mgr).handle(ctx, *fx.msg(600*common.One, common.EmptyAsset, 0))
	c.Check(err, ErrorMatches, "swap index too large, exceeded max iterations \\(0\\)")
}
