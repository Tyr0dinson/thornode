package thorchain

import (
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type LuviGuardSuite struct{}

var _ = Suite(&LuviGuardSuite{})

// luviLossEvents returns the luvi_loss events on ctx.
func luviLossEvents(ctx cosmos.Context) []map[string]string {
	var out []map[string]string
	for _, e := range types2.FlattenLuviEvents(ctx.EventManager().Events()) {
		if e["type"] == types2.LuviLossEventType {
			out = append(out, e)
		}
	}
	return out
}

func (e *luviEnv) guardMsg() *MsgSwap {
	tx := common.NewTx(GetRandomTxHash(), GetRandomETHAddress(), GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(common.One))), common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}, "")
	return NewMsgSwap(tx, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil, MarketSwap, 0, 0, types2.SwapVersion_v1, GetRandomBech32Addr())
}

// runGuarded runs op as a handler would: in a cache of e.ctx, committed only
// when the guard passes.
func (e *luviEnv) runGuarded(msg cosmos.Msg, op func(ctx cosmos.Context)) error {
	e.ctx = e.ctx.WithEventManager(cosmos.NewEventManager())
	cacheCtx, commit := e.ctx.CacheContext()
	err := runLuviGuarded(e.ctx, cacheCtx, e.mgr, msg, func(runCtx cosmos.Context) error {
		op(runCtx)
		return nil
	})
	if err == nil {
		commit()
	}
	return err
}

// steal takes RUNE out of the pool without paying anyone back.
func (e *luviEnv) steal(ctx cosmos.Context, bps uint64) {
	pool, err := e.mgr.Keeper().GetPool(ctx, e.asset)
	e.c.Assert(err, IsNil)
	pool.BalanceRune = common.SafeSub(pool.BalanceRune, pool.BalanceRune.MulUint64(bps).QuoUint64(10_000))
	e.c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
}

func (s *LuviGuardSuite) TestJournalCapturesPreStateOnce(c *C) {
	e := newLuviEnv(c, luviRegime{"light synths", 40})
	before := e.state()

	// no journal: nothing recorded, nothing read
	keeper.RecordLuviTouch(e.ctx, e.asset, func(common.Asset) types2.LuviState {
		c.Fatal("capture without a journal")
		return types2.LuviState{}
	})

	outer, jo := keeper.WithLuviJournal(e.ctx)
	e.steal(outer, 100)
	inner, ji := keeper.WithLuviJournal(outer)
	e.steal(inner, 100)
	e.mintSynths(cosmos.NewUint(common.One)) // e.ctx: not journaled

	c.Assert(jo.Assets(), HasLen, 1)
	c.Assert(ji.Assets(), HasLen, 1)
	pre, ok := jo.Pre(e.asset)
	c.Assert(ok, Equals, true)
	c.Check(pre.BalanceRune.String(), Equals, before.BalanceRune.String())
	c.Check(pre.SynthSupply.String(), Equals, before.SynthSupply.String())
	// the inner scope's pre-state is the state at its own first write
	preInner, _ := ji.Pre(e.asset)
	c.Check(preInner.BalanceRune.LT(before.BalanceRune), Equals, true)

	// synth mint and burn map to the layer-1 pool; untracked assets are ignored
	_, js := keeper.WithLuviJournal(e.ctx)
	c.Assert(js.Assets(), HasLen, 0)
	synthCtx, js := keeper.WithLuviJournal(e.ctx)
	c.Assert(e.mgr.Keeper().MintToModule(synthCtx, ModuleName, common.NewCoin(e.asset.GetSyntheticAsset(), cosmos.NewUint(1))), IsNil)
	c.Assert(e.mgr.Keeper().MintToModule(synthCtx, ModuleName, common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))), IsNil)
	c.Assert(e.mgr.Keeper().BurnFromModule(synthCtx, ModuleName, common.NewCoin(e.asset.GetSyntheticAsset(), cosmos.NewUint(1))), IsNil)
	c.Assert(js.Assets(), DeepEquals, []common.Asset{e.asset})

	_, ok = (*keeper.LuviJournal)(nil).Pre(e.asset)
	c.Check(ok, Equals, false)
	c.Check((*keeper.LuviJournal)(nil).Assets(), IsNil)
	for _, a := range []common.Asset{common.RuneAsset(), common.TOR, e.asset.GetTradeAsset(), e.asset.GetSecuredAsset(), common.EmptyAsset} {
		_, tracked := keeper.LuviTrackedAsset(a)
		c.Check(tracked, Equals, false, Commentf("%s", a))
	}
	got, tracked := keeper.LuviTrackedAsset(e.asset.GetSyntheticAsset())
	c.Check(tracked, Equals, true)
	c.Check(got.Equals(e.asset), Equals, true)
}

func (s *LuviGuardSuite) TestGuardRejectsTheftInUserFlows(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		before := e.state()
		msg := e.guardMsg()
		err := e.runGuarded(msg, func(ctx cosmos.Context) { e.steal(ctx, 10) })
		c.Assert(errors.Is(err, errLuviGuard), Equals, true, Commentf("%s", r.name))
		// reverted
		c.Check(e.state().BalanceRune.String(), Equals, before.BalanceRune.String(), Commentf("%s", r.name))
		// reported on the parent context, so it survives the revert
		evts := luviLossEvents(e.ctx)
		c.Assert(evts, HasLen, 1, Commentf("%s", r.name))
		c.Check(evts[0]["pool"], Equals, e.asset.String())
		c.Check(evts[0]["scope"], Equals, sdk.MsgTypeURL(msg))
		c.Check(evts[0]["tx_id"], Equals, msg.Tx.ID.String())
		c.Check(evts[0]["kind"], Equals, "drop")
		c.Check(evts[0]["excess"], Equals, "true")
		c.Check(evts[0]["rejected"], Equals, "true")
	}
}

func (s *LuviGuardSuite) TestGuardPassesLegitimateFlows(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		// swaps both ways, through the real swapper
		err := e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) {
			saved := e.ctx
			e.ctx = ctx
			e.swapRuneToAsset(1_000 * common.One)
			e.swapAssetToRune(10 * common.One)
			e.ctx = saved
		})
		c.Assert(err, IsNil, Commentf("%s", r.name))
		c.Check(luviLossEvents(e.ctx), HasLen, 0, Commentf("%s", r.name))

		// a donation only raises G
		err = e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) {
			pool, perr := e.mgr.Keeper().GetPool(ctx, e.asset)
			c.Assert(perr, IsNil)
			pool.BalanceRune = pool.BalanceRune.AddUint64(common.One)
			c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
		})
		c.Assert(err, IsNil, Commentf("%s", r.name))
	}
}

func (s *LuviGuardSuite) TestGuardReportsOnlyForProtocolMessages(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	before := e.state()
	msg := &MsgOutboundTx{}
	err := e.runGuarded(msg, func(ctx cosmos.Context) { e.steal(ctx, 10) })
	c.Assert(err, IsNil)
	c.Check(e.state().BalanceRune.LT(before.BalanceRune), Equals, true)
	evts := luviLossEvents(e.ctx)
	c.Assert(evts, HasLen, 1)
	c.Check(evts[0]["excess"], Equals, "true")
	c.Check(evts[0]["rejected"], Equals, "false")
	c.Check(evts[0]["tx_id"], Equals, "")
}

func (s *LuviGuardSuite) TestGuardLedger(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	mint := func(ctx cosmos.Context) {
		pool, err := e.mgr.Keeper().GetPool(ctx, e.asset)
		c.Assert(err, IsNil)
		pool.LPUnits = pool.LPUnits.MulUint64(2)
		c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
	}
	// LP units minted without an add_liquidity event
	err := e.runGuarded(e.guardMsg(), mint)
	c.Assert(errors.Is(err, errLuviGuard), Equals, true)
	kinds := map[string]bool{}
	for _, ev := range luviLossEvents(e.ctx) {
		kinds[ev["kind"]] = true
	}
	c.Check(kinds["ledger"], Equals, true)

	// an errata names the pool: its unit burn emits no unit event
	err = e.runGuarded(&MsgOutboundTx{}, func(ctx cosmos.Context) {
		pool, perr := e.mgr.Keeper().GetPool(ctx, e.asset)
		c.Assert(perr, IsNil)
		pool.LPUnits = pool.LPUnits.QuoUint64(2)
		pool.BalanceRune = pool.BalanceRune.QuoUint64(2)
		pool.BalanceAsset = pool.BalanceAsset.QuoUint64(2)
		c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
		ctx.EventManager().EmitEvent(cosmos.NewEvent(types2.ErrataEventType, cosmos.NewAttribute("asset", e.asset.String())))
	})
	c.Assert(err, IsNil)
	for _, ev := range luviLossEvents(e.ctx) {
		c.Check(ev["kind"], Not(Equals), "ledger")
	}
}

func (s *LuviGuardSuite) TestGuardDepletedPool(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	err := e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) { e.steal(ctx, 10_000) })
	c.Assert(errors.Is(err, errLuviGuard), Equals, true)
	evts := luviLossEvents(e.ctx)
	c.Assert(evts, HasLen, 1)
	c.Check(evts[0]["normalized_delta_g"], Equals, "-1.000000000000000000")

	// with every LP unit gone there is nothing to lose (the ledger still has
	// to match, so pair it with withdraw events)
	err = e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) {
		pool, perr := e.mgr.Keeper().GetPool(ctx, e.asset)
		c.Assert(perr, IsNil)
		units := pool.LPUnits
		pool.LPUnits = cosmos.ZeroUint()
		pool.BalanceRune = cosmos.ZeroUint()
		pool.BalanceAsset = cosmos.ZeroUint()
		c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
		ctx.EventManager().EmitEvent(cosmos.NewEvent(types2.WithdrawEventType,
			cosmos.NewAttribute("pool", e.asset.String()),
			cosmos.NewAttribute("liquidity_provider_units", units.String())))
	})
	c.Assert(err, IsNil)
}

func (s *LuviGuardSuite) TestGuardAllowanceAndReportThreshold(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	// a gas reimbursement: the pool pays asset and gets RUNE at spot
	pay := func(ctx cosmos.Context, bps uint64) {
		pool, err := e.mgr.Keeper().GetPool(ctx, e.asset)
		c.Assert(err, IsNil)
		g := pool.BalanceAsset.MulUint64(bps).QuoUint64(10_000)
		r := common.GetSafeShare(g, pool.BalanceAsset, pool.BalanceRune)
		pool.BalanceAsset = pool.BalanceAsset.Sub(g)
		pool.BalanceRune = pool.BalanceRune.Add(r)
		c.Assert(e.mgr.Keeper().SetPool(ctx, pool), IsNil)
		ctx.EventManager().EmitEvent(cosmos.NewEvent(types2.GasEventType,
			cosmos.NewAttribute("asset", e.asset.String()),
			cosmos.NewAttribute("asset_amt", g.String())))
	}
	// small: covered and below the report threshold
	c.Assert(e.runGuarded(&MsgOutboundTx{}, func(ctx cosmos.Context) { pay(ctx, 1) }), IsNil)
	c.Check(luviLossEvents(e.ctx), HasLen, 0)
	// large (a slash-sized payout): covered, but reported
	c.Assert(e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) { pay(ctx, 500) }), IsNil)
	evts := luviLossEvents(e.ctx)
	c.Assert(evts, HasLen, 1)
	c.Check(evts[0]["excess"], Equals, "false")
	c.Check(evts[0]["rejected"], Equals, "false")
}

func (s *LuviGuardSuite) TestGuardVirtualSynthDepthIsReportOnly(c *C) {
	e := newLuviEnv(c, luviRegime{"light synths", 40})
	// a free synth mint is a loss to LPs
	free := func(ctx cosmos.Context) {
		coin := common.NewCoin(e.asset.GetSyntheticAsset(), cosmos.NewUint(luviPoolAsset/100))
		c.Assert(e.mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	}
	e.mgr.Keeper().SetMimir(e.ctx, constants.VirtualMultSynthsBasisPoints.String(), 10_000)
	c.Assert(errors.Is(e.runGuarded(e.guardMsg(), free), errLuviGuard), Equals, true)

	e.mgr.Keeper().SetMimir(e.ctx, constants.VirtualMultSynthsBasisPoints.String(), 20_000)
	c.Assert(e.runGuarded(e.guardMsg(), free), IsNil)
	evts := luviLossEvents(e.ctx)
	c.Assert(evts, HasLen, 1)
	c.Check(evts[0]["excess"], Equals, "true")
	c.Check(evts[0]["rejected"], Equals, "false")
}

func (s *LuviGuardSuite) TestGuardMimirOff(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIGuard.String(), 0)
	c.Assert(e.runGuarded(e.guardMsg(), func(ctx cosmos.Context) {
		c.Check(keeper.LuviJournalFromContext(ctx), IsNil)
		e.steal(ctx, 10)
	}), IsNil)
	c.Check(luviLossEvents(e.ctx), HasLen, 0)

	ctx, scope := beginLuviScope(e.ctx, e.mgr.Keeper(), "endblock/test", "", false)
	c.Check(scope, IsNil)
	scope.end(ctx, e.mgr) // nil-safe
	losses, reject := scope.check(ctx, e.mgr.Keeper())
	c.Check(losses, IsNil)
	c.Check(reject, Equals, false)
}

func (s *LuviGuardSuite) TestGuardInternalHandler(c *C) {
	// the guard is wired into NewInternalHandler: a real swap passes
	e := newLuviEnv(c, luviRegime{"light synths", 40})
	tx := common.NewTx(GetRandomTxHash(), GetRandomETHAddress(), GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(10*common.One))), common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"=:THOR.RUNE:"+GetRandomTHORAddress().String())
	msg := NewMsgSwap(tx, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil, MarketSwap, 0, 0, types2.SwapVersion_v1, GetRandomBech32Addr())
	before := e.state()
	_, err := NewInternalHandler(e.mgr)(e.ctx, msg)
	c.Assert(err, IsNil)
	c.Check(e.state().BalanceAsset.GT(before.BalanceAsset), Equals, true)
}

func (s *LuviGuardSuite) TestEndBlockScope(c *C) {
	e := newLuviEnv(c, luviRegime{"no synths", 0})
	ctx, scope := beginLuviScope(e.ctx, e.mgr.Keeper(), "endblock/test", "", false)
	c.Assert(scope, NotNil)
	// events emitted before the scope began are not part of it
	e.steal(ctx, 10)
	scope.end(ctx, e.mgr)
	evts := luviLossEvents(e.ctx)
	c.Assert(evts, HasLen, 1)
	c.Check(evts[0]["scope"], Equals, "endblock/test")
	c.Check(evts[0]["rejected"], Equals, "false")

	c.Check(luviMsgTxID(&MsgRagnarok{Tx: common.ObservedTx{Tx: common.Tx{ID: "ABC"}}}), Equals, common.TxID("ABC"))
	c.Check(luviMsgTxID(&MsgMimir{}), Equals, common.TxID(""))
}

func (s *LuviGuardSuite) TestAccumulator(c *C) {
	e := newLuviEnv(c, luviRegime{"light synths", 40})
	k := e.mgr.Keeper()

	_, found, err := k.GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	// first block: initialized at zero
	advanceLuviAccumulators(e.ctx, k)
	rec, found, err := k.GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(rec.RawG.IsZero(), Equals, true)
	c.Check(rec.Height, Equals, e.ctx.BlockHeight())

	// a reward raises G; the accumulator matches a tracker over the same steps
	tracker, err := types2.NewLuviTracker(e.state())
	c.Assert(err, IsNil)
	pool := e.pool()
	pool.BalanceRune = pool.BalanceRune.AddUint64(100 * common.One)
	c.Assert(k.SetPool(e.ctx, pool), IsNil)
	e.ctx = e.ctx.WithBlockHeight(e.ctx.BlockHeight() + 1)
	advanceLuviAccumulators(e.ctx, k)
	step, err := tracker.Observe(e.state())
	c.Assert(err, IsNil)
	rec, _, err = k.GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Assert(err, IsNil)
	c.Check(rec.RawG.BigInt().String(), Equals, tracker.RawG().String())
	c.Check(rec.RawG.IsPositive(), Equals, true)
	c.Check(rec.LastNormalizedDeltaG.String(), Equals, step.NormalizedDeltaG.String())

	// unchanged state: no write
	height := rec.Height
	e.ctx = e.ctx.WithBlockHeight(e.ctx.BlockHeight() + 1)
	advanceLuviAccumulators(e.ctx, k)
	rec, _, _ = k.GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Check(rec.Height, Equals, height)

	// the query exposes it
	var resp types2.QueryPoolResponse
	setPoolLuviAccumulatorFields(e.ctx, k, &resp, e.pool())
	c.Check(resp.LuviG, Equals, tracker.G().String())
	c.Check(resp.LuviC, Equals, tracker.C().String())
	c.Check(resp.LuviStar, Equals, tracker.LuviStar().String())
	c.Check(resp.LuviHeight, Equals, height)

	// an emptied pool loses its accumulator
	pool = e.pool()
	pool.LPUnits = cosmos.ZeroUint()
	c.Assert(k.SetPool(e.ctx, pool), IsNil)
	advanceLuviAccumulators(e.ctx, k)
	_, found, err = k.GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
	var empty types2.QueryPoolResponse
	setPoolLuviAccumulatorFields(e.ctx, k, &empty, pool)
	c.Check(empty.LuviG, Equals, "")
}

func (s *LuviGuardSuite) TestEndBlockAdvancesAccumulator(c *C) {
	e := newLuviEnv(c, luviRegime{"light synths", 40})
	_, err := NewAppModule(e.mgr, false, true).EndBlock(e.ctx)
	c.Assert(err, IsNil)
	rec, found, err := e.mgr.Keeper().GetPoolLuviAccumulator(e.ctx, e.asset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(rec.Height, Equals, e.ctx.BlockHeight())
}
