package thorchain

import (
	"errors"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/gogoproto/proto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var errInjected = errors.New("injected fault")

// faultyDynFeeKeeper injects failures into the dynamic-fee store accessors,
// and optionally swaps the codec the callers unmarshal iterator values with,
// so the error paths of the manager, querier, mimir handler and migration
// can run against a real store.
type faultyDynFeeKeeper struct {
	keeper.Keeper
	cdc                                      codec.BinaryCodec
	failGetRecord, failSetRecord, failSetAcc bool
	failDeleteAcc, failGetPool, failSetPool  bool
	failDeletePool, failDeleteAll            bool
}

func (k *faultyDynFeeKeeper) Cdc() codec.BinaryCodec {
	if k.cdc != nil {
		return k.cdc
	}
	return k.Keeper.Cdc()
}

func (k *faultyDynFeeKeeper) GetDynamicFeeRecord(ctx cosmos.Context, thorname, a, b string) (types.DynamicFeeRecord, error) {
	if k.failGetRecord {
		return types.DynamicFeeRecord{}, errInjected
	}
	return k.Keeper.GetDynamicFeeRecord(ctx, thorname, a, b)
}

func (k *faultyDynFeeKeeper) SetDynamicFeeRecord(ctx cosmos.Context, rec types.DynamicFeeRecord) error {
	if k.failSetRecord {
		return errInjected
	}
	return k.Keeper.SetDynamicFeeRecord(ctx, rec)
}

func (k *faultyDynFeeKeeper) SetDynamicFeeAccumulator(ctx cosmos.Context, acc types.DynamicFeeAccumulator) error {
	if k.failSetAcc {
		return errInjected
	}
	return k.Keeper.SetDynamicFeeAccumulator(ctx, acc)
}

func (k *faultyDynFeeKeeper) DeleteDynamicFeeAccumulator(ctx cosmos.Context, thorname, a, b string) error {
	if k.failDeleteAcc {
		return errInjected
	}
	return k.Keeper.DeleteDynamicFeeAccumulator(ctx, thorname, a, b)
}

func (k *faultyDynFeeKeeper) GetDynamicFeePoolAccumulator(ctx cosmos.Context, a, b string) (types.DynamicFeePoolAccumulator, error) {
	if k.failGetPool {
		return types.DynamicFeePoolAccumulator{}, errInjected
	}
	return k.Keeper.GetDynamicFeePoolAccumulator(ctx, a, b)
}

func (k *faultyDynFeeKeeper) SetDynamicFeePoolAccumulator(ctx cosmos.Context, acc types.DynamicFeePoolAccumulator) error {
	if k.failSetPool {
		return errInjected
	}
	return k.Keeper.SetDynamicFeePoolAccumulator(ctx, acc)
}

func (k *faultyDynFeeKeeper) DeleteDynamicFeePoolAccumulator(ctx cosmos.Context, a, b string) error {
	if k.failDeletePool {
		return errInjected
	}
	return k.Keeper.DeleteDynamicFeePoolAccumulator(ctx, a, b)
}

func (k *faultyDynFeeKeeper) DeleteAllDynamicFeeStateForThorname(ctx cosmos.Context, thorname string) error {
	if k.failDeleteAll {
		return errInjected
	}
	return k.Keeper.DeleteAllDynamicFeeStateForThorname(ctx, thorname)
}

// faultyCodec fails, or post-processes, unmarshals of the message types its
// predicates select. Everything else goes to the real codec.
type faultyCodec struct {
	codec.BinaryCodec
	fail   func(proto.Message) bool
	mutate func(proto.Message)
}

func (c faultyCodec) Unmarshal(bz []byte, ptr proto.Message) error {
	if c.fail != nil && c.fail(ptr) {
		return errInjected
	}
	if err := c.BinaryCodec.Unmarshal(bz, ptr); err != nil {
		return err
	}
	if c.mutate != nil {
		c.mutate(ptr)
	}
	return nil
}

func isPoolAcc(m proto.Message) bool {
	_, ok := m.(*types.DynamicFeePoolAccumulator)
	return ok
}

func isThornameAcc(m proto.Message) bool {
	_, ok := m.(*types.DynamicFeeAccumulator)
	return ok
}

// seedSealable credits one swap and the pair's pool volume for "tn" on
// BTC|RUNE so the next epoch boundary has something to seal.
func seedSealable(ctx cosmos.Context, dfm *DynamicFeeMgr) {
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(5_000))
	creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000), cosmos.NewUint(10))
}

func (s *DynamicFeeMgrSuite) TestCreditPoolVolumeSkipsAndFaults(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	btcA, btcB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	synthBTC, err := common.NewAsset("BTC/BTC")
	c.Assert(err, IsNil)
	newDynamicFeeMgr(k).CreditPoolVolume(ctx, synthBTC, common.RuneAsset(), cosmos.NewUint(100))
	c.Check(dynamicFeeTotalPoolVolume(ctx, k).IsZero(), Equals, true, Commentf("non-L1 legs carry no pool volume"))

	for _, fk := range []*faultyDynFeeKeeper{{Keeper: k, failGetPool: true}, {Keeper: k, failSetPool: true}} {
		newDynamicFeeMgr(fk).CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(100))
	}
	acc, err := k.GetDynamicFeePoolAccumulator(ctx, btcA, btcB)
	c.Assert(err, IsNil)
	c.Check(acc.VolumeTor.IsZero(), Equals, true, Commentf("a failed read or write credits nothing"))
}

// TestEndBlockSurvivesStoreFaults pins that a store failure on one record
// leaves its accumulator in place for the next epoch rather than aborting
// EndBlock or losing the epoch's data.
func (s *DynamicFeeMgrSuite) TestEndBlockSurvivesStoreFaults(c *C) {
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	for _, tc := range []struct {
		name     string
		gate     bool
		fault    func(*faultyDynFeeKeeper)
		keepsAcc bool
	}{
		{name: "record read fails", fault: func(f *faultyDynFeeKeeper) { f.failGetRecord = true }, keepsAcc: true},
		{name: "record write fails on seal", fault: func(f *faultyDynFeeKeeper) { f.failSetRecord = true }, keepsAcc: true},
		{name: "record write fails on roll", gate: true, fault: func(f *faultyDynFeeKeeper) { f.failSetRecord = true }, keepsAcc: true},
		{name: "accumulator write fails on roll", gate: true, fault: func(f *faultyDynFeeKeeper) { f.failSetAcc = true }, keepsAcc: true},
		{name: "accumulator delete fails", fault: func(f *faultyDynFeeKeeper) { f.failDeleteAcc = true }, keepsAcc: true},
		{name: "pool delete fails", fault: func(f *faultyDynFeeKeeper) { f.failDeletePool = true }},
	} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		setDynamicFeeTestMimirs(ctx, k)
		if tc.gate {
			k.SetMimir(ctx, "L1DynamicFeeMinSwaps", 100)
		}
		registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
		seedSealable(ctx, newDynamicFeeMgr(k))

		fk := &faultyDynFeeKeeper{Keeper: k}
		tc.fault(fk)
		ctx = ctx.WithBlockHeight(20)
		c.Assert(newDynamicFeeMgr(fk).EndBlock(ctx), IsNil, Commentf(tc.name))

		acc, err := k.GetDynamicFeeAccumulator(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		c.Check(acc.VolumeTor.IsZero(), Equals, !tc.keepsAcc, Commentf(tc.name))
	}
}

// TestEndBlockPoolReadFaultHoldsTheSearch: without the pair's pool volume the
// sealed epoch cannot be normalized, so the search measures the same bps
// again instead of crediting a zero signal to it.
func (s *DynamicFeeMgrSuite) TestEndBlockPoolReadFaultHoldsTheSearch(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	seedSealable(ctx, newDynamicFeeMgr(k))

	ctx = ctx.WithBlockHeight(20)
	c.Assert(newDynamicFeeMgr(&faultyDynFeeKeeper{Keeper: k, failGetPool: true}).EndBlock(ctx), IsNil)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.DynamicBps, Equals, uint64(1))
	c.Check(rec.Probes, HasLen, 0, Commentf("an unnormalizable epoch is not a measurement"))
	c.Check(rec.SearchDone, Equals, false)
}

// TestEndBlockCodecFaults covers unreadable pool accumulators (skipped by the
// global total and by the end-of-epoch clear) and accumulators written before
// pool volume existed (nil Uint, read as zero).
func (s *DynamicFeeMgrSuite) TestEndBlockCodecFaults(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	seedSealable(ctx, newDynamicFeeMgr(k))

	fk := &faultyDynFeeKeeper{Keeper: k, cdc: faultyCodec{
		BinaryCodec: k.Cdc(),
		fail:        isPoolAcc,
		mutate: func(m proto.Message) {
			if acc, ok := m.(*types.DynamicFeeAccumulator); ok {
				acc.PoolVolumeTor = cosmos.Uint{}
			}
		},
	}}
	c.Check(dynamicFeeTotalPoolVolume(ctx, fk).IsZero(), Equals, true)

	ctx = ctx.WithBlockHeight(20)
	c.Assert(newDynamicFeeMgr(fk).EndBlock(ctx), IsNil)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.History, HasLen, 1, Commentf("a nil pool volume reads as zero and still seals"))
	c.Check(rec.History[0].PoolVolumeTor.Equal(cosmos.NewUint(5_000)), Equals, true)
	pool, err := k.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(pool.VolumeTor.IsZero(), Equals, false, Commentf("an unreadable pool accumulator is skipped, not cleared"))
}

func (s *DynamicFeeMgrSuite) TestSearchStepEdges(c *C) {
	p := dynamicFeeParams{floorBps: 1, ceilingBps: 20, deadbandBps: cosmos.ZeroUint(), tenK: cosmos.NewUint(10_000), probeEpochs: 1}

	rec := types.DynamicFeeRecord{}
	reason, bps, _, _ := searchStep(&rec, 5, p)
	c.Check(reason, Equals, "hold", Commentf("no sealed epoch to credit"))
	c.Check(bps, Equals, uint64(5))

	rec = types.DynamicFeeRecord{History: []types.DynamicFeeEpochRecord{{FeesTor: cosmos.NewUint(10)}}}
	reason, bps, _, _ = searchStep(&rec, 5, p)
	c.Check(reason, Equals, "hold", Commentf("no pool volume to normalize against"))
	c.Check(bps, Equals, uint64(5))
	c.Check(rec.Probes, HasLen, 0)

	// A probe without a signal sum (as unmarshalled from an empty field).
	rec = types.DynamicFeeRecord{
		History: []types.DynamicFeeEpochRecord{{FeesTor: cosmos.NewUint(10), PoolVolumeTor: types.SignalScale}},
		Probes:  []types.DynamicFeeProbe{{Bps: 5}},
	}
	reason, bps, best, current := searchStep(&rec, 5, p)
	c.Check(reason, Equals, "search_probe")
	c.Check(bps, Equals, uint64(20), Commentf("(5, 20] is the larger untested side"))
	c.Check(best.Equal(cosmos.NewUint(10)), Equals, true)
	c.Check(current.Equal(cosmos.NewUint(10)), Equals, true)
}

func (s *DynamicFeeMgrSuite) TestNextSearchBpsDuplicateProbes(c *C) {
	best, _, done := nextSearchBps(probesT(5, 10, 5, 10, 4, 1, 6, 1), 4, 6, cosmos.ZeroUint(), cosmos.NewUint(10_000))
	c.Check(best, Equals, uint64(5))
	c.Check(done, Equals, true)
}

func (s *DynamicFeeMgrSuite) TestWindowMeanSignalSkipsUnnormalizedEntries(c *C) {
	history := []types.DynamicFeeEpochRecord{
		{FeesTor: cosmos.NewUint(100)},
		{FeesTor: cosmos.NewUint(300), PoolVolumeTor: types.SignalScale},
		{FeesTor: cosmos.NewUint(500), PoolVolumeTor: cosmos.ZeroUint()},
	}
	c.Check(windowMeanSignal(history, 0, 3).Equal(cosmos.NewUint(300)), Equals, true)
	c.Check(windowMeanSignal(history, 0, 1).IsZero(), Equals, true, Commentf("no normalizable entry"))
}

func (s *DynamicFeeMgrSuite) TestMimirResetFaultIsLogged(c *C) {
	ctx, k := setupKeeperForTest(c)
	FundModule(c, ctx, k, BondName, 100*common.One)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	k.SetMimir(ctx, "OperationalVotesMin", 1)

	handler := NewMimirHandler(NewDummyMgrWithKeeper(&faultyDynFeeKeeper{Keeper: k, failDeleteAll: true}))
	_, err := handler.Run(ctx, NewMsgMimir("DYNAMICFEE-WHITELIST-FOO", types.DynamicFeeStateGlobal, na.NodeAddress))
	c.Assert(err, IsNil, Commentf("a failed reset does not fail the vote"))
	val, err := k.GetMimir(ctx, "DYNAMICFEE-WHITELIST-FOO")
	c.Assert(err, IsNil)
	c.Check(val, Equals, types.DynamicFeeStateGlobal)
}

func (s *DynamicFeeMgrSuite) TestMigrate20to21Faults(c *C) {
	c.Check(AppModule{}.ConsensusVersion(), Equals, uint64(21))

	for _, tc := range []struct {
		name  string
		fault func(*faultyDynFeeKeeper, keeper.Keeper)
	}{
		{name: "accumulator unreadable", fault: func(f *faultyDynFeeKeeper, k keeper.Keeper) {
			f.cdc = faultyCodec{BinaryCodec: k.Cdc(), fail: isThornameAcc}
		}},
		{name: "accumulator delete fails", fault: func(f *faultyDynFeeKeeper, _ keeper.Keeper) { f.failDeleteAcc = true }},
	} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		c.Assert(k.SetDynamicFeeAccumulator(ctx, types.NewDynamicFeeAccumulator("tn", "BTC.BTC", "THOR.RUNE")), IsNil)
		fk := &faultyDynFeeKeeper{Keeper: k}
		tc.fault(fk, k)
		mgr.K = fk
		c.Check(NewMigrator(mgr).Migrate20to21(ctx), NotNil, Commentf(tc.name))
	}
}

// TestQueryDynamicFeeSearchFields covers the search fields on both record
// endpoints, a per-pair in-progress entry, and records written before pool
// volume existed (nil Uint, reported as "0").
func (s *DynamicFeeMgrSuite) TestQueryDynamicFeeSearchFields(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname: "tn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 15, SearchDone: true,
		Probes: []types.DynamicFeeProbe{
			{Bps: 10, SignalSum: cosmos.NewUint(400), Epochs: 2},
			{Bps: 15, SignalSum: cosmos.NewUint(225), Epochs: 1},
		},
		History: []types.DynamicFeeEpochRecord{{Epoch: 3, VolumeTor: cosmos.NewUint(1), FeesTor: cosmos.NewUint(1), PoolVolumeTor: cosmos.NewUint(9)}},
	}), IsNil)
	seedSealable(ctx, newDynamicFeeMgr(k))

	qs := queryServer{mgr: mgr}
	all, err := qs.queryDynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{})
	c.Assert(err, IsNil)
	c.Assert(all.Entries, HasLen, 1)
	c.Check(all.Entries[0].Phase, Equals, "track")

	byName, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{Thorname: "tn"})
	c.Assert(err, IsNil)
	c.Assert(byName.Pairs, HasLen, 1)
	c.Check(byName.Pairs[0].Phase, Equals, "track")
	c.Check(byName.Pairs[0].Probes, DeepEquals, []types.DynamicL1FeeProbe{
		{Bps: 10, Signal: "200", Epochs: 2},
		{Bps: 15, Signal: "225", Epochs: 1},
	})

	current, err := qs.queryDynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(current.Entries, HasLen, 1)
	c.Check(current.Entries[0].PoolVolumeTor, Equals, "5000", Commentf("per-pair entries add their pair's live pool volume"))

	// Pre-pool-volume rows unmarshal with a nil Uint.
	mgr.K = &faultyDynFeeKeeper{Keeper: k, cdc: faultyCodec{BinaryCodec: k.Cdc(), mutate: func(m proto.Message) {
		switch v := m.(type) {
		case *types.DynamicFeeRecord:
			for i := range v.History {
				v.History[i].PoolVolumeTor = cosmos.Uint{}
			}
		case *types.DynamicFeeAccumulator:
			v.PoolVolumeTor = cosmos.Uint{}
		}
	}}}
	byName, err = qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{Thorname: "tn"})
	c.Assert(err, IsNil)
	c.Check(byName.Pairs[0].History[0].PoolVolumeTor, Equals, "0")
	current, err = qs.queryDynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Check(current.Entries[0].PoolVolumeTor, Equals, "5000")
}
