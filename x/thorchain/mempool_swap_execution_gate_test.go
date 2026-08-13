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
	c.Check(queue.isSwapReady(ctx, normal), Equals, false)

	// EndBlock must defer at readiness without consuming an attempt or
	// mutating either the queued state or the pool.
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
