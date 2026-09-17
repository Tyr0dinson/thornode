package thorchain

import (
	"errors"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MempoolSwapExecutionGateSuite struct{}

var _ = Suite(&MempoolSwapExecutionGateSuite{})

type missingMempoolSwapPoolOwnerKeeper struct {
	keeper.Keeper
	pool common.Asset
}

type mempoolSwapExecutionGateReadCountingKeeper struct {
	keeper.Keeper
	poolOwnerReads    map[string]int
	poolOverlayReads  map[string]int
	synthOverlayReads map[string]int
}

func newMempoolSwapExecutionGateReadCountingKeeper(k keeper.Keeper) *mempoolSwapExecutionGateReadCountingKeeper {
	return &mempoolSwapExecutionGateReadCountingKeeper{
		Keeper:            k,
		poolOwnerReads:    make(map[string]int),
		poolOverlayReads:  make(map[string]int),
		synthOverlayReads: make(map[string]int),
	}
}

func mempoolSwapExecutionGateReadKey(asset common.Asset) string {
	return strings.ToUpper(asset.GetLayer1Asset().String())
}

func (k *mempoolSwapExecutionGateReadCountingKeeper) GetMempoolSwapPoolOwner(
	ctx cosmos.Context,
	asset common.Asset,
) (types.MempoolSwap, bool, error) {
	k.poolOwnerReads[mempoolSwapExecutionGateReadKey(asset)]++
	return k.Keeper.GetMempoolSwapPoolOwner(ctx, asset)
}

func (k *mempoolSwapExecutionGateReadCountingKeeper) GetMempoolSwapPoolBalanceOverlay(
	ctx cosmos.Context,
	asset common.Asset,
) (keeper.MempoolSwapPoolBalanceOverlay, error) {
	k.poolOverlayReads[mempoolSwapExecutionGateReadKey(asset)]++
	return k.Keeper.GetMempoolSwapPoolBalanceOverlay(ctx, asset)
}

func (k *mempoolSwapExecutionGateReadCountingKeeper) GetMempoolSwapNativeSupplyOverlay(
	ctx cosmos.Context,
	asset common.Asset,
) (keeper.MempoolSwapNativeSupplyOverlay, error) {
	k.synthOverlayReads[mempoolSwapExecutionGateReadKey(asset)]++
	return k.Keeper.GetMempoolSwapNativeSupplyOverlay(ctx, asset)
}

func (k missingMempoolSwapPoolOwnerKeeper) GetMempoolSwapPoolOwner(
	ctx cosmos.Context,
	asset common.Asset,
) (types.MempoolSwap, bool, error) {
	if asset.GetLayer1Asset().Equals(k.pool.GetLayer1Asset()) {
		return types.MempoolSwap{}, false, nil
	}
	return k.Keeper.GetMempoolSwapPoolOwner(ctx, asset)
}

func (s *MempoolSwapExecutionGateSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func cloneMempoolSwapExecutionOwner(record types.MempoolSwap) (MsgSwap, types.MempoolSwap) {
	msg := record.LatestSwap
	msg.Tx.ID = GetRandomTxHash()
	record.TxId = msg.Tx.ID
	record.LatestSwap = msg
	record.PendingEffects = types.MempoolSwapPendingEffects{}
	return msg, record
}

func (s *MempoolSwapExecutionGateSuite) TestOnlyLifecycleOwnerCanUseActivePool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msgA, recordA, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceRuneIncrease = cosmos.NewUint(17)
	recordA.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)

	msgB, recordB := cloneMempoolSwapExecutionOwner(recordA)
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordB), IsNil)

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Check(queue.ensureExecutionPoolsAvailable(ctx, msgA), IsNil)
	err := queue.ensureExecutionPoolsAvailable(ctx, msgB)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)

	normal := msgB
	normal.Tx.ID = GetRandomTxHash()
	err = queue.ensureExecutionPoolsAvailable(ctx, normal)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)

	speculativeCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionSpeculative)
	speculativeCtx = withMempoolSwapExecutionIdentity(speculativeCtx, msgA.Tx.ID, msgA.Index)
	c.Check(ensureSwapExecutionFinalityIsolation(speculativeCtx, mgr, msgA), IsNil)

	foreignCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionSpeculative)
	foreignCtx = withMempoolSwapExecutionIdentity(foreignCtx, msgB.Tx.ID, msgB.Index)
	err = ensureSwapExecutionFinalityIsolation(foreignCtx, mgr, msgB)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)

	recordA.Status = types.MempoolSwapStatus_FINALIZED
	recordA.ReservedTor = cosmos.ZeroUint()
	recordA.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)
	c.Check(queue.ensureExecutionPoolsAvailable(ctx, normal), IsNil)
}

func (s *MempoolSwapExecutionGateSuite) TestFinalizedTombstoneDefersNewSpeculationUntilDeepReorgRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msgA, recordA, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceRuneIncrease = cosmos.NewUint(17)
	recordA.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)

	// B may be admitted while it owns no reversible state, but it must not be
	// allowed to claim A's pool after A becomes a zero-overlay tombstone.
	msgB, recordB := cloneMempoolSwapExecutionOwner(recordA)
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordB), IsNil)
	recordA.Status = types.MempoolSwapStatus_FINALIZED
	recordA.ReservedTor = cosmos.ZeroUint()
	recordA.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)

	poolOverlay, err := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(poolOverlay), Equals, true)
	indexedOwner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(indexedOwner.Cursor(), Equals, recordA.Cursor())

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Check(queue.ensureExecutionPoolsAvailable(ctx, msgA), IsNil,
		Commentf("the tombstone's own queued continuation must remain executable"))
	normal := msgB
	normal.Tx.ID = GetRandomTxHash()
	c.Check(queue.ensureExecutionPoolsAvailable(ctx, normal), IsNil,
		Commentf("an ordinary finalized/no-lifecycle swap is not speculative ownership"))
	err = queue.ensureExecutionPoolsAvailable(ctx, msgB)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)
	storedB, _, err := mgr.Keeper().GetMempoolSwap(ctx, recordB.TxId, recordB.Index)
	c.Assert(err, IsNil)
	c.Check(storedB.PendingEffects.Pools, HasLen, 0,
		Commentf("deferral must happen before B can journal or mutate the pool"))

	// A deep reorg can restore A's active overlay without racing B for the
	// owner index, because the tombstone never released that index.
	recordA.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	recordA.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)
	poolOverlay, err = mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolOverlay.BalanceRuneIncrease.Equal(cosmos.NewUint(17)), Equals, true)
	indexedOwner, found, err = mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(indexedOwner.Cursor(), Equals, recordA.Cursor())
	err = queue.ensureExecutionPoolsAvailable(ctx, msgB)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)
}

func (s *MempoolSwapExecutionGateSuite) TestFinalizedSwapQueuesBehindLifecycleOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ownerMsg, owner, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceAssetIncrease = cosmos.OneUint()
	owner.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)

	amount := cosmos.NewUint(10 * common.One)
	normal := ownerMsg
	normal.Tx.ID = GetRandomTxHash()
	normal.Tx.Coins[0].Amount = amount
	normal.Tx.Memo = "=:r:" + normal.Destination.String()
	normal.AffiliateAddress = common.NoAddress
	normal.AffiliateBasisPoints = cosmos.ZeroUint()
	normal.StreamQuantity = 2
	normal.StreamInterval = 1
	normal.State = &types.SwapState{
		Quantity:   2,
		Interval:   1,
		Deposit:    amount,
		Withdrawn:  cosmos.ZeroUint(),
		In:         cosmos.ZeroUint(),
		Out:        cosmos.ZeroUint(),
		LastHeight: ctx.BlockHeight() - 1,
	}

	// Queue admission is the only path allowed to bypass the owner gate. A
	// direct validation of the same swap must remain isolated.
	err := NewSwapHandler(mgr).validate(ctx, normal)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Assert(queue.AddSwapQueueItem(ctx, mgr, &normal), IsNil)
	// Readiness admits a final swap so the execution site can try to preempt
	// the owner for it. This owner has no voter, so preemption cannot resolve
	// it and the swap defers there instead.
	c.Check(queue.isSwapReady(ctx, normal), Equals, true)

	// EndBlock must defer without consuming an attempt or mutating either the
	// queued state or the pool.
	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)
	stored, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, normal.Tx.ID, int(normal.Index))
	c.Assert(err, IsNil)
	c.Check(stored.State.Count, Equals, uint64(0))
	c.Check(stored.State.In.IsZero(), Equals, true)
	c.Check(stored.State.Out.IsZero(), Equals, true)
	c.Check(stored.State.FailedSwaps, HasLen, 0)
	poolDeferred, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolDeferred.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true)
	c.Check(poolDeferred.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true)

	owner.Status = types.MempoolSwapStatus_FINALIZED
	owner.ReservedTor = cosmos.ZeroUint()
	owner.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)
	c.Check(queue.isSwapReady(ctx, stored), Equals, true)

	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)
	stored, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, normal.Tx.ID, int(normal.Index))
	c.Assert(err, IsNil)
	c.Check(stored.State.Count, Equals, uint64(1))
	c.Check(stored.State.In.IsZero(), Equals, false)
	c.Check(stored.State.Out.IsZero(), Equals, false)
	c.Check(stored.State.FailedSwaps, HasLen, 0)
}

func (s *MempoolSwapExecutionGateSuite) TestLegacyQueueDefersPoolConflictWithoutAttemptOrRemoval(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ownerMsg, owner, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceAssetIncrease = cosmos.OneUint()
	owner.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)

	legacy := ownerMsg
	legacy.Tx.ID = GetRandomTxHash()
	legacy.Version = types.SwapVersion_v1
	legacy.StreamInterval = 0
	legacy.StreamQuantity = 0
	legacy.Tx.Coins[0].Amount = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, legacy, int(legacy.Index)), IsNil)
	samePoolAlias := legacy
	samePoolAlias.Tx.ID = GetRandomTxHash()
	samePoolAlias.Tx.Coins = common.Coins{common.NewCoin(
		common.BTCAsset.GetSyntheticAsset(),
		cosmos.NewUint(99*common.One),
	)}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, samePoolAlias, int(samePoolAlias.Index)), IsNil)

	unrelatedPool := NewPool()
	unrelatedPool.Asset = common.ETHAsset
	unrelatedPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	unrelatedPool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	unrelatedPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, unrelatedPool), IsNil)
	unrelated := legacy
	unrelated.Tx.ID = GetRandomTxHash()
	unrelated.Tx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, unrelated, int(unrelated.Index)), IsNil)

	// One attempt per block makes this exercise the budget directly. The
	// higher-fee blocked BTC swap sorts first but must not consume the slot that
	// the unrelated ETH swap can use.
	mgr.Keeper().SetMimir(ctx, constants.MinSwapsPerBlock.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.MaxSwapsPerBlock.String(), 1)
	attemptedIDs := make([]common.TxID, 0, 2)
	gateKeeper := newMempoolSwapExecutionGateReadCountingKeeper(mgr.Keeper())
	queue := newSwapQueue(gateKeeper)
	queue.handler = func(Manager) cosmos.Handler {
		return func(_ cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			swapMsg, ok := msg.(*MsgSwap)
			c.Assert(ok, Equals, true, Commentf("expected *MsgSwap, got %T", msg))
			attemptedIDs = append(attemptedIDs, swapMsg.Tx.ID)
			return &cosmos.Result{}, nil
		}
	}

	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	c.Assert(attemptedIDs, DeepEquals, []common.TxID{unrelated.Tx.ID})
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, legacy.Tx.ID, int(legacy.Index)), Equals, true)
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, samePoolAlias.Tx.ID, int(samePoolAlias.Index)), Equals, true)
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, unrelated.Tx.ID, int(unrelated.Index)), Equals, false)
	btcKey := mempoolSwapExecutionGateReadKey(common.BTCAsset)
	c.Check(gateKeeper.poolOwnerReads[btcKey], Equals, 1,
		Commentf("the live owner must be read once for L1 and synth aliases in one EndBlock"))
	c.Check(gateKeeper.poolOverlayReads[btcKey], Equals, 0,
		Commentf("an indexed owner must not fall through to orphan-overlay reads"))
	c.Check(gateKeeper.synthOverlayReads[btcKey], Equals, 0)

	// Remove the extra alias fixture, then transition the owner between passes.
	// A fresh per-EndBlock cache must observe FINALIZED and allow the original
	// legacy item to execute.
	mgr.Keeper().RemoveSwapQueueItem(ctx, samePoolAlias.Tx.ID, int(samePoolAlias.Index))
	owner.Status = types.MempoolSwapStatus_FINALIZED
	owner.ReservedTor = cosmos.ZeroUint()
	owner.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	c.Assert(attemptedIDs, DeepEquals, []common.TxID{unrelated.Tx.ID, legacy.Tx.ID})
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, legacy.Tx.ID, int(legacy.Index)), Equals, false)
	c.Check(gateKeeper.poolOwnerReads[btcKey], Equals, 2,
		Commentf("the next EndBlock must not reuse the prior pass's pending result"))
}

func (s *MempoolSwapExecutionGateSuite) TestLegacyQueueIsolatesInconsistentPoolStateToAffectedItem(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ownerMsg, owner, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceAssetIncrease = cosmos.OneUint()
	owner.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)

	legacy := ownerMsg
	legacy.Tx.ID = GetRandomTxHash()
	legacy.Version = types.SwapVersion_v1
	legacy.StreamInterval = 0
	legacy.StreamQuantity = 0
	legacy.Tx.Coins[0].Amount = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, legacy, int(legacy.Index)), IsNil)
	samePool := legacy
	samePool.Tx.ID = GetRandomTxHash()
	samePool.Tx.Coins[0].Amount = cosmos.NewUint(99 * common.One)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, samePool, int(samePool.Index)), IsNil)

	unrelatedPool := NewPool()
	unrelatedPool.Asset = common.ETHAsset
	unrelatedPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	unrelatedPool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	unrelatedPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, unrelatedPool), IsNil)
	unrelated := legacy
	unrelated.Tx.ID = GetRandomTxHash()
	unrelated.Tx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, unrelated, int(unrelated.Index)), IsNil)

	// Simulate a missing derived owner index while retaining the active BTC
	// overlay. The affected swap must remain fail-closed without preventing an
	// unrelated pool from using the rest of this block's queue budget.
	mgr.Keeper().SetMimir(ctx, constants.MinSwapsPerBlock.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.MaxSwapsPerBlock.String(), 1)
	attemptedIDs := make([]common.TxID, 0, 1)
	orphanKeeper := missingMempoolSwapPoolOwnerKeeper{
		Keeper: mgr.Keeper(),
		pool:   common.BTCAsset,
	}
	gateErr := ensureMempoolSwapExecutionPoolsAvailable(ctx, orphanKeeper, legacy, nil)
	c.Assert(gateErr, NotNil)
	c.Check(errors.Is(gateErr, errMempoolSwapPoolExecutionPending), Equals, false)
	c.Check(gateErr.Error(), Equals, "mempool swap pool BTC.BTC has an active overlay without an owner")
	gateKeeper := newMempoolSwapExecutionGateReadCountingKeeper(orphanKeeper)
	queue := newSwapQueue(gateKeeper)

	queued, err := queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	queued, err = queue.scoreMsgs(ctx, queued, 10_000)
	c.Assert(err, IsNil)
	queued = queued.Sort()
	c.Assert(queued, HasLen, 3)
	c.Check(queued[0].msg.Tx.ID, Equals, legacy.Tx.ID,
		Commentf("the inconsistent item must sort first to exercise the one-attempt budget"))
	c.Check(queued[1].msg.Tx.ID, Equals, samePool.Tx.ID,
		Commentf("both inconsistent items must be scanned before the unrelated item"))

	queue.handler = func(Manager) cosmos.Handler {
		return func(_ cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			swapMsg, ok := msg.(*MsgSwap)
			c.Assert(ok, Equals, true, Commentf("expected *MsgSwap, got %T", msg))
			attemptedIDs = append(attemptedIDs, swapMsg.Tx.ID)
			return &cosmos.Result{}, nil
		}
	}

	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	c.Assert(attemptedIDs, DeepEquals, []common.TxID{unrelated.Tx.ID})
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, legacy.Tx.ID, int(legacy.Index)), Equals, true)
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, samePool.Tx.ID, int(samePool.Index)), Equals, true)
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, unrelated.Tx.ID, int(unrelated.Index)), Equals, false)
	btcKey := mempoolSwapExecutionGateReadKey(common.BTCAsset)
	ethKey := mempoolSwapExecutionGateReadKey(common.ETHAsset)
	c.Check(gateKeeper.poolOwnerReads[btcKey], Equals, 1,
		Commentf("the orphaned BTC pool must be validated only once per EndBlock"))
	c.Check(gateKeeper.poolOverlayReads[btcKey], Equals, 1)
	c.Check(gateKeeper.synthOverlayReads[btcKey], Equals, 1)
	c.Check(gateKeeper.poolOwnerReads[ethKey], Equals, 1,
		Commentf("an unrelated pool must still receive its own independent validation"))
	c.Check(gateKeeper.poolOverlayReads[ethKey], Equals, 1)
	c.Check(gateKeeper.synthOverlayReads[ethKey], Equals, 1)
}

func (s *MempoolSwapExecutionGateSuite) TestTwoPoolSwapDefersForEitherOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_, record, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceAssetDecrease = cosmos.OneUint()
	record.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	msg := record.LatestSwap
	msg.Tx.ID = GetRandomTxHash()
	msg.Tx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}
	msg.TargetAsset = common.BTCAsset

	err := ensureMempoolSwapExecutionPoolsAvailable(ctx, mgr.Keeper(), msg, nil)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)
}

func (s *MempoolSwapExecutionGateSuite) TestMultiplePhysicalOwnersRejectedAtStoreBoundary(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msgA, recordA, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effectA := newMempoolSwapPoolEffect(common.BTCAsset)
	effectA.BalanceRuneIncrease = cosmos.NewUint(7)
	recordA.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effectA}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, recordA), IsNil)

	_, recordB := cloneMempoolSwapExecutionOwner(recordA)
	effectB := newMempoolSwapPoolEffect(common.BTCAsset)
	effectB.BalanceAssetIncrease = cosmos.NewUint(9)
	recordB.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effectB}
	err := mgr.Keeper().SetMempoolSwap(ctx, recordB)
	c.Assert(err, NotNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, recordB.TxId, recordB.Index), Equals, false)

	c.Check(ensureMempoolSwapExecutionPoolsAvailable(ctx, mgr.Keeper(), msgA, &recordA), IsNil)
	err = ensureMempoolSwapExecutionPoolsAvailable(ctx, mgr.Keeper(), recordB.LatestSwap, &recordB)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)

	rollbackCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionRollback)
	c.Check(ensureSwapExecutionFinalityIsolation(rollbackCtx, mgr, msgA), IsNil)
}

// newLifecyclelessMempoolSwapExecutionSwap returns a swap over the same pools as
// the lifecycle owner but with its own inbound hash and no lifecycle record,
// which is how an ordinary already-final inbound reaches the queue.
func newLifecyclelessMempoolSwapExecutionSwap(owner MsgSwap) MsgSwap {
	msg := owner
	msg.Tx.ID = GetRandomTxHash()
	return msg
}

// A speculative inbound is only ever unconfirmed, and full-RBF relay means any
// unconfirmed inbound can be replaced so it never confirms. A swap whose own
// inbound is already final must therefore be able to take the pool back rather
// than wait for the lifecycle recovery timeout.
func (s *MempoolSwapExecutionGateSuite) TestFinalSwapPreemptsUnconfirmedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, voter, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)

	// The owner's inbound is still unconfirmed: exactly the state an attacker
	// holds while the replacement propagates.
	c.Assert(voter.FinalisedHeight, Equals, int64(0))

	queue := newSwapQueueAdv(mgr.Keeper())
	final := newLifecyclelessMempoolSwapExecutionSwap(record.LatestSwap)
	blocked := queue.ensureExecutionPoolsAvailable(ctx, final)
	c.Assert(blocked, NotNil)
	c.Check(errors.Is(blocked, errMempoolSwapPoolExecutionPending), Equals, true)

	c.Assert(preemptMempoolSwapExecutionOwnersAtomic(ctx, mgr, final), IsNil)

	c.Check(queue.ensureExecutionPoolsAvailable(ctx, final), IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	// The preempted lifecycle releases its reservation rather than leaking it.
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.MempoolSwapTOR.IsZero(), Equals, true)
	c.Check(updatedVoter.Reverted, Equals, false,
		Commentf("preemption must leave the inbound free to execute normally if it does confirm"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

// Preemption is a privilege of finality, not of contention. A second
// speculative lifecycle has an unconfirmed inbound of its own and must keep
// deferring, otherwise two reversible lifecycles could interleave deltas on one
// pool and neither could roll back exactly.
func (s *MempoolSwapExecutionGateSuite) TestSpeculativeSwapCannotPreemptPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, _, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)

	foreign, foreignRecord := cloneMempoolSwapExecutionOwner(record)
	foreignRecord.Status = types.MempoolSwapStatus_EXECUTING
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, foreignRecord), IsNil)

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Check(queue.isSwapReady(ctx, foreign), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, true,
		Commentf("a speculative swap must not roll back the pool owner"))
}

// Readiness and execution have to agree: filtering a final swap out of the
// fetched batch would leave it deferred even though the execution site is
// willing to preempt for it.
func (s *MempoolSwapExecutionGateSuite) TestReadinessAdmitsFinalSwapBehindUnconfirmedOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, _, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)

	queue := newSwapQueueAdv(mgr.Keeper())
	final := newLifecyclelessMempoolSwapExecutionSwap(record.LatestSwap)
	c.Check(queue.isSwapReady(ctx, final), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, true,
		Commentf("readiness must stay read-only; the execution site owns preemption"))
}

// The replacement scenario end to end: an unconfirmed inbound buys a hold on
// BTC, and the hold outlives the inbound because the sender replaces it. A
// queued swap whose own inbound is already final must still execute in the very
// next EndBlock rather than wait out the lifecycle recovery timeout.
func (s *MempoolSwapExecutionGateSuite) TestEndBlockExecutesFinalSwapHeldByReplacedInbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, voter, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	// setExecutedMempoolSwapPoolOwner installs dummy constants, which zero the
	// per-block swap budget EndBlock derives from them.
	mgr.Keeper().SetMimir(ctx, constants.MinSwapsPerBlock.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.MaxSwapsPerBlock.String(), 100)

	amount := cosmos.NewUint(10 * common.One)
	final := newLifecyclelessMempoolSwapExecutionSwap(record.LatestSwap)
	final.Tx.Coins[0].Amount = amount
	final.Tx.Memo = "=:r:" + final.Destination.String()
	final.AffiliateAddress = common.NoAddress
	final.AffiliateBasisPoints = cosmos.ZeroUint()
	final.StreamQuantity = 1
	final.StreamInterval = 0
	final.State = &types.SwapState{
		Quantity:   1,
		Interval:   0,
		Deposit:    amount,
		Withdrawn:  cosmos.ZeroUint(),
		In:         cosmos.ZeroUint(),
		Out:        cosmos.ZeroUint(),
		LastHeight: ctx.BlockHeight() - 1,
	}

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Assert(queue.AddSwapQueueItem(ctx, mgr, &final), IsNil)
	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)

	// The speculative hold is gone and the final swap made progress.
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	stored, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, final.Tx.ID, int(final.Index))
	if err == nil {
		c.Check(stored.State.FailedSwaps, HasLen, 0)
		c.Check(stored.State.Count, Equals, uint64(1))
	}
}

// The legacy queue has the same exposure: its items are always final, so an
// unconfirmed inbound must not be able to park on their pools.
func (s *MempoolSwapExecutionGateSuite) TestLegacyQueuePreemptsUnconfirmedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, _, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)

	legacy := newLifecyclelessMempoolSwapExecutionSwap(record.LatestSwap)
	legacy.Version = types.SwapVersion_v1
	legacy.StreamInterval = 0
	legacy.StreamQuantity = 0
	legacy.Tx.Coins[0].Amount = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, legacy, int(legacy.Index)), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MinSwapsPerBlock.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.MaxSwapsPerBlock.String(), 1)
	attemptedIDs := make([]common.TxID, 0, 1)
	queue := newSwapQueue(mgr.Keeper())
	queue.handler = func(Manager) cosmos.Handler {
		return func(_ cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			swapMsg, ok := msg.(*MsgSwap)
			c.Assert(ok, Equals, true, Commentf("expected *MsgSwap, got %T", msg))
			attemptedIDs = append(attemptedIDs, swapMsg.Tx.ID)
			return &cosmos.Result{}, nil
		}
	}

	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	c.Check(attemptedIDs, DeepEquals, []common.TxID{legacy.Tx.ID})
	c.Check(mgr.Keeper().HasSwapQueueItem(ctx, legacy.Tx.ID, int(legacy.Index)), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
}

// An owner which cannot be resolved stays unresolvable for the rest of the
// pass, so preemption must be attempted once per pool rather than once per
// queued swap waiting on it.
func (s *MempoolSwapExecutionGateSuite) TestPreemptionFailureIsNotRetriedWithinAPass(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// No voter is ever created for this owner, so preemption cannot resolve it.
	ownerMsg, owner, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(common.BTCAsset)
	effect.BalanceAssetIncrease = cosmos.OneUint()
	owner.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, owner), IsNil)

	baseKeeper := mgr.Keeper()
	gateKeeper := newMempoolSwapExecutionGateReadCountingKeeper(baseKeeper)
	mgr.K = gateKeeper
	defer func() { mgr.K = baseKeeper }()
	preemptions := newMempoolSwapPreemptionAttempts()

	first := newLifecyclelessMempoolSwapExecutionSwap(ownerMsg)
	c.Check(preemptions.preempt(ctx, mgr, first), NotNil)
	btcKey := mempoolSwapExecutionGateReadKey(common.BTCAsset)
	readsAfterFirst := gateKeeper.poolOwnerReads[btcKey]
	c.Check(readsAfterFirst > 0, Equals, true)

	second := newLifecyclelessMempoolSwapExecutionSwap(ownerMsg)
	err := preemptions.preempt(ctx, mgr, second)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)
	c.Check(gateKeeper.poolOwnerReads[btcKey], Equals, readsAfterFirst,
		Commentf("a recorded failure must short-circuit before touching the keeper again"))
	c.Check(baseKeeper.HasMempoolSwap(ctx, owner.TxId, owner.Index), Equals, true)
}

// Admit a second swap after the first has executed one slice and claimed BTC.
// Both inbounds are initially unconfirmed, so the second swap must wait.
func setupWaitingMempoolSwap(c *C) (cosmos.Context, *Mgrs, mempoolSwapLifecycleFixture, MsgSwap, ObservedTxVoter) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	owner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(owner.TxId, Equals, fixture.voter.TxID)

	waiting := fixture.msg
	waiting.Tx.ID = GetRandomTxHash()
	state := *waiting.State
	waiting.State = &state
	queue := newSwapQueueAdv(mgr.Keeper())
	c.Assert(queue.AddSwapQueueItem(ctx, mgr, &waiting), IsNil)
	observed := NewObservedTx(waiting.Tx, ctx.BlockHeight(), fixture.vault.PubKey, 0)
	voter := NewObservedTxVoter(waiting.Tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = fixture.reservedTOR
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().AddMempoolSwapUsageTOR(ctx, fixture.reservedTOR)
	record := newMempoolSwapLifecycle(ctx, mgr.Keeper(), voter, waiting, fixture.reservedTOR)
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	c.Assert(queue.isSwapReady(ctx, waiting), Equals, false)
	return ctx, mgr, fixture, waiting, voter
}

func (s *MempoolSwapExecutionGateSuite) TestConfirmedLifecyclePreemptsUnconfirmedPoolOwner(c *C) {
	ctx, mgr, fixture, waiting, voter := setupWaitingMempoolSwap(c)
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	record, found, err := mgr.Keeper().GetMempoolSwap(ctx, waiting.Tx.ID, waiting.Index)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(record.Status, Equals, types.MempoolSwapStatus_FINALIZED)
	c.Assert(record.PendingEffects.Pools, HasLen, 0)

	queue := newSwapQueueAdv(mgr.Keeper())
	c.Check(queue.isSwapReady(ctx, waiting), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, true,
		Commentf("readiness must not preempt the owner"))
	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)

	stored, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, waiting.Tx.ID, int(waiting.Index))
	c.Assert(err, IsNil)
	c.Check(stored.State.Count, Equals, uint64(1))
	c.Check(stored.State.In.IsZero(), Equals, false)
	c.Check(stored.State.FailedSwaps, HasLen, 0)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	owner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(owner.TxId, Equals, waiting.Tx.ID)
	c.Check(owner.Status, Equals, types.MempoolSwapStatus_FINALIZED)
	c.Check(len(owner.PendingEffects.Pools) > 0, Equals, true,
		Commentf("the confirmed stream must retain its own effects journal"))
	preempted, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(preempted.Reverted, Equals, false)
	c.Check(preempted.MempoolSwapTOR.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapExecutionGateSuite) TestLifecyclePreemptionRequiresCompletedFinality(c *C) {
	for _, tc := range []struct {
		status    types.MempoolSwapStatus
		confirmed bool
		reverted  bool
	}{
		{types.MempoolSwapStatus_RESERVED, false, false},
		{types.MempoolSwapStatus_EXECUTING, false, false},
		{types.MempoolSwapStatus_FINALIZED, false, false},
		{types.MempoolSwapStatus_RESERVED, true, false},
		{types.MempoolSwapStatus_ROLLBACK_PENDING, true, false},
		{types.MempoolSwapStatus_FINALIZED, true, true},
	} {
		ctx, mgr, fixture, waiting, voter := setupWaitingMempoolSwap(c)
		if tc.confirmed {
			voter.FinalisedHeight = ctx.BlockHeight()
		}
		voter.Reverted = tc.reverted
		record, _, err := mgr.Keeper().GetMempoolSwap(ctx, waiting.Tx.ID, waiting.Index)
		c.Assert(err, IsNil)
		if tc.status == types.MempoolSwapStatus_FINALIZED {
			mgr.Keeper().SubMempoolSwapUsageTOR(ctx, record.ReservedTor)
			record.ReservedTor = cosmos.ZeroUint()
			voter.MempoolSwapTOR = cosmos.ZeroUint()
		}
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		record.Status = tc.status
		c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		queue := newSwapQueueAdv(mgr.Keeper())
		c.Check(queue.isSwapReady(ctx, waiting), Equals, false,
			Commentf("status=%s confirmed=%t reverted=%t", tc.status, tc.confirmed, tc.reverted))
		c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)
		c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, true)
		stored, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, waiting.Tx.ID, int(waiting.Index))
		c.Assert(err, IsNil)
		c.Check(stored.State.Count, Equals, uint64(0))
		c.Check(stored.State.FailedSwaps, HasLen, 0)
	}
}

func (s *MempoolSwapExecutionGateSuite) TestConfirmedLifecycleDefersBehindFinalizedPoolOwner(c *C) {
	ctx, mgr, fixture, waiting, voter := setupWaitingMempoolSwap(c)
	for _, finalVoter := range []ObservedTxVoter{fixture.voter, voter} {
		finalVoter.FinalisedHeight = ctx.BlockHeight()
		mgr.Keeper().SetObservedTxInVoter(ctx, finalVoter)
		c.Assert(completeMempoolSwapAtomic(ctx, mgr, finalVoter), IsNil)
	}
	ownerBefore, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(ownerBefore.Status, Equals, types.MempoolSwapStatus_FINALIZED)

	// The first confirmed stream still owns pool effects. A second lifecycle
	// cannot interleave its journal, even though a lifecycle-free swap could.
	err = preemptMempoolSwapExecutionOwnersAtomic(ctx, mgr, waiting)
	c.Check(errors.Is(err, errMempoolSwapPoolExecutionPending), Equals, true)
	queue := newSwapQueueAdv(mgr.Keeper())
	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil)
	stored, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, waiting.Tx.ID, int(waiting.Index))
	c.Assert(err, IsNil)
	c.Check(stored.State.Count, Equals, uint64(0))
	c.Check(stored.State.FailedSwaps, HasLen, 0)
	ownerAfter, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(ownerAfter, DeepEquals, ownerBefore)
}
