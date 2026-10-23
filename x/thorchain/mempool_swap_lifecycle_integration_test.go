package thorchain

import (
	"fmt"
	"math"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MempoolSwapLifecycleIntegrationSuite struct{}

var _ = Suite(&MempoolSwapLifecycleIntegrationSuite{})

const lifecycleTestAffiliate = "LIFECYCLE-FINALITY"

type mempoolSwapLifecycleFixture struct {
	node        NodeAccount
	vault       Vault
	voter       ObservedTxVoter
	msg         MsgSwap
	sourcePool  Pool
	reservedTOR cosmos.Uint
}

type mempoolSwapEventManagerOverride struct {
	Manager
	eventMgr EventManager
}

func (m mempoolSwapEventManagerOverride) EventMgr() EventManager { return m.eventMgr }

type failMempoolRefundEventManager struct {
	EventManager
}

func (m failMempoolRefundEventManager) EmitEvent(_ cosmos.Context, _ EmitEventItem) error {
	return fmt.Errorf("injected refund event failure")
}

func (s *MempoolSwapLifecycleIntegrationSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestRecoveryHeightBoundsUntrustedIntervals(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), 3)
	mgr.Keeper().SetMimir(ctx, constants.StreamingSwapMaxLength.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.StreamingLimitSwapMaxAge.String(), 200)

	market := fixture.msg
	market.SwapType = types.SwapType_market
	market.State.Interval = math.MaxUint64
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_RESERVED), Equals, ctx.BlockHeight()+110)

	limit := fixture.msg
	limit.SwapType = types.SwapType_limit
	limit.InitialBlockHeight = ctx.BlockHeight()
	limit.State.Interval = math.MaxUint64
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), limit, types.MempoolSwapStatus_EXECUTING), Equals, ctx.BlockHeight()+210)

	c.Check(addMempoolSwapRecoveryHeight(math.MaxInt64-5, 10), Equals, int64(math.MaxInt64))
	c.Check(multiplyMempoolSwapRecoveryDelay(math.MaxInt64, 2), Equals, int64(math.MaxInt64))
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestRecoveryHeightDropsStreamGraceOnceSettled(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), 3)
	mgr.Keeper().SetMimir(ctx, constants.StreamingSwapMaxLength.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.StreamingLimitSwapMaxAge.String(), 200)

	// A market swap requesting the maximum stream lifetime is normalized to a
	// single slice, yet the queue state keeps the requested interval. While it
	// is still scheduled the grace applies; once it settles only the normal
	// recovery window may, otherwise its pools stay locked for a whole interval
	// after the one slice it ever gets has already run.
	market := fixture.msg
	market.SwapType = types.SwapType_market
	market.State.Interval = 100
	market.State.Quantity = 1
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_RESERVED), Equals, ctx.BlockHeight()+110)
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_EXECUTING), Equals, ctx.BlockHeight()+110)
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_SETTLED), Equals, ctx.BlockHeight()+30)
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_STAGED), Equals, ctx.BlockHeight()+30)

	// A finalized tombstone still owns live advanced-queue work, so it keeps
	// the grace instead of being re-inspected every normal window.
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), market, types.MempoolSwapStatus_FINALIZED), Equals, ctx.BlockHeight()+110)

	// The limit TTL is the same kind of forward schedule and is dropped too.
	limit := fixture.msg
	limit.SwapType = types.SwapType_limit
	limit.InitialBlockHeight = ctx.BlockHeight()
	limit.State.Interval = math.MaxUint64
	limit.State.Quantity = 1
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), limit, types.MempoolSwapStatus_EXECUTING), Equals, ctx.BlockHeight()+210)
	c.Check(mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), limit, types.MempoolSwapStatus_SETTLED), Equals, ctx.BlockHeight()+30)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestSettledOneSliceSwapPersistsNormalRecoveryWindow(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), 3)
	mgr.Keeper().SetMimir(ctx, constants.StreamingSwapMaxLength.String(), 100)

	queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	lastHeight := ctx.BlockHeight()
	// The queue only runs a slice on an interval boundary, so step forward one
	// whole interval: this is the single slice such a swap ever gets.
	ctx = ctx.WithBlockHeight(lastHeight + 100)
	queued.State.Quantity = 1
	queued.State.Interval = 100
	queued.State.Deposit = queued.Tx.Coins[0].Amount
	queued.State.In = cosmos.ZeroUint()
	queued.State.Out = cosmos.ZeroUint()
	queued.State.Count = 0
	queued.State.LastHeight = lastHeight
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queued), IsNil)

	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)

	record, found, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(mempoolSwapRecoveryScheduleDone(record.Status), Equals, true)
	// 30 blocks, not the 110 the retained interval would otherwise impose.
	c.Check(record.NextRecoveryHeight, Equals, ctx.BlockHeight()+30)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestRagnarokDrainsQueueOnlyLifecycleBeforeStageTwo(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	mgr.Keeper().SetRagnarokBlockHeight(ctx, 1)
	mgr.Keeper().SetRagnarokNth(ctx, 0)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	// This lifecycle has a reservation and queued work but no executed pool
	// effect. Overlay-only preflight would miss it, then Ragnarok's trading halt
	// would prevent it from ever reaching terminal settlement.
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(record.PendingEffects.Pools, HasLen, 0)
	c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil)

	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	nth, err := mgr.Keeper().GetRagnarokNth(ctx)
	c.Assert(err, IsNil)
	c.Check(nth, Equals, int64(0), Commentf("the drain block must not also advance Ragnarok"))

	// The following clean pass may safely begin Stage2.
	c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil)
	nth, err = mgr.Keeper().GetRagnarokNth(ctx)
	c.Assert(err, IsNil)
	c.Check(nth, Equals, int64(1))
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestRagnarokDrainsExecutedCrossAssetLifecycle(c *C) {
	for _, retry := range []bool{false, true} {
		ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
		targetPool := fixture.sourcePool
		targetPool.Asset = common.ETHAsset
		c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

		msg := fixture.msg
		msg.TargetAsset = common.ETHAsset
		msg.Destination = GetRandomETHAddress()
		msg.Tx.Memo = fmt.Sprintf("=:ETH.ETH:%s:0/1/2", msg.Destination)
		fixture.voter.Tx.Tx = msg.Tx
		mgr.Keeper().SetObservedTxInVoter(ctx, fixture.voter)
		record := newMempoolSwapLifecycle(ctx, mgr.Keeper(), fixture.voter, msg, fixture.reservedTOR)
		c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, msg), IsNil)

		// Execute one real queue slice, leaving pool effects that require an
		// ETH->BTC internal swap to unwind when shutdown begins.
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		record, found, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, true)
		c.Assert(record.Status, Equals, types.MempoolSwapStatus_EXECUTING)
		c.Assert(record.PendingEffects.Pools, HasLen, 2)
		c.Assert(record.LatestSwap.State.Out.IsZero(), Equals, false)

		if retry {
			// A previous failed drain leaves this status for the recovery worker.
			record.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
			record.NextRecoveryHeight = ctx.BlockHeight()
			c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		}
		mgr.Keeper().SetRagnarokBlockHeight(ctx, 1)
		mgr.Keeper().SetRagnarokNth(ctx, 0)
		validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
		c.Assert(mgr.Keeper().IsTradingHalt(ctx, &msg), Equals, true)
		c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil, Commentf("retry=%t", retry))
		nth, err := mgr.Keeper().GetRagnarokNth(ctx)
		c.Assert(err, IsNil)
		c.Check(nth, Equals, int64(0), Commentf("the drain block must not advance Ragnarok"))
		if retry {
			c.Assert(mgr.Keeper().HasMempoolSwap(ctx, msg.Tx.ID, msg.Index), Equals, true)
			slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
			c.Assert(slasher.recoverDueMempoolSwaps(ctx, mgr, 10, 100), IsNil)
		}

		c.Check(mgr.Keeper().HasMempoolSwap(ctx, msg.Tx.ID, msg.Index), Equals, false)
		c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
		c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
		c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
		for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
			_, hasOwner, ownerErr := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, asset)
			c.Assert(ownerErr, IsNil)
			c.Check(hasOwner, Equals, false)
		}

		// Stage two can begin on the next pass, with trading still halted.
		c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil)
		nth, err = mgr.Keeper().GetRagnarokNth(ctx)
		c.Assert(err, IsNil)
		c.Check(nth, Equals, int64(1))
		c.Check(mgr.Keeper().IsTradingHalt(ctx, &msg), Equals, true)
	}
}

// An unfinalized cross-pool rollback must return both pools to their exact
// pre-swap balances. A reverse AMM swap would instead reprice the output and
// leave the target pool with the slip of both legs, paid by the source pool.
func (s *MempoolSwapLifecycleIntegrationSuite) TestUnfinalizedCrossPoolRollbackRestoresBothPools(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	targetPool := fixture.sourcePool
	targetPool.Asset = common.ETHAsset
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	msg := fixture.msg
	msg.TargetAsset = common.ETHAsset
	msg.Destination = GetRandomETHAddress()
	msg.Tx.Memo = fmt.Sprintf("=:ETH.ETH:%s:0/1/2", msg.Destination)
	fixture.voter.Tx.Tx = msg.Tx
	mgr.Keeper().SetObservedTxInVoter(ctx, fixture.voter)
	record := newMempoolSwapLifecycle(ctx, mgr.Keeper(), fixture.voter, msg, fixture.reservedTOR)
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, msg), IsNil)
	asgardRuneBefore := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native())

	// Execute one real speculative BTC->ETH slice.
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	record, found, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(record.PendingEffects.Pools, HasLen, 2)
	c.Assert(record.LatestSwap.State.Out.IsZero(), Equals, false)
	btcAfterSwap, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btcAfterSwap.BalanceRune.LT(fixture.sourcePool.BalanceRune), Equals, true)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, msg.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(rollbackMempoolSwapCached(ctx, mgr.Keeper(), mgr, voter), IsNil)

	for _, expected := range []Pool{fixture.sourcePool, targetPool} {
		pool, poolErr := mgr.Keeper().GetPool(ctx, expected.Asset)
		c.Assert(poolErr, IsNil)
		c.Check(pool.BalanceRune.String(), Equals, expected.BalanceRune.String(), Commentf("%s rune", expected.Asset))
		c.Check(pool.BalanceAsset.String(), Equals, expected.BalanceAsset.String(), Commentf("%s asset", expected.Asset))
		overlay, overlayErr := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, expected.Asset)
		c.Assert(overlayErr, IsNil)
		c.Check(mempoolSwapPoolBalanceOverlayIsZero(overlay), Equals, true)
	}
	// The system fee never left Asgard, and no reverse swap moved RUNE.
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native()).String(), Equals, asgardRuneBefore.String())
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, msg.Tx.ID, msg.Index), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

// setupMempoolSwapLifecycleFixture creates one admitted, unexecuted BTC->RUNE
// mempool swap. The queue entry is deliberately present alongside the durable
// lifecycle record so each test can remove or advance whichever representation
// is relevant to the transition it exercises.
func setupMempoolSwapLifecycleFixture(c *C) (cosmos.Context, *Mgrs, mempoolSwapLifecycleFixture) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 1_000_000*common.One)
	mgr.Keeper().SetMimir(ctx, constants.StreamingSwapPause.String(), 0)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	sourcePool.LPUnits = sourcePool.BalanceRune
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	vault := NewVault(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.BTCChain, common.THORChain}.Strings(),
		nil,
	)
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddress, err := vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	destination := GetRandomTHORAddress()
	inboundAmount := cosmos.NewUint(common.One)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		vaultAddress,
		common.Coins{common.NewCoin(common.BTCAsset, inboundAmount)},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		fmt.Sprintf("=:THOR.RUNE:%s:0/1/2", destination),
	)
	observed := NewObservedTx(tx, ctx.BlockHeight(), vault.PubKey, 0)
	observed.Status = common.Status_done
	observed.Signers = []string{node.NodeAddress.String()}
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = 0
	voter.MempoolSwapTOR = cosmos.NewUint(25 * common.One)

	msg := NewMsgSwap(
		tx,
		common.RuneAsset(),
		destination,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		2,
		1,
		types.SwapVersion_v2,
		node.NodeAddress,
	)
	msg.State.LastHeight = ctx.BlockHeight() - 1

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	record := newMempoolSwapLifecycle(ctx, mgr.Keeper(), voter, *msg, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *msg), IsNil)

	return ctx, mgr, mempoolSwapLifecycleFixture{
		node:        node,
		vault:       vault,
		voter:       voter,
		msg:         *msg,
		sourcePool:  sourcePool,
		reservedTOR: voter.MempoolSwapTOR,
	}
}

// settleMempoolSwapWithFailedOutbound drives a complete advanced-queue leg
// through production settlement while injecting a TxOut failure. The output
// cannot be sent, so the unfinalized lifecycle must retain both the rollback
// snapshot and a deferred cleanup instead of donating/burning immediately.
func settleMempoolSwapWithFailedOutbound(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	fixture mempoolSwapLifecycleFixture,
) types.MempoolSwap {
	queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	queued.State.Quantity = 1
	queued.State.Interval = 1
	queued.State.Deposit = queued.Tx.Coins[0].Amount
	queued.State.In = cosmos.ZeroUint()
	queued.State.Out = cosmos.ZeroUint()
	queued.State.Count = 0
	queued.State.LastHeight = ctx.BlockHeight() - 1
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queued), IsNil)

	failingMgr := NewDummyMgrWithKeeper(mgr.Keeper())
	failingMgr.txOutStore = NewTxStoreFailDummy()
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, failingMgr, false), IsNil)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(record.Status, Equals, types.MempoolSwapStatus_SETTLED)
	c.Assert(record.PendingEffects.Cleanups, HasLen, 1)
	c.Check(record.LatestSwap.State.Out.IsZero(), Equals, false)
	return record
}

func attestMempoolSwapLifecycleErrata(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	fixture mempoolSwapLifecycleFixture,
) keeperErrataResult {
	voter := NewErrataTxVoter(fixture.voter.TxID, common.BTCChain)
	err := processErrataTxAttestation(
		ctx,
		mgr,
		&voter,
		fixture.node.NodeAddress,
		NodeAccounts{fixture.node},
		&common.ErrataTx{Id: fixture.voter.TxID, Chain: common.BTCChain},
		true,
	)
	c.Assert(err, IsNil)
	return keeperErrataResult{blockHeight: voter.BlockHeight}
}

// keeperErrataResult intentionally exposes only the consensus marker needed by
// these tests. The errata voter itself is owned by the outer handler in normal
// execution; lifecycle assertions are made against the canonical stores.
type keeperErrataResult struct {
	blockHeight int64
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestAdmissionHonorsLifecycleCountBound(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapMaxCount.String(), 1)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)

	vaultAddress, err := fixture.vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	secondTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		vaultAddress,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		fmt.Sprintf("=:THOR.RUNE:%s", GetRandomTHORAddress()),
	)
	observed := NewObservedTx(secondTx, ctx.BlockHeight(), fixture.vault.PubKey, 0)
	observed.Status = common.Status_done
	observed.Signers = []string{fixture.node.NodeAddress.String()}
	voter := NewObservedTxVoter(secondTx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	err = handleObservedTxInQuorum(
		ctx,
		mgr,
		fixture.node.NodeAddress,
		NodeAccounts{fixture.node},
		NewInternalHandler(mgr),
		observed,
		voter,
		[]cosmos.AccAddress{fixture.node.NodeAddress},
		true,
	)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, secondTx.ID, 0), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, secondTx.ID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(fixture.reservedTOR), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestAdmissionPersistsNormalizedLimitTiming(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapPoolCapBPS.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.StreamingLimitSwapMaxAge.String(), 30)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	vaultAddress, err := fixture.vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		vaultAddress,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		fmt.Sprintf("=<:THOR.RUNE:%s:1/1000/2", GetRandomTHORAddress()),
	)
	observed := NewObservedTx(tx, ctx.BlockHeight(), fixture.vault.PubKey, 0)
	observed.Status = common.Status_done
	observed.Signers = []string{fixture.node.NodeAddress.String()}
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	err = handleObservedTxInQuorum(
		ctx,
		mgr,
		fixture.node.NodeAddress,
		NodeAccounts{fixture.node},
		NewInternalHandler(mgr),
		observed,
		voter,
		[]cosmos.AccAddress{fixture.node.NodeAddress},
		true,
	)
	c.Assert(err, IsNil)
	queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, tx.ID, 0)
	c.Assert(err, IsNil)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, tx.ID, 0)
	c.Assert(err, IsNil)
	c.Check(queued.InitialBlockHeight, Equals, ctx.BlockHeight())
	c.Check(queued.State.Interval, Equals, uint64(30))
	c.Check(record.LatestSwap, DeepEquals, queued)
	c.Check(record.NextRecoveryHeight, Equals, mempoolSwapRecoveryHeight(ctx, mgr.Keeper(), queued, record.Status))
	c.Check(record.NextRecoveryHeight >= queued.InitialBlockHeight+30, Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestDeferredPoolCycleAdvancesOwnerAndClearPoolTraffic(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	book := newSwapQueueAdv(mgr.Keeper())
	poolMgr := newPoolMgr()

	// Execute the first stream slice normally so this lifecycle becomes the
	// established owner of a real BTC pool overlay.
	c.Assert(book.EndBlock(ctx, mgr, false), IsNil)
	ownerRecord, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(ownerRecord.LatestSwap.State.Count, Equals, uint64(1))
	c.Check(mempoolSwapOwnsPoolCycleOverlay(ownerRecord), Equals, true)

	// Queue an unrelated confirmed swap through a disjoint pool. It can neither
	// observe the owner's reversible depth nor create an overlay of its own, so
	// a deferred cycle must not stall it for the owner's recovery window.
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	ethPool.LPUnits = ethPool.BalanceRune
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	unrelatedTx := GetRandomTx()
	unrelatedTx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)))
	unrelated := NewMsgSwap(
		unrelatedTx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *unrelated), IsNil)

	// A confirmed swap through the owner's BTC pool would observe reversible
	// depth, and preempting the owner mid-pass would let the cycle run in a
	// block that already executed swaps. It must wait for the clean block.
	blockedTx := GetRandomTx()
	blockedTx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)))
	blocked := NewMsgSwap(
		blockedTx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *blocked), IsNil)

	const poolCycle int64 = 10
	mgr.Keeper().SetMimir(ctx, constants.PoolCycle.String(), poolCycle)
	mgr.Keeper().SetMimir(ctx, constants.MaxAvailablePools.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.MinRunePoolDepth.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.StagedPoolCost.String(), 0)
	boundaryHeight := ((ctx.BlockHeight() / poolCycle) + 1) * poolCycle
	boundaryCtx := ctx.WithBlockHeight(boundaryHeight)

	// The configured boundary retains the historical all-queue pause, then
	// PoolMgr persists a retry because the BTC overlay is still reversible.
	pairs, pools := book.getAssetPairs(boundaryCtx)
	boundaryItems, err := book.FetchQueue(boundaryCtx, mgr, pairs, pools, nil)
	c.Assert(err, IsNil)
	c.Check(boundaryItems, HasLen, 0)
	c.Assert(poolMgr.EndBlock(boundaryCtx, mgr), IsNil)
	c.Check(getPoolCycleRetryHeight(boundaryCtx, mgr), Equals, boundaryHeight)

	// On the retry block the existing owner and the unrelated confirmed swap are
	// visible; the confirmed swap through the owner's pool is not.
	retryCtx := boundaryCtx.WithBlockHeight(boundaryHeight + 1)
	pairs, pools = book.getAssetPairs(retryCtx)
	retryItems, err := book.FetchQueue(retryCtx, mgr, pairs, pools, nil)
	c.Assert(err, IsNil)
	c.Assert(retryItems, HasLen, 2)
	c.Check(retryItems.HasItem(fixture.voter.TxID), Equals, true)
	c.Check(retryItems.HasItem(unrelated.Tx.ID), Equals, true)
	c.Check(retryItems.HasItem(blocked.Tx.ID), Equals, false)
	c.Assert(book.EndBlock(retryCtx, mgr, false), IsNil)

	ownerRecord, _, err = mgr.Keeper().GetMempoolSwap(retryCtx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(ownerRecord.LatestSwap.State.Count, Equals, uint64(2))
	c.Check(mempoolSwapOwnsPoolCycleOverlay(ownerRecord), Equals, true)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(retryCtx, unrelated.Tx.ID, int(unrelated.Index)), Equals, false,
		Commentf("the unrelated single-slice swap must execute and settle during the retry"))
	blockedAfter, err := mgr.Keeper().GetAdvSwapQueueItem(retryCtx, blocked.Tx.ID, int(blocked.Index))
	c.Assert(err, IsNil)
	c.Check(blockedAfter.State.Count, Equals, uint64(0))
	c.Check(blockedAfter.State.FailedSwaps, HasLen, 0)

	// PoolMgr runs after the queue and must still see the owner's retained
	// lifecycle overlay, so no cycle write occurs in the progress block.
	c.Assert(poolMgr.EndBlock(retryCtx, mgr), IsNil)
	c.Check(getPoolCycleRetryHeight(retryCtx, mgr), Equals, boundaryHeight)

	// Model finality removing reversible ownership before the next EndBlock.
	// The retry block then runs no swap at all, allowing PoolMgr to cycle against
	// finalized state and clear the persisted retry.
	finalizeMempoolSwapPoolGateRecord(c, retryCtx, mgr, &ownerRecord)
	cleanCtx := retryCtx.WithBlockHeight(boundaryHeight + 2)
	pairs, pools = book.getAssetPairs(cleanCtx)
	cleanItems, err := book.FetchQueue(cleanCtx, mgr, pairs, pools, nil)
	c.Assert(err, IsNil)
	c.Check(cleanItems, HasLen, 0)
	c.Assert(poolMgr.EndBlock(cleanCtx, mgr), IsNil)
	c.Check(getPoolCycleRetryHeight(cleanCtx, mgr), Equals, int64(0))

	// Normal queue traffic resumes only after the cycle succeeds.
	resumedCtx := cleanCtx.WithBlockHeight(boundaryHeight + 3)
	pairs, pools = book.getAssetPairs(resumedCtx)
	resumedItems, err := book.FetchQueue(resumedCtx, mgr, pairs, pools, nil)
	c.Assert(err, IsNil)
	c.Check(resumedItems.HasItem(blocked.Tx.ID), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestErrataBeforeExecutionClearsEveryOwner(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)

	result := attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)
	c.Check(result.blockHeight, Equals, ctx.BlockHeight())

	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
	c.Check(stored.HasReservedMempoolSwapTOR(), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool, DeepEquals, fixture.sourcePool)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestErrataMidStreamRollsBackProductionProgress(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	progressed, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(progressed.Status, Equals, types.MempoolSwapStatus_EXECUTING)
	c.Check(progressed.LatestSwap.State.Count, Equals, uint64(1))
	c.Check(len(progressed.PendingEffects.Pools) > 0, Equals, true)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, true)
	poolAfterLeg, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(poolAfterLeg, Not(DeepEquals), fixture.sourcePool)

	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)
	rolledBack, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolledBack, DeepEquals, fixture.sourcePool)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestErrataAfterStagingClearsOutboundAndPoolProgress(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	queued.State.Quantity = 1
	queued.State.Interval = 1
	queued.State.Deposit = queued.Tx.Coins[0].Amount
	queued.State.LastHeight = ctx.BlockHeight() - 1
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queued), IsNil)

	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(record.Status, Equals, types.MempoolSwapStatus_STAGED)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, fixture.voter.TxID), Equals, true)
	poolAfterSettlement, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(poolAfterSettlement, Not(DeepEquals), fixture.sourcePool)

	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)
	rolledBack, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolledBack, DeepEquals, fixture.sourcePool)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, fixture.voter.TxID), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestDeepReorgAfterMidStreamFinalityUsesTombstone(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.UpdatedVault = true
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)

	tombstone, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(tombstone.Status, Equals, types.MempoolSwapStatus_FINALIZED)
	c.Check(tombstone.ReservedTor.IsZero(), Equals, true)
	c.Check(len(tombstone.PendingEffects.Pools) > 0, Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	queueBefore, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)

	vaultBefore, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
	c.Assert(err, IsNil)
	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)

	rolledBack, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolledBack, DeepEquals, fixture.sourcePool)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	vaultAfter, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
	c.Assert(err, IsNil)
	expectedVaultAsset := common.SafeSub(vaultBefore.GetCoin(common.BTCAsset).Amount, fixture.voter.Tx.Tx.Coins[0].Amount)
	c.Check(vaultAfter.GetCoin(common.BTCAsset).Amount.Equal(expectedVaultAsset), Equals, true)

	// The reverted voter is an unconditional queue stop even if stale work were
	// accidentally reinserted after errata.
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queueBefore), IsNil)
	poolBeforeReplay, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	poolAfterReplay, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfterReplay, DeepEquals, poolBeforeReplay)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestMarkerlessSettledErrataUsesLifecycleSnapshot(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	record := settleMempoolSwapWithFailedOutbound(c, ctx, mgr, fixture)
	cleanupKey := record.PendingEffects.Cleanups[0].CleanupKey
	c.Check(mgr.Keeper().HasRefundCleanup(ctx, cleanupKey), Equals, false,
		Commentf("unfinalized settlement must not perform irreversible cleanup"))
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)

	forwardPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(forwardPool, Not(DeepEquals), fixture.sourcePool)

	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)
	rolledBackPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolledBackPool, DeepEquals, fixture.sourcePool)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().HasRefundCleanup(ctx, cleanupKey), Equals, false,
		Commentf("errata must discard the deferred cleanup"))
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestMarkerlessCleanupFinalizesExactlyOnce(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	record := settleMempoolSwapWithFailedOutbound(c, ctx, mgr, fixture)
	cleanupKey := record.PendingEffects.Cleanups[0].CleanupKey
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	c.Check(mgr.Keeper().HasRefundCleanup(ctx, cleanupKey), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	reserveAfter := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveAfter.GT(reserveBefore), Equals, true)

	// A replay cannot repeat either cleanup or the rest of the effect journal.
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveAfter), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestErrataRollbackFailureStopsAdvancedQueue(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)

	// Missing inbound coins fails rollback after the cached path has already
	// touched lifecycle status and usage. The outer errata transaction must
	// discard those economic writes and persist only terminal stop markers.
	broken := fixture.voter
	broken.Tx.Tx.Coins = nil
	mgr.Keeper().SetObservedTxInVoter(ctx, broken)
	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	queueBefore, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)

	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(record.Status, Equals, types.MempoolSwapStatus_ROLLBACK_PENDING)
	c.Check(record.NextRecoveryHeight, Equals, ctx.BlockHeight()+1)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(fixture.reservedTOR), Equals, true)
	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
	c.Check(stored.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, true)
	c.Check(newSwapQueueAdv(mgr.Keeper()).isSwapReady(ctx, queueBefore), Equals, false)

	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	queueAfter, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(queueAfter, DeepEquals, queueBefore)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestCompletionReplaysReservationEffectsAndOutboundExactlyOnce(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.Keeper().RemoveAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), IsNil)

	feeRune := cosmos.NewUint(17)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_STAGED
	record.PendingEffects.Affiliates = []types.MempoolSwapAffiliateEffect{{
		Thorname: lifecycleTestAffiliate,
		FeeRune:  feeRune,
	}}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	recipient := GetRandomTHORAddress()
	recipientAcc, err := recipient.AccAddress()
	c.Assert(err, IsNil)
	stagedAmount := cosmos.NewUint(7 * common.One)
	staged := TxOutItem{
		Chain:       common.THORChain,
		InHash:      fixture.voter.TxID,
		ToAddress:   recipient,
		VaultPubKey: fixture.vault.PubKey,
		Coin:        common.NewCoin(common.RuneAsset(), stagedAmount),
		Memo:        NewOutboundMemo(fixture.voter.TxID).String(),
	}
	voter := fixture.voter
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Actions = []TxOutItem{staged}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, staged, ctx.BlockHeight()+5), IsNil)

	before := mgr.Keeper().GetBalance(ctx, recipientAcc).AmountOf(common.RuneNative.Native())
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	afterFirst := mgr.Keeper().GetBalance(ctx, recipientAcc).AmountOf(common.RuneNative.Native())
	c.Check(afterFirst.Sub(before).Equal(cosmos.NewIntFromBigInt(stagedAmount.BigInt())), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, fixture.voter.TxID), Equals, false)
	fees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(fees, HasLen, 1)
	c.Check(fees[0].Thorname, Equals, lifecycleTestAffiliate)
	c.Check(fees[0].Fee.Equal(feeRune), Equals, true)

	// Replay the successful finalization with the stale pre-completion voter.
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	afterReplay := mgr.Keeper().GetBalance(ctx, recipientAcc).AmountOf(common.RuneNative.Native())
	c.Check(afterReplay.Equal(afterFirst), Equals, true)
	fees = mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(fees, HasLen, 1)
	c.Check(fees[0].Fee.Equal(feeRune), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestLateCompletionFailurePreservesPreCallState(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.Keeper().RemoveAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), IsNil)

	feeRune := cosmos.NewUint(19)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_STAGED
	record.PendingEffects.Affiliates = []types.MempoolSwapAffiliateEffect{{
		Thorname: lifecycleTestAffiliate,
		FeeRune:  feeRune,
	}}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	stagedHeight := ctx.BlockHeight() + 5
	staged := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      fixture.voter.TxID,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: fixture.vault.PubKey,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One)),
		Memo:        NewOutboundMemo(fixture.voter.TxID).String(),
	}
	voter := fixture.voter
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Actions = []TxOutItem{staged}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, staged, stagedHeight), IsNil)

	// Corrupt only the aggregate reservation so completion gets through effect
	// application and external TxOut materialization, then fails at the final
	// release check. The outer cache must discard every preceding mutation.
	understatedUsage := fixture.reservedTOR.Sub(cosmos.OneUint())
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, understatedUsage)

	err = completeMempoolSwapAtomic(ctx, mgr, voter)
	c.Assert(err, ErrorMatches, "mempool swap lifecycle reservation .* exceeds aggregate usage .* for .*")

	current, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(current.Status, Equals, types.MempoolSwapStatus_STAGED)
	c.Assert(current.PendingEffects.Affiliates, HasLen, 1)
	c.Check(current.PendingEffects.Affiliates[0].FeeRune.Equal(feeRune), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(understatedUsage), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, fixture.voter.TxID), Equals, true)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0)
	txOut, err := mgr.Keeper().GetTxOut(ctx, stagedHeight)
	c.Assert(err, IsNil)
	c.Check(txOut.TxArray, HasLen, 0)
	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stored.HasReservedMempoolSwapTOR(), Equals, true)
	c.Check(stored.Actions, HasLen, 1)

	// Repair the aggregate and verify that a successful retry—and then a replay
	// of that success—materializes every effect exactly once.
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, fixture.reservedTOR)
	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	txOut, err = mgr.Keeper().GetTxOut(ctx, stagedHeight)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 1)
	fees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(fees, HasLen, 1)
	c.Check(fees[0].Fee.Equal(feeRune), Equals, true)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, fixture.voter.TxID), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	c.Assert(completeMempoolSwapAtomic(ctx, mgr, voter), IsNil)
	txOut, err = mgr.Keeper().GetTxOut(ctx, stagedHeight)
	c.Assert(err, IsNil)
	c.Check(txOut.TxArray, HasLen, 1)
	fees = mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(fees, HasLen, 1)
	c.Check(fees[0].Fee.Equal(feeRune), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestFinalizedLiveLifecycleErrataDebitsVaultOnlyOnce(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_SETTLED
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	voter := fixture.voter
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.UpdatedVault = true
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	vaultBefore, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
	c.Assert(err, IsNil)
	assetBefore := vaultBefore.GetCoin(common.BTCAsset).Amount
	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	attestMempoolSwapLifecycleErrata(c, ctx, mgr, fixture)

	vaultAfter, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
	c.Assert(err, IsNil)
	expectedVaultAsset := common.SafeSub(assetBefore, fixture.voter.Tx.Tx.Coins[0].Amount)
	c.Check(vaultAfter.GetCoin(common.BTCAsset).Amount.Equal(expectedVaultAsset), Equals, true)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore,
		Commentf("lifecycle rollback must not fall through to ordinary finalized errata pool accounting"))
	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
	c.Check(stored.UpdatedVault, Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	outbounds, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outbounds, HasLen, 0, Commentf("errata of a finalized live lifecycle must not schedule an ordinary refund"))
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestReferenceReadCannotEnterSpeculativePath(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 1_000_000*common.One)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnMaxUse.String(), 1)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	vault := NewVault(
		ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), nil,
	)
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddress, err := vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	const reference = "40123"
	resolved := fmt.Sprintf("=:THOR.RUNE:%s:0/1/2", GetRandomTHORAddress())
	mgr.Keeper().SetReferenceMemo(ctx, NewReferenceMemo(common.BTCAsset, resolved, reference, ctx.BlockHeight()-1))
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		vaultAddress,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"r:"+reference,
	)
	observed := NewObservedTx(tx, ctx.BlockHeight(), vault.PubKey, 0)
	observed.Status = common.Status_done
	observed.Signers = []string{node.NodeAddress.String()}
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()

	rawMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), tx.Memo)
	c.Assert(err, IsNil)
	c.Check(rawMemo.IsType(TxReferenceReadMemo), Equals, true)
	c.Check(canUseMempoolSwap(ctx, mgr, tx, rawMemo), Equals, false)
	resolvedMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), resolved)
	c.Assert(err, IsNil)
	c.Check(canUseMempoolSwap(ctx, mgr, tx, resolvedMemo), Equals, true,
		Commentf("the registered memo would be eligible if reference resolution happened before the raw-memo gate"))

	err = handleObservedTxInQuorum(
		ctx,
		mgr,
		node.NodeAddress,
		NodeAccounts{node},
		NewInternalHandler(mgr),
		observed,
		voter,
		[]cosmos.AccAddress{node.NodeAddress},
		true,
	)
	c.Assert(err, IsNil)
	storedReference, err := mgr.Keeper().GetReferenceMemo(ctx, common.BTCAsset, reference)
	c.Assert(err, IsNil)
	c.Check(storedReference.GetUsageCount(), Equals, int64(0))
	c.Check(storedReference.HasBeenUsedBy(tx.ID), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, tx.ID, 0), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, tx.ID, 0), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestInactiveVaultLifecycleIsNotQueueReadyBeforeRecovery(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	queueItem, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	queue := newSwapQueueAdv(mgr.Keeper())
	c.Check(queue.isSwapReady(ctx, queueItem), Equals, true)

	vault := fixture.vault
	vault.Status = InactiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	startMempoolSwapVaultRecovery(ctx, mgr.Keeper(), vault)
	jobs, err := mgr.Keeper().GetMempoolSwapVaultRecoveries(ctx, 10)
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	cursor, err := mgr.Keeper().GetMempoolSwapVaultRecovery(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Check(cursor, Equals, "", Commentf("the bounded vault-recovery job has not processed this record yet"))
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, true)
	c.Check(queue.isSwapReady(ctx, queueItem), Equals, false)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestInactiveVaultRefundFailureIsAtomic(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	vault := fixture.vault
	vault.Status = InactiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	queueBefore, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	recordBefore, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	outboundsBefore, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)

	failingEvents := failMempoolRefundEventManager{EventManager: mgr.EventMgr()}
	override := mempoolSwapEventManagerOverride{Manager: mgr, eventMgr: failingEvents}
	err = rollbackAndRefundInactiveVaultMempoolSwap(ctx, override, voter.Tx, voter)
	c.Assert(err, ErrorMatches, "fail to refund inactive-vault mempool swap: fail to emit refund event: injected refund event failure")

	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
	queueAfter, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(queueAfter, DeepEquals, queueBefore)
	recordAfter, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(recordAfter, DeepEquals, recordBefore)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(fixture.reservedTOR), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	storedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(storedVoter, DeepEquals, voter)
	outboundsAfter, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(outboundsAfter, DeepEquals, outboundsBefore)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestRefundOnlyCloutReclaimFailureIsAtomic(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	baseKeeper := mgr.Keeper()

	sender := fixture.voter.Tx.Tx.FromAddress
	cloutBefore := NewSwapperClout(sender)
	cloutBefore.Spent = cosmos.NewUint(100)
	c.Assert(baseKeeper.SetSwapperClout(ctx, cloutBefore), IsNil)

	spent := cosmos.NewUint(80)
	refund := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   sender,
		VaultPubKey: fixture.vault.PubKey,
		Coin:        fixture.voter.Tx.Tx.Coins[0],
		Memo:        NewRefundMemo(fixture.voter.TxID).String(),
		InHash:      fixture.voter.TxID,
		CloutSpent:  &spent,
	}
	voter := fixture.voter
	voter.Actions = []TxOutItem{refund}
	baseKeeper.SetObservedTxInVoter(ctx, voter)
	c.Assert(baseKeeper.SetStagedOutbound(ctx, refund, ctx.BlockHeight()+10), IsNil)

	recordBefore, _, err := baseKeeper.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	queueBefore, err := baseKeeper.GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	stagedBefore, err := baseKeeper.GetStagedOutbounds(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	poolBefore, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	failKeeper := &failSecondSetSwapperCloutKeeper{Keeper: baseKeeper}
	err = rollbackMempoolSwapAtomic(ctx, failKeeper, mgr, voter)
	c.Assert(err, ErrorMatches, "fail to reclaim staged clout on rollback: fail to save recipient clout: fail second swapper clout write")
	c.Check(failKeeper.setCalls, Equals, 2)

	// Releasing TOR and marking the lifecycle ROLLBACK_PENDING happen before
	// clout reclaim. The enclosing cache must discard both when reclaim fails.
	recordAfter, _, err := baseKeeper.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(recordAfter, DeepEquals, recordBefore)
	c.Check(baseKeeper.GetMempoolSwapUsageTOR(ctx).Equal(fixture.reservedTOR), Equals, true)
	c.Check(baseKeeper.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	storedVoter, err := baseKeeper.GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(storedVoter, DeepEquals, voter)
	queueAfter, err := baseKeeper.GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(queueAfter, DeepEquals, queueBefore)
	stagedAfter, err := baseKeeper.GetStagedOutbounds(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stagedAfter, DeepEquals, stagedBefore)
	poolAfter, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
	cloutAfter, err := baseKeeper.GetSwapperClout(ctx, sender)
	c.Assert(err, IsNil)
	c.Check(cloutAfter, DeepEquals, cloutBefore)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestOutputCloutReclaimFailureIsAtomic(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	baseKeeper := mgr.Keeper()

	// Model a completed speculative BTC->RUNE transition. Rollback restores
	// this pool before reclaiming clout, so a reclaim error exercises a late
	// failure after real pool and reservation mutations have already occurred.
	record, _, err := baseKeeper.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Assert(record.LatestSwap.State, NotNil)
	deposit := fixture.voter.Tx.Tx.Coins[0].Amount
	runeOutput := cosmos.NewUint(common.One)
	record.Status = types.MempoolSwapStatus_SETTLED
	record.LatestSwap.State.In = deposit
	record.LatestSwap.State.Out = runeOutput
	c.Assert(baseKeeper.SetMempoolSwap(ctx, record), IsNil)

	poolBefore, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	poolBefore.BalanceRune = poolBefore.BalanceRune.Sub(runeOutput)
	poolBefore.BalanceAsset = poolBefore.BalanceAsset.Add(deposit)
	c.Assert(baseKeeper.SetPool(ctx, poolBefore), IsNil)

	sender := fixture.voter.Tx.Tx.FromAddress
	recipient := GetRandomTHORAddress()
	senderCloutBefore := NewSwapperClout(sender)
	senderCloutBefore.Spent = cosmos.NewUint(100)
	recipientCloutBefore := NewSwapperClout(recipient)
	recipientCloutBefore.Spent = cosmos.NewUint(100)
	c.Assert(baseKeeper.SetSwapperClout(ctx, senderCloutBefore), IsNil)
	c.Assert(baseKeeper.SetSwapperClout(ctx, recipientCloutBefore), IsNil)

	spent := cosmos.NewUint(80)
	output := TxOutItem{
		Chain:      common.THORChain,
		ToAddress:  recipient,
		Coin:       common.NewCoin(common.RuneAsset(), runeOutput),
		Memo:       NewOutboundMemo(fixture.voter.TxID).String(),
		InHash:     fixture.voter.TxID,
		ModuleName: ModuleName,
		CloutSpent: &spent,
	}
	voter := fixture.voter
	voter.Actions = []TxOutItem{output}
	baseKeeper.SetObservedTxInVoter(ctx, voter)
	c.Assert(baseKeeper.SetStagedOutbound(ctx, output, ctx.BlockHeight()+10), IsNil)

	recordBefore, _, err := baseKeeper.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	queueBefore, err := baseKeeper.GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	stagedBefore, err := baseKeeper.GetStagedOutbounds(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)

	failKeeper := &failSecondSetSwapperCloutKeeper{Keeper: baseKeeper}
	err = rollbackMempoolSwapAtomic(ctx, failKeeper, mgr, voter)
	c.Assert(err, ErrorMatches, "fail to reclaim staged clout on rollback: fail to save recipient clout: fail second swapper clout write")
	c.Check(failKeeper.setCalls, Equals, 2)

	recordAfter, _, err := baseKeeper.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(recordAfter, DeepEquals, recordBefore)
	c.Check(baseKeeper.GetMempoolSwapUsageTOR(ctx).Equal(fixture.reservedTOR), Equals, true)
	c.Check(baseKeeper.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	storedVoter, err := baseKeeper.GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(storedVoter, DeepEquals, voter)
	queueAfter, err := baseKeeper.GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(queueAfter, DeepEquals, queueBefore)
	stagedAfter, err := baseKeeper.GetStagedOutbounds(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(stagedAfter, DeepEquals, stagedBefore)
	poolAfter, err := baseKeeper.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
	senderCloutAfter, err := baseKeeper.GetSwapperClout(ctx, sender)
	c.Assert(err, IsNil)
	c.Check(senderCloutAfter, DeepEquals, senderCloutBefore)
	recipientCloutAfter, err := baseKeeper.GetSwapperClout(ctx, recipient)
	c.Assert(err, IsNil)
	c.Check(recipientCloutAfter, DeepEquals, recipientCloutBefore)
}

// TestRefundOnlyStagedRollbackRestoresRetainedLifecycleOutput covers a partial
// stream whose target outbound failed to stage while its unused source refund
// staged successfully. Settlement retained the executed output on the lifecycle
// and removed the queue item. The refund is filtered out of the rollback
// actions, so rollback must still recover and unwind the lifecycle output
// rather than only deducting the credited source.
func (s *MempoolSwapLifecycleIntegrationSuite) TestRefundOnlyStagedRollbackRestoresRetainedLifecycleOutput(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	k := mgr.Keeper()

	deposit := fixture.voter.Tx.Tx.Coins[0].Amount
	executedIn := deposit.QuoUint64(2)
	runeOutput := cosmos.NewUint(common.One / 4)

	record, _, err := k.GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Assert(record.LatestSwap.State, NotNil)
	record.Status = types.MempoolSwapStatus_SETTLED
	record.LatestSwap.State.In = executedIn
	record.LatestSwap.State.Out = runeOutput
	// Journal the pool mutation applied below, as speculative execution does.
	poolEffect := newMempoolSwapPoolEffect(common.BTCAsset)
	poolEffect.BalanceRuneDecrease = runeOutput
	poolEffect.BalanceAssetIncrease = executedIn
	record.PendingEffects.Pools = []types.MempoolSwapPoolEffect{poolEffect}
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	c.Assert(k.RemoveAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), IsNil)

	pool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	pool.BalanceRune = pool.BalanceRune.Sub(runeOutput)
	pool.BalanceAsset = pool.BalanceAsset.Add(executedIn)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	refund := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   fixture.voter.Tx.Tx.FromAddress,
		VaultPubKey: fixture.vault.PubKey,
		Coin:        common.NewCoin(common.BTCAsset, deposit.Sub(executedIn)),
		Memo:        NewRefundMemo(fixture.voter.TxID).String(),
		InHash:      fixture.voter.TxID,
	}
	voter := fixture.voter
	voter.Actions = []TxOutItem{refund}
	k.SetObservedTxInVoter(ctx, voter)
	c.Assert(k.SetStagedOutbound(ctx, refund, ctx.BlockHeight()+10), IsNil)

	c.Assert(rollbackMempoolSwapAtomic(ctx, k, mgr, voter), IsNil)

	poolAfter, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceRune.String(), Equals, fixture.sourcePool.BalanceRune.String(),
		Commentf("retained lifecycle output must be returned to the pool"))
	c.Check(poolAfter.BalanceAsset.String(), Equals, fixture.sourcePool.BalanceAsset.String(),
		Commentf("only the executed source input may be deducted"))
	c.Check(k.HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false)
	c.Check(k.HasStagedOutbound(ctx, fixture.voter.TxID), Equals, false)
	c.Check(k.GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestQuoteSimulationPreemptsOwnedPoolWithoutMutatingState(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	owner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	liveRecord, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	livePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	quote := fixture.msg
	quote.Tx.ID = GetRandomTxHash()
	quote.State = &types.SwapState{Quantity: 1, Interval: 0}
	_, err = simulate(ctx, mgr, &quote)
	c.Assert(err, IsNil)

	afterOwner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(afterOwner, DeepEquals, owner)
	afterRecord, _, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(afterRecord, DeepEquals, liveRecord)
	afterPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(afterPool, DeepEquals, livePool)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, fixture.voter.TxID, 0), Equals, true)
}

type recordingMempoolEventManager struct {
	EventManager
	items []EmitEventItem
}

func (m *recordingMempoolEventManager) EmitEvent(ctx cosmos.Context, evt EmitEventItem) error {
	m.items = append(m.items, evt)
	return m.EventManager.EmitEvent(ctx, evt)
}

// TestRollbackPoolEventsMatchPoolBalanceChanges replays every pool-changing
// event a complete rollback emits and checks that the sum reproduces the
// actual before/after balances of each pool. Canonical swap events are
// suppressed for the reverse swap, so any pool movement left unreported
// (reverse swap, fee restoration) would leave indexers with drifted depths.
func (s *MempoolSwapLifecycleIntegrationSuite) TestRollbackPoolEventsMatchPoolBalanceChanges(c *C) {
	type poolFlow struct{ runeIn, runeOut, assetIn, assetOut cosmos.Uint }
	for _, crossAsset := range []bool{false, true} {
		label := fmt.Sprintf("crossAsset=%t", crossAsset)
		comment := Commentf("%s", label)
		ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
		pools := []common.Asset{common.BTCAsset}
		queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
		c.Assert(err, IsNil)
		if crossAsset {
			// One executed slice of a two-slice BTC->ETH stream leaves an ETH
			// output that only an inline reverse swap can unwind.
			targetPool := fixture.sourcePool
			targetPool.Asset = common.ETHAsset
			c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)
			pools = append(pools, common.ETHAsset)
			queued.TargetAsset = common.ETHAsset
			queued.Destination = GetRandomETHAddress()
			queued.Tx.Memo = fmt.Sprintf("=:ETH.ETH:%s:0/1/2", queued.Destination)
			fixture.voter.Tx.Tx = queued.Tx
			mgr.Keeper().SetObservedTxInVoter(ctx, fixture.voter)
			c.Assert(mgr.Keeper().SetMempoolSwap(ctx, newMempoolSwapLifecycle(ctx, mgr.Keeper(), fixture.voter, queued, fixture.reservedTOR)), IsNil)
		} else {
			// A fully staged native RUNE output unwinds directly.
			queued.State.Quantity = 1
			queued.State.Interval = 1
			queued.State.Deposit = queued.Tx.Coins[0].Amount
		}
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, queued), IsNil)
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

		record, found, err := mgr.Keeper().GetMempoolSwap(ctx, fixture.voter.TxID, 0)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, true)
		c.Assert(record.PendingEffects.Pools, HasLen, len(pools), comment)
		for _, effect := range record.PendingEffects.Pools {
			c.Assert(effect.ReserveRune.IsZero(), Equals, false, Commentf("fee restoration must be exercised for %s", effect.Asset))
		}

		before := make(map[string]Pool, len(pools))
		for _, asset := range pools {
			before[asset.String()], err = mgr.Keeper().GetPool(ctx, asset)
			c.Assert(err, IsNil)
		}
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
		c.Assert(err, IsNil)
		recorder := &recordingMempoolEventManager{EventManager: mgr.EventMgr()}
		rollbackMgr := mempoolSwapEventManagerOverride{Manager: mgr, eventMgr: recorder}
		c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), rollbackMgr, voter), IsNil, comment)
		c.Assert(mgr.Keeper().HasMempoolSwap(ctx, fixture.voter.TxID, 0), Equals, false, comment)

		flows := make(map[string]*poolFlow, len(pools))
		for _, asset := range pools {
			flows[asset.String()] = &poolFlow{cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()}
		}
		flowFor := func(asset common.Asset) *poolFlow {
			flow, ok := flows[asset.String()]
			c.Assert(ok, Equals, true, Commentf("rollback reported a change to unexpected pool %s", asset))
			return flow
		}
		for _, item := range recorder.items {
			switch evt := item.(type) {
			case *EventSwap:
				c.Errorf("rollback emitted a canonical swap event for pool %s (%s)", evt.Pool, label)
			case *types.EventPoolBalanceChanged:
				flow := flowFor(evt.PoolChange.Asset)
				if evt.PoolChange.RuneAdd {
					flow.runeIn = flow.runeIn.Add(evt.PoolChange.RuneAmt)
				} else {
					flow.runeOut = flow.runeOut.Add(evt.PoolChange.RuneAmt)
				}
				if evt.PoolChange.AssetAdd {
					flow.assetIn = flow.assetIn.Add(evt.PoolChange.AssetAmt)
				} else {
					flow.assetOut = flow.assetOut.Add(evt.PoolChange.AssetAmt)
				}
			case *EventDonate:
				flow := flowFor(evt.Pool)
				for _, coin := range evt.InTx.Coins {
					if coin.IsRune() {
						flow.runeIn = flow.runeIn.Add(coin.Amount)
					} else {
						flow.assetIn = flow.assetIn.Add(coin.Amount)
					}
				}
			}
		}

		for _, asset := range pools {
			after, err := mgr.Keeper().GetPool(ctx, asset)
			c.Assert(err, IsNil)
			flow := flows[asset.String()]
			prev := before[asset.String()]
			c.Check(prev.BalanceRune.Add(flow.runeIn).String(), Equals, after.BalanceRune.Add(flow.runeOut).String(),
				Commentf("%s rune: before=%s after=%s reported +%s -%s (%s)", asset, prev.BalanceRune, after.BalanceRune, flow.runeIn, flow.runeOut, label))
			c.Check(prev.BalanceAsset.Add(flow.assetIn).String(), Equals, after.BalanceAsset.Add(flow.assetOut).String(),
				Commentf("%s asset: before=%s after=%s reported +%s -%s (%s)", asset, prev.BalanceAsset, after.BalanceAsset, flow.assetIn, flow.assetOut, label))
		}
	}
}
