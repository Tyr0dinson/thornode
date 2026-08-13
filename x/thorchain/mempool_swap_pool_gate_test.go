package thorchain

import (
	"errors"
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MempoolSwapPoolGateSuite struct{}

var _ = Suite(&MempoolSwapPoolGateSuite{})

func (s *MempoolSwapPoolGateSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func setActiveMempoolSwapPoolOverlay(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	runeIncrease cosmos.Uint,
) (types.MempoolSwap, Pool) {
	_, record, pool := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	effect := newMempoolSwapPoolEffect(pool.Asset)
	effect.BalanceRuneIncrease = runeIncrease
	record.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	// Pool balance effects are already applied during speculative execution;
	// the lifecycle overlay identifies the reversible portion of the live pool.
	pool.BalanceRune = pool.BalanceRune.Add(runeIncrease)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return record, pool
}

func setActiveMempoolSwapSynthSupplyOverlay(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	supplyIncrease cosmos.Uint,
) (types.MempoolSwap, common.Asset) {
	_, record, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	synthAsset := common.BTCAsset.GetSyntheticAsset()
	record.PendingEffects.Supplies = []types.MempoolSwapSupplyEffect{{
		Asset:          synthAsset,
		SupplyIncrease: supplyIncrease,
		SupplyDecrease: cosmos.ZeroUint(),
	}}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	return record, synthAsset
}

func finalizeMempoolSwapPoolGateRecord(c *C, ctx cosmos.Context, mgr *Mgrs, record *types.MempoolSwap) {
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, *record), IsNil)
}

func setExecutedMempoolSwapPoolOwner(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
) (types.MempoolSwap, ObservedTxVoter, Pool) {
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: false,
	}, nil)
	msg, record, basePool := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	observed := NewObservedTx(msg.Tx, ctx.BlockHeight(), record.VaultPubKey, 0)
	voter := NewObservedTxVoter(msg.Tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, record.ReservedTor)

	emit, _ := executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_SETTLED
	record.LatestSwap.State.Count = record.LatestSwap.State.Quantity
	record.LatestSwap.State.In = msg.Tx.Coins[0].Amount
	record.LatestSwap.State.Out = emit
	record.LatestSwap.State.Withdrawn = emit
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	return record, voter, basePool
}

func newPoolGateAssetAddMsg(assetAmount cosmos.Uint, runeAddress, assetAddress common.Address) *MsgAddLiquidity {
	tx := common.NewTx(
		GetRandomTxHash(),
		assetAddress,
		GetRandomBTCAddress(),
		common.NewCoins(common.NewCoin(common.BTCAsset, assetAmount)),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"+:BTC.BTC:"+runeAddress.String(),
	)
	tx.Chain = common.BTCChain
	return NewMsgAddLiquidity(
		tx,
		common.BTCAsset,
		cosmos.ZeroUint(),
		assetAmount,
		runeAddress,
		assetAddress,
		common.NoAddress,
		cosmos.ZeroUint(),
		GetRandomBech32Addr(),
	)
}

func (s *MempoolSwapPoolGateSuite) TestAddLiquidityPreemptsUnfinalizedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: false,
	}, nil)
	record, voter, basePool := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	runeAddress := GetRandomRUNEAddress()
	assetAddress := GetRandomBTCAddress()
	assetAmount := cosmos.NewUint(common.One)
	msg := newPoolGateAssetAddMsg(assetAmount, runeAddress, assetAddress)

	_, err := NewAddLiquidityHandler(mgr).Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.MempoolSwapTOR.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceRune.Equal(basePool.BalanceRune), Equals, true)
	c.Check(pool.BalanceAsset.Equal(basePool.BalanceAsset), Equals, true)
	c.Check(pool.PendingInboundAsset.Equal(assetAmount), Equals, true)
	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	c.Check(lp.PendingAsset.Equal(assetAmount), Equals, true)
}

func (s *MempoolSwapPoolGateSuite) TestFailedAddDoesNotCommitPoolOwnerPreemption(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, voter, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	runeAddress := GetRandomRUNEAddress()
	existing := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddress,
		AssetAddress:      GetRandomBTCAddress(),
		Units:             cosmos.NewUint(10 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, existing)
	msg := newPoolGateAssetAddMsg(cosmos.OneUint(), runeAddress, GetRandomBTCAddress())
	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	usageBefore := mgr.Keeper().GetMempoolSwapUsageTOR(ctx)

	_, err = NewAddLiquidityHandler(mgr).Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, true)
	owner, found, ownerErr := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(ownerErr, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, record.Cursor())
	afterVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
	c.Assert(err, IsNil)
	c.Check(afterVoter.MempoolSwapTOR.Equal(voter.MempoolSwapTOR), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).Equal(usageBefore), Equals, true)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
}

func (s *MempoolSwapPoolGateSuite) TestErrataPreemptsUnrelatedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, _, basePool := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	livePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(livePool.BalanceAsset.Equal(basePool.BalanceAsset), Equals, false)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	errataAmount := basePool.BalanceAsset
	errataTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.NewCoins(common.NewCoin(common.BTCAsset, errataAmount)),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"=:THOR.RUNE:"+GetRandomTHORAddress().String(),
	)
	errataTx.Chain = common.BTCChain
	observed := NewObservedTx(errataTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight())
	observed.Status = common.Status_done
	observed.Signers = []string{node.NodeAddress.String()}
	errataObservedVoter := NewObservedTxVoter(errataTx.ID, ObservedTxs{observed})
	errataObservedVoter.Tx = observed
	errataObservedVoter.Height = ctx.BlockHeight()
	errataObservedVoter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, errataObservedVoter)

	errataVoter := NewErrataTxVoter(errataTx.ID, common.BTCChain)
	errata := common.ErrataTx{Id: errataTx.ID, Chain: common.BTCChain}
	err = processErrataTxAttestation(ctx, mgr, &errataVoter, node.NodeAddress, NodeAccounts{node}, &errata, true)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.IsZero(), Equals, true)
	_, _, err = newFinalityPoolView(mgr.Keeper()).getPool(ctx, common.BTCAsset)
	c.Check(err, IsNil)
}

func (s *MempoolSwapPoolGateSuite) TestWithdrawPreemptsUnfinalizedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	seedActiveAdvSwapVault(c, ctx, mgr)
	record, _, _ := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	pendingRune := cosmos.NewUint(common.One)
	runeAddress := GetRandomRUNEAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddress,
		AssetAddress:      GetRandomBTCAddress(),
		Units:             cosmos.ZeroUint(),
		PendingRune:       pendingRune,
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	pool.PendingInboundRune = pendingRune
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	vault := GetRandomVault()
	vault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, pendingRune.Uint64())
	tx := common.NewTx(
		GetRandomTxHash(),
		runeAddress,
		runeAddress,
		common.Coins{},
		common.Gas{},
		"-:BTC.BTC",
	)
	tx.Chain = common.THORChain
	msg := NewMsgWithdrawLiquidity(
		tx,
		runeAddress,
		cosmos.NewUint(MaxWithdrawBasisPoints),
		common.BTCAsset,
		common.EmptyAsset,
		GetRandomBech32Addr(),
	)

	_, err = NewWithdrawLiquidityHandler(mgr).Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	afterPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(afterPool.PendingInboundRune.IsZero(), Equals, true)
	afterLP, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	c.Check(afterLP.Units.IsZero(), Equals, true)
	c.Check(afterLP.PendingRune.IsZero(), Equals, true)
	c.Check(afterLP.PendingAsset.IsZero(), Equals, true)
}

func (s *MempoolSwapPoolGateSuite) TestLPHandlersRejectPoolBalanceOverlayWithoutMutation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_, livePool := setActiveMempoolSwapPoolOverlay(c, ctx, mgr, cosmos.OneUint())

	balanceAffected, supplyAffected, err := mempoolSwapPoolFinalityAffected(ctx, mgr.Keeper(), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(balanceAffected, Equals, true)
	c.Check(supplyAffected, Equals, false)
	err = ensureMempoolSwapPoolFinalized(ctx, mgr.Keeper(), common.BTCAsset)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)
	c.Check(err.Error(), Equals, fmt.Sprintf(
		"pool has active unfinalized mempool swap effects for %s: pool balance overlay is active",
		common.BTCAsset,
	))

	runeAddress := GetRandomRUNEAddress()
	assetAddress := GetRandomBTCAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddress,
		AssetAddress:      assetAddress,
		Units:             cosmos.NewUint(10 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	handler := NewAddLiquidityHandler(mgr)
	err = handler.addLiquidity(
		ctx,
		common.BTCAsset,
		cosmos.OneUint(),
		cosmos.OneUint(),
		runeAddress,
		assetAddress,
		GetRandomTxHash(),
		false,
		constants.GetConstantValues(GetCurrentVersion()),
	)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)

	afterAddPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(afterAddPool, DeepEquals, livePool)
	afterAddLP, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	c.Check(afterAddLP, DeepEquals, lp)

	msg := NewMsgWithdrawLiquidity(
		GetRandomTx(),
		runeAddress,
		cosmos.NewUint(MaxWithdrawBasisPoints/2),
		common.BTCAsset,
		common.EmptyAsset,
		GetRandomBech32Addr(),
	)
	_, _, _, _, err = withdraw(ctx, *msg, mgr)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)

	afterWithdrawPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(afterWithdrawPool, DeepEquals, livePool)
	afterWithdrawLP, err := mgr.Keeper().GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	c.Check(afterWithdrawLP, DeepEquals, lp)
}

func (s *MempoolSwapPoolGateSuite) TestSaverHandlersRejectSynthSupplyOverlayWithoutMutation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_, synthAsset := setActiveMempoolSwapSynthSupplyOverlay(c, ctx, mgr, cosmos.OneUint())

	balanceAffected, supplyAffected, err := mempoolSwapPoolFinalityAffected(ctx, mgr.Keeper(), synthAsset)
	c.Assert(err, IsNil)
	c.Check(balanceAffected, Equals, false)
	c.Check(supplyAffected, Equals, true)
	err = ensureMempoolSwapPoolFinalized(ctx, mgr.Keeper(), synthAsset)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)
	c.Check(err.Error(), Equals, fmt.Sprintf(
		"pool has active unfinalized mempool swap effects for %s: synth supply overlay is active",
		common.BTCAsset,
	))

	synthPool := NewPool()
	synthPool.Asset = synthAsset
	synthPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	synthPool.LPUnits = cosmos.NewUint(100 * common.One)
	synthPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, synthPool), IsNil)

	assetAddress := GetRandomBTCAddress()
	lp := LiquidityProvider{
		Asset:             synthAsset,
		AssetAddress:      assetAddress,
		Units:             cosmos.NewUint(10 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	handler := NewAddLiquidityHandler(mgr)
	err = handler.addLiquidity(
		ctx,
		synthAsset,
		cosmos.ZeroUint(),
		cosmos.OneUint(),
		common.NoAddress,
		assetAddress,
		GetRandomTxHash(),
		false,
		constants.GetConstantValues(GetCurrentVersion()),
	)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)

	msg := NewMsgWithdrawLiquidity(
		GetRandomTx(),
		assetAddress,
		cosmos.NewUint(MaxWithdrawBasisPoints/2),
		synthAsset,
		common.EmptyAsset,
		GetRandomBech32Addr(),
	)
	_, _, _, _, err = withdraw(ctx, *msg, mgr)
	c.Assert(errors.Is(err, errMempoolSwapPoolFinalityPending), Equals, true)

	afterPool, err := mgr.Keeper().GetPool(ctx, synthAsset)
	c.Assert(err, IsNil)
	c.Check(afterPool, DeepEquals, synthPool)
	afterLP, err := mgr.Keeper().GetLiquidityProvider(ctx, synthAsset, assetAddress)
	c.Assert(err, IsNil)
	c.Check(afterLP, DeepEquals, lp)
}

func (s *MempoolSwapPoolGateSuite) TestPoolCycleDefersAllStatusAndRankingWrites(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, _ := setActiveMempoolSwapPoolOverlay(c, ctx, mgr, cosmos.OneUint())
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(17)), IsNil)

	asset, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	candidate := NewPool()
	candidate.Asset = asset
	candidate.Status = PoolAvailable
	candidate.BalanceAsset = cosmos.OneUint()
	c.Assert(mgr.Keeper().SetPool(ctx, candidate), IsNil)

	pm := newPoolMgr()
	c.Assert(pm.cyclePools(ctx, 100, 1, 0, mgr), IsNil)
	afterDeferred, err := mgr.Keeper().GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(afterDeferred.Status, Equals, PoolAvailable)
	rollingFee, err := mgr.Keeper().GetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rollingFee, Equals, uint64(17))
	previousFee, err := mgr.Keeper().GetPrevCyclePoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(previousFee, Equals, uint64(0))

	finalizeMempoolSwapPoolGateRecord(c, ctx, mgr, &record)
	c.Assert(pm.cyclePools(ctx, 100, 1, 0, mgr), IsNil)
	afterFinality, err := mgr.Keeper().GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(afterFinality.Status, Equals, PoolStaged)
	rollingFee, err = mgr.Keeper().GetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rollingFee, Equals, uint64(0))
	previousFee, err = mgr.Keeper().GetPrevCyclePoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(previousFee, Equals, uint64(17))
}

func (s *MempoolSwapPoolGateSuite) TestDeferredPoolCyclePersistsAndRetriesNextBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr.Keeper().SetMimir(ctx, constants.PoolCycle.String(), 10)
	mgr.Keeper().SetMimir(ctx, constants.MaxAvailablePools.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.MinRunePoolDepth.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.StagedPoolCost.String(), 0)
	record, _ := setActiveMempoolSwapPoolOverlay(c, ctx, mgr, cosmos.OneUint())

	asset, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	candidate := NewPool()
	candidate.Asset = asset
	candidate.Status = PoolAvailable
	candidate.BalanceAsset = cosmos.OneUint()
	c.Assert(mgr.Keeper().SetPool(ctx, candidate), IsNil)

	pm := newPoolMgr()
	c.Assert(pm.EndBlock(ctx, mgr), IsNil)
	afterBoundary, err := mgr.Keeper().GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(afterBoundary.Status, Equals, PoolAvailable)
	c.Check(getPoolCycleRetryHeight(ctx, mgr), Equals, int64(20))

	finalizeMempoolSwapPoolGateRecord(c, ctx, mgr, &record)
	retryCtx := ctx.WithBlockHeight(21)
	c.Assert(pm.EndBlock(retryCtx, mgr), IsNil)
	afterRetry, err := mgr.Keeper().GetPool(retryCtx, asset)
	c.Assert(err, IsNil)
	c.Check(afterRetry.Status, Equals, PoolStaged)
	c.Check(getPoolCycleRetryHeight(retryCtx, mgr), Equals, int64(0))
}

func (s *MempoolSwapPoolGateSuite) TestPendingLiquidityCleanupSkipsAffectedPool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(30)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: false,
	}, nil)
	mgr.Keeper().SetMimir(ctx, constants.PendingLiquidityAgeLimit.String(), 10)
	record, pool := setActiveMempoolSwapPoolOverlay(c, ctx, mgr, cosmos.OneUint())

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	pool.PendingInboundRune = cosmos.NewUint(501)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	lp := LiquidityProvider{
		Asset:             pool.Asset,
		RuneAddress:       GetRandomRUNEAddress(),
		AssetAddress:      GetRandomBTCAddress(),
		PendingRune:       cosmos.NewUint(501),
		PendingAsset:      cosmos.ZeroUint(),
		Units:             cosmos.NewUint(300),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	pm := newPoolMgr()
	pm.cleanupPendingLiquidity(ctx, mgr)
	afterDeferredPool, err := mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Check(afterDeferredPool, DeepEquals, pool)
	afterDeferredLP, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp.GetAddress())
	c.Assert(err, IsNil)
	c.Check(afterDeferredLP, DeepEquals, lp)

	finalizeMempoolSwapPoolGateRecord(c, ctx, mgr, &record)
	pm.cleanupPendingLiquidity(ctx, mgr)
	afterFinalityPool, err := mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Check(afterFinalityPool.PendingInboundRune.IsZero(), Equals, true)
	c.Check(afterFinalityPool.BalanceRune.Equal(pool.BalanceRune.Add(lp.PendingRune)), Equals, true)
	afterFinalityLP, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp.GetAddress())
	c.Assert(err, IsNil)
	c.Check(afterFinalityLP.PendingRune.IsZero(), Equals, true)
}

func (s *MempoolSwapPoolGateSuite) TestTVLCapAndLiquidityTotalUseFinalizedPoolView(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, nil)
	record, _ := setActiveMempoolSwapPoolOverlay(c, ctx, mgr, cosmos.NewUint(900*common.One))
	saverPool := NewPool()
	saverPool.Asset = common.BTCAsset.GetSyntheticAsset()
	saverPool.BalanceAsset = cosmos.NewUint(25 * common.One)
	saverPool.LPUnits = saverPool.BalanceAsset
	saverPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, saverPool), IsNil)

	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(500 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// The finalized BTC pool is 100 RUNE / 100 BTC, so the vault is worth
	// 100 RUNE and remains below the 500 RUNE security bond. The live pool is
	// 1000 RUNE / 100 BTC only because an active lifecycle owns +900 RUNE.
	c.Check(atTVLCap(ctx, nil, mgr), Equals, false)
	liquidity, err := NewAddLiquidityHandler(mgr).getTotalLiquidityRUNE(ctx)
	c.Assert(err, IsNil)
	c.Check(liquidity.Equal(cosmos.NewUint(100*common.One)), Equals, true)

	finalizeMempoolSwapPoolGateRecord(c, ctx, mgr, &record)
	c.Check(atTVLCap(ctx, nil, mgr), Equals, true)
	liquidity, err = NewAddLiquidityHandler(mgr).getTotalLiquidityRUNE(ctx)
	c.Assert(err, IsNil)
	c.Check(liquidity.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *MempoolSwapPoolGateSuite) TestSynthLiquidityCapUsesFinalizedDisjointPoolView(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: false,
	}, nil)

	_, record, btcPool := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	decrease := cosmos.NewUint(90 * common.One)
	effect := newMempoolSwapPoolEffect(btcPool.Asset)
	effect.BalanceRuneDecrease = decrease
	record.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	btcPool.BalanceRune = btcPool.BalanceRune.Sub(decrease)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.LPUnits = ethPool.BalanceRune
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.MaximumLiquidityRune.String(), 150*common.One)
	mgr.Keeper().SetMimir(ctx, constants.MaxSynthPerPoolDepth.String(), MaxWithdrawBasisPoints)

	handler := NewSwapHandler(mgr)
	liquidity, err := handler.getTotalLiquidityRUNE(ctx)
	c.Assert(err, IsNil)
	c.Check(liquidity.Equal(cosmos.NewUint(200*common.One)), Equals, true)

	from := GetRandomTHORAddress()
	tx := common.NewTx(
		GetRandomTxHash(),
		from,
		from,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))),
		nil,
		"",
	)
	msg := NewMsgSwap(
		tx,
		common.ETHAsset.GetSyntheticAsset(),
		GetRandomTHORAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		0, 0,
		types.SwapVersion_v1,
		GetRandomBech32Addr(),
	)

	// The swap touches only ETH, so BTC's owner gate does not defer it. The
	// finalized BTC depth must nevertheless count toward this global cap.
	err = handler.validate(ctx, *msg)
	c.Check(errors.Is(err, errAddLiquidityRUNEOverLimit), Equals, true)

	// Once the BTC lifecycle finalizes, its reduced live depth is authoritative
	// and the same synth swap fits below the cap.
	finalizeMempoolSwapPoolGateRecord(c, ctx, mgr, &record)
	liquidity, err = handler.getTotalLiquidityRUNE(ctx)
	c.Assert(err, IsNil)
	c.Check(liquidity.Equal(cosmos.NewUint(110*common.One)), Equals, true)
	c.Check(handler.validate(ctx, *msg), IsNil)
}
