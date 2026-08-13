package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MempoolSwapEffectsSuite struct{}

var _ = Suite(&MempoolSwapEffectsSuite{})

const mempoolSwapEffectsAffiliate = "effects-affiliate"

type failOneCloutReadKeeper struct {
	keeper.Keeper
	failAddress common.Address
}

func (k failOneCloutReadKeeper) GetSwapperClout(ctx cosmos.Context, address common.Address) (SwapperClout, error) {
	if address.Equals(k.failAddress) {
		return NewSwapperClout(address), errors.New("forced clout read failure")
	}
	return k.Keeper.GetSwapperClout(ctx, address)
}

func (s *MempoolSwapEffectsSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func setupMempoolSwapEffectsSwap(c *C, ctx cosmos.Context, mgr *Mgrs, index uint32) (MsgSwap, types.MempoolSwap, Pool) {
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	destination := GetRandomTHORAddress()
	affiliateAlias := GetRandomTHORAddress()
	affiliate := NewTHORName(mempoolSwapEffectsAffiliate, 100_000_000, []THORNameAlias{{
		Chain:   common.THORChain,
		Address: affiliateAlias,
	}})
	affiliate.Owner = GetRandomBech32Addr()
	mgr.Keeper().SetTHORName(ctx, affiliate)
	mgr.Keeper().SetMimir(ctx, "L1DynamicFeeEnabled", 1)
	mgr.Keeper().SetMimir(ctx, "L1DynamicFeeFloorBPS", 1)
	mgr.Keeper().SetMimir(ctx, "DYNAMICFEE-WHITELIST-EFFECTS-AFFILIATE", 1)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5*common.One)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"=:r:"+destination.String()+"::"+mempoolSwapEffectsAffiliate+":100",
	)
	tx.Chain = common.BTCChain
	msg := types.NewMsgSwap(
		tx,
		common.RuneAsset(),
		destination,
		cosmos.ZeroUint(),
		affiliateAlias,
		cosmos.NewUint(100),
		"", "", nil,
		types.SwapType_market,
		1, 0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	msg.Index = index
	record := types.MempoolSwap{
		TxId:               tx.ID,
		Index:              index,
		VaultPubKey:        GetRandomPubKey(),
		ReservedTor:        cosmos.NewUint(common.One),
		AdmittedHeight:     ctx.BlockHeight(),
		UpdatedHeight:      ctx.BlockHeight(),
		NextRecoveryHeight: ctx.BlockHeight() + 100,
		Status:             types.MempoolSwapStatus_EXECUTING,
		LatestSwap:         *msg,
		PendingEffects:     types.MempoolSwapPendingEffects{},
	}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	return *msg, record, pool
}

func executeMempoolSwapEffectsSwap(c *C, ctx cosmos.Context, mgr *Mgrs, msg MsgSwap, mode mempoolSwapExecutionMode) (cosmos.Uint, *EventSwap) {
	swapCtx := withMempoolSwapExecutionMode(ctx, mode)
	swapCtx = withMempoolSwapExecutionIdentity(swapCtx, msg.Tx.ID, msg.Index)
	swapCtx = swapCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
	emit, _, events, err := newSwapper().Swap(
		swapCtx,
		mgr.Keeper(),
		msg.Tx,
		msg.TargetAsset,
		msg.Destination,
		cosmos.ZeroUint(),
		"", "", nil,
		StreamingSwap{},
		20_000,
		mgr,
		[]string{mempoolSwapEffectsAffiliate}, []cosmos.Uint{cosmos.NewUint(100)},
	)
	c.Assert(err, IsNil)
	c.Assert(events, HasLen, 1)
	c.Assert(creditDynamicFeeForLegs(
		swapCtx,
		mgr,
		events,
		[]string{mempoolSwapEffectsAffiliate},
		[]cosmos.Uint{cosmos.NewUint(100)},
	), IsNil)
	return emit, events[0]
}

func setupMempoolSupplySwap(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	source, target common.Asset,
	amount cosmos.Uint,
) MsgSwap {
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	if source.IsDerivedAsset() || target.IsDerivedAsset() {
		mgr.Keeper().SetMimir(ctx, "TorAnchor-BTC-BTC", 1)
		mgr.Keeper().SetMimir(ctx, constants.DerivedDepthBasisPts.String(), 10_000)
	}

	sourceUsesBank := source.IsRune() || source.IsSyntheticAsset() || source.IsDerivedAsset()
	targetUsesBank := target.IsRune() || target.IsSyntheticAsset() || target.IsDerivedAsset()
	from := GetRandomTHORAddress()
	txChain := common.THORChain
	gas := common.Gas(nil)
	if !sourceUsesBank {
		from = GetRandomBTCAddress()
		txChain = source.GetChain()
		gas = common.Gas{common.NewCoin(source.GetChain().GetGasAsset(), cosmos.OneUint())}
	}
	destination := GetRandomTHORAddress()
	if !targetUsesBank {
		destination = GetRandomBTCAddress()
	}
	tx := common.NewTx(
		GetRandomTxHash(),
		from,
		from,
		common.NewCoins(common.NewCoin(source, amount)),
		gas,
		"",
	)
	tx.Chain = txChain
	msg := NewMsgSwap(
		tx,
		target,
		destination,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		1, 0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	record := types.MempoolSwap{
		TxId:               tx.ID,
		Index:              msg.Index,
		VaultPubKey:        GetRandomPubKey(),
		ReservedTor:        cosmos.NewUint(common.One),
		AdmittedHeight:     ctx.BlockHeight(),
		UpdatedHeight:      ctx.BlockHeight(),
		NextRecoveryHeight: ctx.BlockHeight() + 100,
		Status:             types.MempoolSwapStatus_EXECUTING,
		LatestSwap:         *msg,
		PendingEffects:     types.MempoolSwapPendingEffects{},
	}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	return *msg
}

func executeMempoolSupplySwap(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	msg MsgSwap,
	mode mempoolSwapExecutionMode,
) (cosmos.Uint, []*EventSwap) {
	swapCtx := withMempoolSwapExecutionMode(ctx, mode)
	swapCtx = withMempoolSwapExecutionIdentity(swapCtx, msg.Tx.ID, msg.Index)
	swapCtx = swapCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
	emit, _, events, err := newSwapper().Swap(
		swapCtx,
		mgr.Keeper(),
		msg.Tx,
		msg.TargetAsset,
		msg.Destination,
		cosmos.ZeroUint(),
		"", "", nil,
		StreamingSwap{},
		20_000,
		mgr,
		nil, nil,
	)
	c.Assert(err, IsNil)
	return emit, events
}

func findMempoolSupplyEffect(effects []types.MempoolSwapSupplyEffect, asset common.Asset) (types.MempoolSwapSupplyEffect, bool) {
	for _, effect := range effects {
		if effect.Asset.Native() == asset.Native() {
			return effect, true
		}
	}
	return types.MempoolSwapSupplyEffect{}, false
}

func (s *MempoolSwapEffectsSuite) TestSpeculativeEffectsApplyExactlyOnce(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, originalRecord, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)

	asgardBefore := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native())
	reserveBefore := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native())
	_, event := executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)

	// Pool math and fee-depth deduction happened, but every ancillary effect
	// and the actual Asgard -> Reserve transfer remains gated on finality.
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native()).Equal(asgardBefore), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native()).Equal(reserveBefore), Equals, true)
	fees, err := mgr.Keeper().GetPoolLiquidityFees(ctx, uint64(ctx.BlockHeight()), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(fees.IsZero(), Equals, true)
	slip, err := mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(slip.IsZero(), Equals, true)
	_, err = mgr.Keeper().GetVolume(ctx, common.BTCAsset)
	c.Check(err, NotNil)
	clout, err := mgr.Keeper().GetSwapperClout(ctx, msg.Tx.FromAddress)
	c.Assert(err, IsNil)
	c.Check(clout.Score.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0)
	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	dynamicAccumulator, err := mgr.Keeper().GetDynamicFeeAccumulator(ctx, mempoolSwapEffectsAffiliate, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(dynamicAccumulator.VolumeTor.IsZero(), Equals, true)
	c.Check(dynamicAccumulator.FeesTor.IsZero(), Equals, true)

	current, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Assert(current.PendingEffects.Pools, HasLen, 1)
	c.Check(current.PendingEffects.Pools[0].LiquidityFeeRune.Equal(event.LiquidityFeeInRune), Equals, true)
	c.Check(current.PendingEffects.Pools[0].ReserveRune.Equal(event.LiquidityFeeInRune), Equals, true)
	c.Check(current.PendingEffects.Pools[0].SwapSlip.Equal(event.PoolSlip), Equals, true)
	c.Assert(current.PendingEffects.Clout, HasLen, 2)
	c.Assert(current.PendingEffects.Affiliates, HasLen, 1)
	c.Assert(current.PendingEffects.DynamicFees, HasLen, 1)
	pendingAffiliate := current.PendingEffects.Affiliates[0]
	pendingDynamic := current.PendingEffects.DynamicFees[0]

	// Finality applies what execution resolved even if governance changes in
	// between; re-checking the dynamic whitelist here would make attribution
	// nondeterministic with respect to the speculative execution.
	mgr.Keeper().SetMimir(ctx, "L1DynamicFeeEnabled", 0)
	mgr.Keeper().SetMimir(ctx, "DYNAMICFEE-WHITELIST-EFFECTS-AFFILIATE", 0)

	c.Assert(applyPendingMempoolSwapEffectsCached(ctx, mgr, originalRecord), IsNil)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native()).Equal(asgardBefore.Sub(event.LiquidityFeeInRune)), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native()).Equal(reserveBefore.Add(event.LiquidityFeeInRune)), Equals, true)
	fees, err = mgr.Keeper().GetPoolLiquidityFees(ctx, uint64(ctx.BlockHeight()), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(fees.Equal(event.LiquidityFeeInRune), Equals, true)
	slip, err = mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(slip.Equal(cosmos.NewIntFromBigInt(event.PoolSlip.BigInt())), Equals, true)
	volume, err := mgr.Keeper().GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(volume.ChangeAsset.Equal(msg.Tx.Coins[0].Amount), Equals, true)
	c.Check(volume.ChangeRune.Equal(event.EmitAsset.Amount), Equals, true)
	clout, err = mgr.Keeper().GetSwapperClout(ctx, msg.Tx.FromAddress)
	c.Assert(err, IsNil)
	c.Check(clout.Score.Equal(event.LiquidityFeeInRune.QuoUint64(2)), Equals, true)
	destinationClout, err := mgr.Keeper().GetSwapperClout(ctx, msg.Destination)
	c.Assert(err, IsNil)
	c.Check(destinationClout.Score.Equal(common.SafeSub(event.LiquidityFeeInRune, clout.Score)), Equals, true)
	affiliateEffects := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(affiliateEffects, HasLen, 1)
	c.Check(affiliateEffects[0].Fee.Equal(pendingAffiliate.FeeRune), Equals, true)
	dynamicAccumulator, err = mgr.Keeper().GetDynamicFeeAccumulator(ctx, pendingDynamic.Thorname, pendingDynamic.PairAssetA.String(), pendingDynamic.PairAssetB.String())
	c.Assert(err, IsNil)
	c.Check(dynamicAccumulator.VolumeTor.Equal(pendingDynamic.VolumeTor), Equals, true)
	c.Check(dynamicAccumulator.FeesTor.Equal(pendingDynamic.FeesTor), Equals, true)

	// Replay a stale pre-application record. The helper reloads the canonical
	// empty journal, so every value remains unchanged.
	c.Assert(applyPendingMempoolSwapEffectsCached(ctx, mgr, originalRecord), IsNil)
	feesAfterReplay, err := mgr.Keeper().GetPoolLiquidityFees(ctx, uint64(ctx.BlockHeight()), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(feesAfterReplay.Equal(fees), Equals, true)
	slipAfterReplay, err := mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(slipAfterReplay.Equal(slip), Equals, true)
	volumeAfterReplay, err := mgr.Keeper().GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(volumeAfterReplay, DeepEquals, volume)
	senderCloutAfterReplay, err := mgr.Keeper().GetSwapperClout(ctx, msg.Tx.FromAddress)
	c.Assert(err, IsNil)
	c.Check(senderCloutAfterReplay, DeepEquals, clout)
	destinationCloutAfterReplay, err := mgr.Keeper().GetSwapperClout(ctx, msg.Destination)
	c.Assert(err, IsNil)
	c.Check(destinationCloutAfterReplay, DeepEquals, destinationClout)
	affiliateEffectsAfterReplay := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(affiliateEffectsAfterReplay, HasLen, 1)
	c.Check(affiliateEffectsAfterReplay[0], DeepEquals, affiliateEffects[0])
	dynamicAfterReplay, err := mgr.Keeper().GetDynamicFeeAccumulator(ctx, pendingDynamic.Thorname, pendingDynamic.PairAssetA.String(), pendingDynamic.PairAssetB.String())
	c.Assert(err, IsNil)
	c.Check(dynamicAfterReplay, DeepEquals, dynamicAccumulator)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native()).Equal(asgardBefore.Sub(event.LiquidityFeeInRune)), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native()).Equal(reserveBefore.Add(event.LiquidityFeeInRune)), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestFinalityPoolViewChangesExactlyOnce(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)

	livePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(livePool.BalanceRune.Equal(poolBefore.BalanceRune), Equals, false)
	poolView := newFinalityPoolView(mgr.Keeper())
	adjusted, affected, err := poolView.getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(adjusted.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true)
	c.Check(adjusted.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	finalView := newFinalityPoolView(mgr.Keeper())
	finalPool, affected, err := finalView.getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, false)
	c.Check(finalPool.BalanceRune.Equal(livePool.BalanceRune), Equals, true)
	c.Check(finalPool.BalanceAsset.Equal(livePool.BalanceAsset), Equals, true)

	// Replaying the same finality status neither changes live balances nor the
	// aggregate overlay.
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	replayedPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(replayedPool, DeepEquals, livePool)
	overlay, err := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(overlay), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSpeculativeSynthSupplyEffectsAreExact(c *C) {
	// A realistic L1 -> synth double swap journals the exact successful mint.
	ctx, mgr := setupManagerForTest(c)
	synth := common.BTCAsset.GetSyntheticAsset()
	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, synth, cosmos.NewUint(common.One))
	supplyBefore := mgr.Keeper().GetTotalSupply(ctx, synth)
	emit, _ := executeMempoolSupplySwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	supplyAfter := mgr.Keeper().GetTotalSupply(ctx, synth)
	c.Check(supplyAfter.Sub(supplyBefore).Equal(emit), Equals, true)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	effect, ok := findMempoolSupplyEffect(record.PendingEffects.Supplies, synth)
	c.Assert(ok, Equals, true)
	c.Check(effect.SupplyIncrease.Equal(emit), Equals, true)
	c.Check(effect.SupplyDecrease.IsZero(), Equals, true)

	poolView := newFinalityPoolView(mgr.Keeper())
	finalizedSupply, affected, err := poolView.getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(finalizedSupply.Equal(supplyBefore), Equals, true)

	// FINALIZED removes the contribution exactly once; a deep-reorg marker
	// restores it until rollback preparation clears the journal.
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	finalizedSupply, affected, err = newFinalityPoolView(mgr.Keeper()).getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, false)
	c.Check(finalizedSupply.Equal(supplyAfter), Equals, true)

	record.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	finalizedSupply, affected, err = newFinalityPoolView(mgr.Keeper()).getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(finalizedSupply.Equal(supplyBefore), Equals, true)
	rollbackSupplyTargets, err := getMempoolSwapRollbackSupplyTargets(ctx, mgr.Keeper(), record)
	c.Assert(err, IsNil)
	c.Assert(prepareMempoolSwapRollbackEffectsCached(ctx, mgr, record), IsNil)
	cleared, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Check(cleared.PendingEffects.Supplies, HasLen, 0)
	_, affected, err = newFinalityPoolView(mgr.Keeper()).getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, false)

	// The inverse source-synth leg is reachable only as rollback of an eligible
	// external-L1 lifecycle. It burns the exact forward mint, and reconciliation
	// returns supply to the pre-forward value without creating a new journal.
	rollbackTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomTHORAddress(),
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(synth, emit)),
		nil,
		"",
	)
	rollbackTx.Chain = common.THORChain
	rollbackCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionRollback)
	rollbackCtx = rollbackCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
	_, _, _, err = newSwapper().Swap(
		rollbackCtx,
		mgr.Keeper(),
		rollbackTx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(),
		"", "", nil,
		StreamingSwap{},
		20_000,
		mgr,
		nil, nil,
	)
	c.Assert(err, IsNil)
	c.Assert(reconcileMempoolSwapRollbackSupplies(ctx, mgr, rollbackSupplyTargets), IsNil)
	c.Check(mgr.Keeper().GetTotalSupply(ctx, synth).Equal(supplyBefore), Equals, true)
	cleared, _, err = mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Check(cleared.PendingEffects.Supplies, HasLen, 0)
}

func (s *MempoolSwapEffectsSuite) TestRollbackSupplyReconciliationPreservesInterveningSameDenomMint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	synth := common.BTCAsset.GetSyntheticAsset()
	baseline := common.NewCoin(synth, cosmos.NewUint(10*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, baseline), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(baseline)), IsNil)
	preForwardSupply := mgr.Keeper().GetBankSupply(ctx, synth)

	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, synth, cosmos.NewUint(common.One))
	emit, _ := executeMempoolSupplySwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Assert(emit.GT(cosmos.OneUint()), Equals, true)

	// A later transaction changes the same bank denom. Removing this lifecycle
	// must preserve that independent supply rather than restoring a stale global
	// snapshot from before the speculative swap.
	intervening := common.NewCoin(synth, cosmos.NewUint(777))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, intervening), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(intervening)), IsNil)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	targets, err := getMempoolSwapRollbackSupplyTargets(ctx, mgr.Keeper(), record)
	c.Assert(err, IsNil)
	c.Assert(prepareMempoolSwapRollbackEffectsCached(ctx, mgr, record), IsNil)

	// Model an inverse swap which burns one unit less than the forward mint. The
	// exact reconciler must remove that rounding residue without consuming the
	// intervening mint.
	inverseBurn := common.NewCoin(synth, emit.Sub(cosmos.OneUint()))
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(inverseBurn)), IsNil)
	c.Assert(mgr.Keeper().BurnFromModule(ctx, ModuleName, inverseBurn), IsNil)
	c.Assert(reconcileMempoolSwapRollbackSupplies(ctx, mgr, targets), IsNil)

	expectedSupply := preForwardSupply.Add(intervening.Amount)
	c.Check(mgr.Keeper().GetBankSupply(ctx, synth).Equal(expectedSupply), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synth.Native()).Equal(expectedSupply), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSpeculativeDerivedSupplyAndRegenerationAreExact(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, common.TOR, cosmos.NewUint(common.One))
	c.Check(msg.Tx.Coins[0].Asset.IsExternalL1Asset(), Equals, true)
	// Put RUNE at its hard cap with a funded Reserve. The eligible external-L1
	// forward leg burns RUNE, and that exact protected headroom must let rollback
	// mint it back without falling through to a Reserve borrow.
	reserveFunding := common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(reserveFunding)), IsNil)
	maxRuneSupply := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
	mgr.Keeper().SetMimir(ctx, constants.MaxRuneSupply.String(), maxRuneSupply.BigInt().Int64())
	reserveBeforeForward := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	runeBefore := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
	torBefore := mgr.Keeper().GetBankSupply(ctx, common.TOR)
	emit, events := executeMempoolSupplySwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBeforeForward), Equals, true)
	c.Assert(events, HasLen, 2)
	intermediateRune := events[0].EmitAsset.Amount
	runeAfter := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
	torAfter := mgr.Keeper().GetBankSupply(ctx, common.TOR)
	c.Check(runeBefore.Sub(runeAfter).Equal(intermediateRune), Equals, true)
	c.Check(torAfter.Sub(torBefore).Equal(emit), Equals, true)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	runeEffect, ok := findMempoolSupplyEffect(record.PendingEffects.Supplies, common.RuneAsset())
	c.Assert(ok, Equals, true)
	c.Check(runeEffect.SupplyIncrease.IsZero(), Equals, true)
	c.Check(runeEffect.SupplyDecrease.Equal(intermediateRune), Equals, true)
	torEffect, ok := findMempoolSupplyEffect(record.PendingEffects.Supplies, common.TOR)
	c.Assert(ok, Equals, true)
	c.Check(torEffect.SupplyIncrease.Equal(emit), Equals, true)
	c.Check(torEffect.SupplyDecrease.IsZero(), Equals, true)

	poolView := newFinalityPoolView(mgr.Keeper())
	finalizedRune, affected, err := poolView.getTotalSupply(ctx, common.RuneAsset())
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(finalizedRune.Equal(runeBefore), Equals, true)
	finalizedTOR, affected, err := poolView.getTotalSupply(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(finalizedTOR.Equal(torBefore), Equals, true)

	// BeginBlock regeneration must rebuild the finalized virtual base and then
	// reapply the still-active pool overlay, leaving live speculative depths
	// unchanged and therefore still reversible.
	liveBeforeRegeneration, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	mgr.NetworkMgr().SpawnDerivedAsset(ctx, common.TOR, mgr)
	liveAfterRegeneration, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(liveAfterRegeneration.BalanceRune.Equal(liveBeforeRegeneration.BalanceRune), Equals, true)
	c.Check(liveAfterRegeneration.BalanceAsset.Equal(liveBeforeRegeneration.BalanceAsset), Equals, true)

	// Rollback preparation removes supply ownership before the inverse so the
	// RUNE cap sees real headroom. Rollback execution itself must never create
	// a replacement journal contribution.
	record.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	rollbackSupplyTargets, err := getMempoolSwapRollbackSupplyTargets(ctx, mgr.Keeper(), record)
	c.Assert(err, IsNil)
	c.Assert(prepareMempoolSwapRollbackEffectsCached(ctx, mgr, record), IsNil)
	cleared, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Check(cleared.PendingEffects.Supplies, HasLen, 0)

	rollbackTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomTHORAddress(),
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(common.TOR, emit)),
		nil,
		"",
	)
	rollbackTx.Chain = common.THORChain
	rollbackCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionRollback)
	rollbackCtx = rollbackCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
	reserveBeforeRollback := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	_, _, _, err = newSwapper().Swap(
		rollbackCtx,
		mgr.Keeper(),
		rollbackTx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(),
		"", "", nil,
		StreamingSwap{},
		20_000,
		mgr,
		nil, nil,
	)
	c.Assert(err, IsNil)
	c.Assert(reconcileMempoolSwapRollbackSupplies(ctx, mgr, rollbackSupplyTargets), IsNil)
	c.Check(mgr.Keeper().GetBankSupply(ctx, common.RuneAsset()).Equal(runeBefore), Equals, true)
	c.Check(mgr.Keeper().GetBankSupply(ctx, common.TOR).Equal(torBefore), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBeforeRollback), Equals, true,
		Commentf("exact inverse RUNE reconciliation must mint into Asgard rather than borrow Reserve"))
	cleared, _, err = mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Check(cleared.PendingEffects.Supplies, HasLen, 0)
}

func (s *MempoolSwapEffectsSuite) TestRollbackRuneMintRejectsReserveBorrowAtomically(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// Create the derived pool through a valid external-L1 lifecycle fixture, but
	// do not execute its forward swap. With no speculative RUNE burn there is no
	// protected mint headroom for the rollback-only TOR -> RUNE operation below.
	setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, common.TOR, cosmos.NewUint(common.One))
	mgr.NetworkMgr().SpawnDerivedAsset(ctx, common.TOR, mgr)
	derivedBefore, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Assert(derivedBefore.IsEmpty(), Equals, false)

	torInput := common.NewCoin(common.TOR, cosmos.NewUint(common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, torInput), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(torInput)), IsNil)
	reserveFunding := common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(reserveFunding)), IsNil)
	maxRuneSupply := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
	mgr.Keeper().SetMimir(ctx, constants.MaxRuneSupply.String(), maxRuneSupply.BigInt().Int64())

	runeSupplyBefore := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
	torSupplyBefore := mgr.Keeper().GetBankSupply(ctx, common.TOR)
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	asgardRuneBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	asgardTORBefore := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.TOR.Native())

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomTHORAddress(),
		GetRandomTHORAddress(),
		common.NewCoins(torInput),
		nil,
		"",
	)
	cacheCtx, _ := ctx.CacheContext()
	rollbackCtx := withMempoolSwapExecutionMode(cacheCtx, mempoolSwapExecutionRollback)
	rollbackCtx = rollbackCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
	_, _, _, err = newSwapper().Swap(
		rollbackCtx,
		mgr.Keeper(),
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.ZeroUint(),
		"", "", nil,
		StreamingSwap{},
		20_000,
		mgr,
		nil, nil,
	)
	c.Assert(err, ErrorMatches, "non-normal RUNE mint would borrow Reserve:.*")

	// Discarding the caller-owned cache must leave both the earlier source burn
	// and the rejected Reserve fallback completely absent from consensus state.
	c.Check(mgr.Keeper().GetBankSupply(ctx, common.RuneAsset()).Equal(runeSupplyBefore), Equals, true)
	c.Check(mgr.Keeper().GetBankSupply(ctx, common.TOR).Equal(torSupplyBefore), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgardRuneBefore), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.TOR.Native()).Equal(asgardTORBefore), Equals, true)
	derivedAfter, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(derivedAfter, DeepEquals, derivedBefore)
}

func (s *MempoolSwapEffectsSuite) TestMarkerlessRollbackReconcilesSupplyWithoutAction(c *C) {
	ctx, mgr := setupManagerForTest(c)
	synth := common.BTCAsset.GetSyntheticAsset()
	inputAmount := cosmos.NewUint(common.One)
	mintedAmount := cosmos.NewUint(common.One / 2)
	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, synth, inputAmount)

	// Model terminal markerless settlement: swap execution credited the source
	// pool and minted synth, but dust cleanup left no queue item, staged outbound,
	// or voter action through which rollback could discover an inverse swap.
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	poolBeforeCredit := pool
	pool.BalanceAsset = pool.BalanceAsset.Add(inputAmount)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	coin := common.NewCoin(synth, mintedAmount)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_SETTLED
	record.LatestSwap.State.Deposit = inputAmount
	record.LatestSwap.State.In = inputAmount
	record.PendingEffects.Supplies = []types.MempoolSwapSupplyEffect{{
		Asset:          synth,
		SupplyIncrease: mintedAmount,
		SupplyDecrease: cosmos.ZeroUint(),
	}}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)

	observed := ObservedTx{
		Tx:                    msg.Tx,
		Status:                common.Status_done,
		BlockHeight:           ctx.BlockHeight(),
		FinaliseHeight:        0,
		ObservedPubKey:        record.VaultPubKey,
		Aggregator:            "",
		AggregatorTarget:      "",
		AggregatorTargetLimit: nil,
	}
	voter := NewObservedTxVoter(msg.Tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, record.ReservedTor)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, msg.Tx.ID, 0), Equals, false)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, msg.Tx.ID), Equals, false)
	c.Check(voter.Actions, HasLen, 0)

	c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter), IsNil)
	c.Check(mgr.Keeper().GetBankSupply(ctx, synth).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synth.Native()).IsZero(), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, msg.Tx.ID, msg.Index), Equals, false)
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(poolBeforeCredit.BalanceAsset), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSaverYieldUsesFinalizedSupplyAndPoolDepth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	synth := common.BTCAsset.GetSyntheticAsset()
	initialSynthSupply := cosmos.NewUint(10 * common.One)
	seed := common.NewCoin(synth, initialSynthSupply)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, seed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(seed)), IsNil)

	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, synth, cosmos.NewUint(20*common.One))
	executeMempoolSupplySwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	poolView := newFinalityPoolView(mgr.Keeper())
	finalizedPool, poolAffected, err := poolView.getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAffected, Equals, true)
	finalizedSupply, supplyAffected, err := poolView.getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(supplyAffected, Equals, true)
	c.Check(finalizedSupply.Equal(initialSynthSupply), Equals, true)

	saver := NewPool()
	saver.Asset = synth
	saver.Status = PoolAvailable
	saver.BalanceAsset = initialSynthSupply
	saver.LPUnits = initialSynthSupply
	c.Assert(mgr.Keeper().SetPool(ctx, saver), IsNil)

	const (
		basisPts = int64(10_000)
		maxYield = int64(10_000)
	)
	mgr.Keeper().SetMimir(ctx, constants.SynthYieldBasisPoints.String(), basisPts)
	mgr.Keeper().SetMimir(ctx, constants.MaxSynthsForSaversYield.String(), maxYield)
	runeYield := cosmos.NewUint(50 * common.One)

	// Reproduce the yield formula from finalized inputs to make the test
	// sensitive to either the live speculative pool or live synth supply.
	expectedPool := finalizedPool
	expectedPool.CalcUnits(finalizedSupply)
	synthPerPoolDepth := common.GetUncappedShare(expectedPool.SynthUnits, expectedPool.GetPoolUnits(), cosmos.NewUint(10_000))
	lostYield := common.GetUncappedShare(synthPerPoolDepth, cosmos.NewUint(uint64(maxYield)), cosmos.NewUint(uint64(basisPts)))
	effectiveBasisPts := common.SafeSub(cosmos.NewUint(uint64(basisPts)), lostYield)
	expectedEarnings := expectedPool.RuneValueInAsset(runeYield)
	expectedEarnings = common.GetSafeShare(saver.BalanceAsset, expectedPool.BalanceAsset.MulUint64(2), expectedEarnings)
	expectedEarnings = common.GetSafeShare(effectiveBasisPts, cosmos.NewUint(10_000), expectedEarnings)
	c.Check(expectedEarnings.IsZero(), Equals, false)

	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.paySaverYield(ctx, common.BTCAsset, runeYield), IsNil)
	updatedSaver, err := mgr.Keeper().GetPool(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(updatedSaver.BalanceAsset.Sub(saver.BalanceAsset).Equal(expectedEarnings), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSaverUtilizationSkipsAffectedFinalizedView(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	synth := common.BTCAsset.GetSyntheticAsset()
	baselineSupply := cosmos.NewUint(50 * common.One)
	seed := common.NewCoin(synth, baselineSupply)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, seed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(seed)), IsNil)

	msg := setupMempoolSupplySwap(c, ctx, mgr, common.BTCAsset, synth, cosmos.NewUint(common.One))
	executeMempoolSupplySwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	saverPool := NewPool()
	saverPool.Asset = synth
	saverPool.Status = PoolAvailable
	saverPool.BalanceAsset = baselineSupply
	saverPool.LPUnits = baselineSupply
	c.Assert(mgr.Keeper().SetPool(ctx, saverPool), IsNil)

	saver := LiquidityProvider{
		Asset:         synth,
		RuneAddress:   common.NoAddress,
		AssetAddress:  GetRandomBTCAddress(),
		Units:         baselineSupply,
		LastAddHeight: ctx.BlockHeight(),
		PendingRune:   cosmos.ZeroUint(),
		PendingAsset:  cosmos.ZeroUint(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, saver)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.SaversEjectInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.MaxSynthPerPoolDepth.String(), 1_000)

	view := newFinalityPoolView(mgr.Keeper())
	finalizedSupply, supplyAffected, err := view.getTotalSupply(ctx, synth)
	c.Assert(err, IsNil)
	finalizedL1, poolAffected, err := view.getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(supplyAffected, Equals, true)
	c.Check(poolAffected, Equals, true)
	maxSynth := common.GetUncappedShare(
		cosmos.NewUint(1_000),
		cosmos.NewUint(constants.MaxBasisPts),
		finalizedL1.BalanceAsset.MulUint64(2),
	)
	c.Check(finalizedSupply.GT(maxSynth), Equals, true,
		Commentf("the finalized position is deliberately over cap so the affected-state guard, not adjusted scoring, prevents ejection"))

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.checkSaversUtilization(ctx, mgr), IsNil)
	after, err := mgr.Keeper().GetLiquidityProvider(ctx, synth, saver.AssetAddress)
	c.Assert(err, IsNil)
	c.Check(after.Units.Equal(saver.Units), Equals, true)
	c.Check(after.LastAddHeight, Equals, saver.LastAddHeight)
	c.Check(after.LastWithdrawHeight, Equals, saver.LastWithdrawHeight)
	c.Check(after.AssetAddress.Equals(saver.AssetAddress), Equals, true)
}

func finalizeMempoolEffectsRecord(c *C, ctx cosmos.Context, mgr *Mgrs, msg MsgSwap) {
	record, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
}

func installManualMempoolPoolOverlay(
	c *C,
	ctx cosmos.Context,
	mgr *Mgrs,
	base, live Pool,
) MsgSwap {
	msg, record, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	c.Assert(base.Asset.Equals(live.Asset), Equals, true)
	c.Assert(mgr.Keeper().SetPool(ctx, base), IsNil)

	effect := newMempoolSwapPoolEffect(base.Asset)
	if live.BalanceRune.GT(base.BalanceRune) {
		effect.BalanceRuneIncrease = live.BalanceRune.Sub(base.BalanceRune)
	} else if base.BalanceRune.GT(live.BalanceRune) {
		effect.BalanceRuneDecrease = base.BalanceRune.Sub(live.BalanceRune)
	}
	if live.BalanceAsset.GT(base.BalanceAsset) {
		effect.BalanceAssetIncrease = live.BalanceAsset.Sub(base.BalanceAsset)
	} else if base.BalanceAsset.GT(live.BalanceAsset) {
		effect.BalanceAssetDecrease = base.BalanceAsset.Sub(live.BalanceAsset)
	}
	record.PendingEffects.Pools = []types.MempoolSwapPoolEffect{effect}
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	c.Assert(mgr.Keeper().SetPool(ctx, live), IsNil)
	return msg
}

func (s *MempoolSwapEffectsSuite) TestFinalityPoolViewDoesNotAliasSaverPoolToL1Overlay(c *C) {
	ctx, mgr := setupManagerForTest(c)
	base := NewPool()
	base.Asset = common.BTCAsset
	base.Status = PoolAvailable
	base.BalanceRune = cosmos.NewUint(100 * common.One)
	base.BalanceAsset = cosmos.NewUint(100 * common.One)
	live := base
	live.BalanceRune = live.BalanceRune.Add(cosmos.NewUint(10 * common.One))
	installManualMempoolPoolOverlay(c, ctx, mgr, base, live)

	saver := NewPool()
	saver.Asset = common.BTCAsset.GetSyntheticAsset()
	saver.Status = PoolAvailable
	saver.BalanceAsset = cosmos.NewUint(25 * common.One)
	view := newFinalityPoolView(mgr.Keeper())
	saverOverlay, err := view.overlay(ctx, saver.Asset)
	c.Assert(err, IsNil)
	c.Check(saverOverlay.Asset.Equals(saver.Asset), Equals, true)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(saverOverlay), Equals, true)

	adjusted, affected, err := view.adjust(ctx, saver)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, false)
	c.Check(adjusted, DeepEquals, saver)
	applied, affected, err := view.apply(ctx, saver)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, false)
	c.Check(applied, DeepEquals, saver)
}

func (s *MempoolSwapEffectsSuite) TestGasPricingUsesFinalizedPoolAndPreservesLiveOverlay(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	gasMgr := newGasMgr(constants.GetConstantValues(GetCurrentVersion()), mgr.Keeper())
	gasMgr.BeginBlock()
	mgr.Keeper().SetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.TargetOutboundFeeSurplusRune.String(), int64(100*common.One))
	mgr.Keeper().SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), int64(constants.MaxBasisPts))
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundFeeMultiplierBasisPoints.String(), int64(constants.MaxBasisPts))
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 70, 50)), IsNil)

	feeBefore, err := gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, true)
	c.Assert(err, IsNil)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	feeDuring, err := gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, true)
	c.Assert(err, IsNil)
	c.Check(feeDuring.Equal(feeBefore), Equals, true)

	liveBeforeGas, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	gasAmount := cosmos.NewUint(common.One / 10)
	expectedRune := poolBefore.AssetValueInRune(gasAmount)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 10*common.One)
	gasMgr.AddGasAsset(common.BTCAsset, common.Gas{common.NewCoin(common.BTCAsset, gasAmount)}, true)
	gasMgr.ProcessGas(ctx, mgr)

	c.Assert(gasMgr.gasEvent.Pools, HasLen, 1)
	c.Check(gasMgr.gasEvent.Pools[0].RuneAmt.Equal(expectedRune), Equals, true)
	liveAfterGas, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(liveAfterGas.BalanceRune.Equal(liveBeforeGas.BalanceRune.Add(expectedRune)), Equals, true)
	c.Check(liveAfterGas.BalanceAsset.Equal(common.SafeSub(liveBeforeGas.BalanceAsset, gasAmount)), Equals, true)
	adjustedAfterGas, affected, err := newFinalityPoolView(mgr.Keeper()).getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(adjustedAfterGas.BalanceRune.Equal(poolBefore.BalanceRune.Add(expectedRune)), Equals, true)
	c.Check(adjustedAfterGas.BalanceAsset.Equal(common.SafeSub(poolBefore.BalanceAsset, gasAmount)), Equals, true)
	_, ownerFound, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(ownerFound, Equals, true,
		Commentf("a gas debit that fits both pool views must not preempt its active owner"))

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	feeAfter, err := gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, true)
	c.Assert(err, IsNil)
	c.Check(feeAfter.Equal(feeBefore), Equals, false)
}

func (s *MempoolSwapEffectsSuite) TestProcessGasRollsBackConflictingUnfinalizedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	record, voter, base := setExecutedMempoolSwapPoolOwner(c, ctx, mgr)
	live, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(live.BalanceAsset.GT(base.BalanceAsset), Equals, true)

	gasMgr := newGasMgr(constants.GetConstantValues(GetCurrentVersion()), mgr.Keeper())
	gasMgr.BeginBlock()
	// Each observation fits the finalized depth by itself, but their aggregate
	// does not. The owner must be resolved before any gas valuation or debit.
	gasAmount := base.BalanceAsset.Add(live.BalanceAsset.Sub(base.BalanceAsset).QuoUint64(2))
	firstGas := gasAmount.QuoUint64(2)
	secondGas := gasAmount.Sub(firstGas)
	expectedRune := base.AssetValueInRune(gasAmount)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 1_000*common.One)
	gasMgr.AddGasAsset(common.BTCAsset, common.Gas{common.NewCoin(common.BTCAsset, firstGas)}, true)
	gasMgr.AddGasAsset(common.ETHAsset, common.Gas{common.NewCoin(common.BTCAsset, secondGas)}, true)
	gasMgr.ProcessGas(ctx, mgr)

	c.Assert(gasMgr.gasEvent.Pools, HasLen, 1)
	c.Check(gasMgr.gasEvent.Pools[0].AssetAmt.Equal(gasAmount), Equals, true)
	c.Check(gasMgr.gasEvent.Pools[0].RuneAmt.Equal(expectedRune), Equals, true)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.IsZero(), Equals, true)
	c.Check(poolAfter.BalanceRune.Equal(base.BalanceRune.Add(expectedRune)), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, ownerFound, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(ownerFound, Equals, false)
	overlay, err := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(overlay), Equals, true)
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.MempoolSwapTOR.IsZero(), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestProcessGasCompletesConflictingFinalizedPoolOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	base := NewPool()
	base.Asset = common.BTCAsset
	base.Status = PoolAvailable
	base.BalanceRune = cosmos.NewUint(100 * common.One)
	base.BalanceAsset = cosmos.NewUint(10 * common.One)
	base.LPUnits = base.BalanceRune
	live := base
	live.BalanceAsset = cosmos.NewUint(15 * common.One)
	msg := installManualMempoolPoolOverlay(c, ctx, mgr, base, live)
	record, found, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	observed := NewObservedTx(msg.Tx, ctx.BlockHeight(), record.VaultPubKey, ctx.BlockHeight())
	voter := NewObservedTxVoter(msg.Tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	voter.Tx.FinaliseHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, record.ReservedTor)

	gasMgr := newGasMgr(constants.GetConstantValues(GetCurrentVersion()), mgr.Keeper())
	gasMgr.BeginBlock()
	gasAmount := cosmos.NewUint(12 * common.One)
	firstGas := gasAmount.QuoUint64(2)
	secondGas := gasAmount.Sub(firstGas)
	expectedRune := live.AssetValueInRune(gasAmount)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 1_000*common.One)
	gasMgr.AddGasAsset(common.BTCAsset, common.Gas{common.NewCoin(common.BTCAsset, firstGas)}, true)
	gasMgr.AddGasAsset(common.ETHAsset, common.Gas{common.NewCoin(common.BTCAsset, secondGas)}, true)
	gasMgr.ProcessGas(ctx, mgr)

	c.Assert(gasMgr.gasEvent.Pools, HasLen, 1)
	c.Check(gasMgr.gasEvent.Pools[0].AssetAmt.Equal(gasAmount), Equals, true)
	c.Check(gasMgr.gasEvent.Pools[0].RuneAmt.Equal(expectedRune), Equals, true)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.Equal(live.BalanceAsset.Sub(gasAmount)), Equals, true,
		Commentf("finalized speculative depth must be charged instead of discarded"))
	c.Check(poolAfter.BalanceRune.Equal(live.BalanceRune.Add(expectedRune)), Equals, true)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	_, ownerFound, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(ownerFound, Equals, false)
	overlay, err := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(overlay), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSlashUsesFinalizedValueAndMutatesLivePool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	poolBefore := NewPool()
	poolBefore.Asset = common.BTCAsset
	poolBefore.Status = PoolAvailable
	poolBefore.BalanceRune = cosmos.NewUint(100 * common.One)
	poolBefore.BalanceAsset = cosmos.NewUint(100 * common.One)
	poolBefore.LPUnits = poolBefore.BalanceRune
	liveBeforeSlash := poolBefore
	liveBeforeSlash.BalanceRune = cosmos.NewUint(110 * common.One)
	liveBeforeSlash.BalanceAsset = cosmos.NewUint(60 * common.One)
	installManualMempoolPoolOverlay(c, ctx, mgr, poolBefore, liveBeforeSlash)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node.Bond.Uint64())
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, node.PubKeySet.Secp256k1, common.Chains{common.BTCChain}.Strings(), nil)
	vault.Membership = []string{node.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	stolen := cosmos.NewUint(common.One)
	expectedValue := poolBefore.AssetValueInRune(stolen)
	penaltyBps := mgr.Keeper().GetConfigInt64(ctx, constants.SlashPenalty)
	expectedSlash := common.GetUncappedShare(cosmos.NewUint(uint64(penaltyBps)), cosmos.NewUint(constants.MaxBasisPts), expectedValue)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, stolen)), mgr), IsNil)

	updatedNode, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, node.PubKeySet.Secp256k1)
	c.Assert(err, IsNil)
	c.Check(updatedNode.Bond.Equal(common.SafeSub(node.Bond, expectedSlash)), Equals, true)
	liveAfterSlash, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(liveAfterSlash.BalanceRune.Equal(liveBeforeSlash.BalanceRune.Add(expectedValue)), Equals, true)
	c.Check(liveAfterSlash.BalanceAsset.Equal(common.SafeSub(liveBeforeSlash.BalanceAsset, stolen)), Equals, true)
	adjustedAfterSlash, affected, err := newFinalityPoolView(mgr.Keeper()).getPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(affected, Equals, true)
	c.Check(adjustedAfterSlash.BalanceRune.Equal(poolBefore.BalanceRune.Add(expectedValue)), Equals, true)
	c.Check(adjustedAfterSlash.BalanceAsset.Equal(common.SafeSub(poolBefore.BalanceAsset, stolen)), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSolvencyCheckUsesFinalizedPoolValue(c *C) {
	ctx, mgr := setupManagerForTest(c)
	base := NewPool()
	base.Asset = common.ETHAsset
	base.Status = PoolAvailable
	base.BalanceRune = cosmos.NewUint(10_000 * common.One)
	base.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	live := base
	live.BalanceRune = cosmos.NewUint(1_000 * common.One)
	msg := installManualMempoolPoolOverlay(c, ctx, mgr, base, live)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5*common.One)
	mgr.Keeper().SetMimir(ctx, constants.PermittedSolvencyGapUSD.String(), 500*common.One)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), nil)
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000*common.One)))
	wallet := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(980*common.One)))
	// Finalized pricing values the 20 ETH gap at 200 RUNE, above the 100
	// RUNE permitted gap. The speculative live price would value it at 20.
	c.Check(insolvencyCheck(ctx, mgr, vault, wallet, common.ETHChain), Equals, true)
	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	c.Check(insolvencyCheck(ctx, mgr, vault, wallet, common.ETHChain), Equals, false)
}

func (s *MempoolSwapEffectsSuite) TestRagnarokPoolPassWaitsForActiveOverlay(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(validatorMgr.ragnarokPools(ctx, 1, mgr), IsNil)
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolAvailable)

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	c.Assert(validatorMgr.ragnarokPools(ctx, 1, mgr), IsNil)
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolStaged)
}

func (s *MempoolSwapEffectsSuite) TestRagnarokProcessDoesNotAdvanceWhileOverlayActive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, record, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	observed := NewObservedTx(msg.Tx, ctx.BlockHeight(), record.VaultPubKey, 0)
	voter := NewObservedTxVoter(msg.Tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, record.ReservedTor)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	mgr.Keeper().SetRagnarokBlockHeight(ctx, 1)
	mgr.Keeper().SetRagnarokNth(ctx, 0)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil)
	nth, err := mgr.Keeper().GetRagnarokNth(ctx)
	c.Assert(err, IsNil)
	c.Check(nth, Equals, int64(0))
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolAvailable)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)

	c.Assert(validatorMgr.processRagnarok(ctx, mgr), IsNil)
	nth, err = mgr.Keeper().GetRagnarokNth(ctx)
	c.Assert(err, IsNil)
	c.Check(nth, Equals, int64(1))
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolStaged)
}

func (s *MempoolSwapEffectsSuite) TestTCYClaimingSwapWaitsForActiveOverlay(c *C) {
	ctx, mgr := setupManagerForTest(c)
	base := NewPool()
	base.Asset = common.TCY
	base.Status = PoolAvailable
	base.BalanceRune = cosmos.NewUint(100 * common.One)
	base.BalanceAsset = cosmos.NewUint(100 * common.One)
	live := base
	live.BalanceRune = cosmos.NewUint(90 * common.One)
	live.BalanceAsset = cosmos.NewUint(110 * common.One)
	msg := installManualMempoolPoolOverlay(c, ctx, mgr, base, live)
	mgr.Keeper().SetMimir(ctx, constants.TCYClaimingSwapHalt.String(), 0)

	claimingRune := cosmos.NewUint(10 * common.One)
	FundModule(c, ctx, mgr.Keeper(), TCYClaimingName, claimingRune.Uint64())
	tcyLiquidity := common.NewCoin(common.TCY, cosmos.NewUint(200*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, tcyLiquidity), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(tcyLiquidity)), IsNil)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(networkMgr.claimingSwapRuneToTCY(ctx, mgr), IsNil)
	unchanged, err := mgr.Keeper().GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Check(unchanged, DeepEquals, live)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, TCYClaimingName).Equal(claimingRune), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native()).IsZero(), Equals, true)

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	c.Assert(networkMgr.claimingSwapRuneToTCY(ctx, mgr), IsNil)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, TCYClaimingName).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native()).IsZero(), Equals, false)
}

func (s *MempoolSwapEffectsSuite) TestMigrationDustSettlementWaitsForActiveOverlay(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	msg, _, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1, 1)), IsNil)

	active := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	retiring := NewVault(1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	retiring.StatusSince = 0
	dust := common.BTCChain.DustThreshold()
	retiring.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, dust))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 10*common.One)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	unchangedVault, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Check(unchangedVault.GetCoin(common.BTCAsset).Amount.Equal(dust), Equals, true)
	unchangedPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(unchangedPool, DeepEquals, poolBefore)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	settledVault, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Check(settledVault.GetCoin(common.BTCAsset).Amount.IsZero(), Equals, true)
	settledPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(settledPool.BalanceAsset.Equal(common.SafeSub(poolBefore.BalanceAsset, dust)), Equals, true)
	c.Check(settledPool.BalanceRune.GT(poolBefore.BalanceRune), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSpeculativePoolDepthCannotSkewRewardAllocation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	liveBTCBeforeReward, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = poolBefore.BalanceRune
	ethPool.BalanceAsset = poolBefore.BalanceAsset
	ethPool.LPUnits = poolBefore.LPUnits
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	poolView := newFinalityPoolView(mgr.Keeper())
	pools, totalRune, err := getAvailablePoolsRuneWithFinalityView(ctx, mgr.Keeper(), poolView)
	c.Assert(err, IsNil)
	c.Assert(pools, HasLen, 2)
	c.Check(totalRune.Equal(poolBefore.BalanceRune.MulUint64(2)), Equals, true)
	reward := cosmos.NewUint(20 * common.One)
	rewardAmts := make([]cosmos.Uint, len(pools))
	for i, pool := range pools {
		rewardAmts[i] = common.GetSafeShare(pool.BalanceRune, totalRune, reward)
		c.Check(rewardAmts[i].Equal(reward.QuoUint64(2)), Equals, true)
	}

	FundModule(c, ctx, mgr.Keeper(), ReserveName, reward.Uint64())
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.payPoolRewards(ctx, rewardAmts, pools), IsNil)
	liveBTCAfterReward, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(liveBTCAfterReward.BalanceRune.Equal(liveBTCBeforeReward.BalanceRune.Add(reward.QuoUint64(2))), Equals, true,
		Commentf("reward must be added to the live pool without erasing its speculative mutation"))
	liveETHAfterReward, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(liveETHAfterReward.BalanceRune.Equal(ethPool.BalanceRune.Add(reward.QuoUint64(2))), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestUpdateNetworkRewardsUseFinalizedPoolDepth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = poolBefore.BalanceRune
	ethPool.BalanceAsset = poolBefore.BalanceAsset
	ethPool.LPUnits = poolBefore.LPUnits
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)
	for i := 0; i < 2; i++ {
		node := GetRandomValidatorNode(NodeActive)
		node.Bond = cosmos.NewUint(500_000 * common.One)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	}
	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.BTCChain.String(), common.ETHChain.String()}, []ChainContract{})
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, poolBefore.BalanceAsset),
		common.NewCoin(common.ETHAsset, poolBefore.BalanceAsset),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetNetwork(ctx, NewNetwork()), IsNil)

	btcBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	ethBefore, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	btcAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	ethAfter, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	btcReward := btcAfter.BalanceRune.Sub(btcBefore.BalanceRune)
	ethReward := ethAfter.BalanceRune.Sub(ethBefore.BalanceRune)
	c.Check(btcReward.IsZero(), Equals, false)
	c.Check(btcReward.Equal(ethReward), Equals, true,
		Commentf("equal finalized depths must receive equal rewards despite BTC's live speculative depth"))
}

func (s *MempoolSwapEffectsSuite) TestSpeculativePoolDepthDefersPOLReserveMutation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)
	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(50*common.One))

	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.POLReserveCycle(ctx, mgr), IsNil)
	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName).Equal(cosmos.NewUint(50*common.One)), Equals, true)

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	c.Assert(networkMgr.POLReserveCycle(ctx, mgr), IsNil)
	deposit, err = mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.Equal(cosmos.NewUint(50*common.One)), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestAnchorCalculationUsesFinalizedPoolDepth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-BTC-BTC", 1)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	depth, price, _ := networkMgr.CalcAnchor(ctx, mgr, common.TOR)
	c.Check(depth.Equal(poolBefore.BalanceRune), Equals, true)
	c.Check(price.Equal(poolBefore.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti*common.One))), Equals, true)
	mgr.Keeper().SetMimir(ctx, constants.DerivedDepthBasisPts.String(), 10_000)
	networkMgr.SpawnDerivedAsset(ctx, common.TOR, mgr)
	derivedBeforeFinality, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(derivedBeforeFinality.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true)
	c.Check(derivedBeforeFinality.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true)

	finalizeMempoolEffectsRecord(c, ctx, mgr, msg)
	livePool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	depth, price, _ = networkMgr.CalcAnchor(ctx, mgr, common.TOR)
	c.Check(depth.Equal(livePool.BalanceRune), Equals, true)
	c.Check(price.Equal(livePool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti*common.One))), Equals, true)
	networkMgr.SpawnDerivedAsset(ctx, common.TOR, mgr)
	derivedAfterFinality, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(derivedAfterFinality.BalanceRune.Equal(livePool.BalanceRune), Equals, true)
	expectedDerivedAsset := livePool.BalanceRune.Mul(price).QuoUint64(constants.DollarMulti * common.One)
	c.Check(derivedAfterFinality.BalanceAsset.Equal(expectedDerivedAsset), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestRollbackPreparationRestoresFeeDepthExactlyOnce(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, originalRecord, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)

	emit, event := executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	poolAfterSwap, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfterSwap.BalanceRune.Equal(common.SafeSub(poolBefore.BalanceRune, emit.Add(event.LiquidityFeeInRune))), Equals, true)
	asgardBefore := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native())
	reserveBefore := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native())

	c.Assert(prepareMempoolSwapRollbackEffectsCached(ctx, mgr, originalRecord), IsNil)
	poolPrepared, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolPrepared.BalanceRune.Equal(common.SafeSub(poolBefore.BalanceRune, emit)), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.RuneNative.Native()).Equal(asgardBefore), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native()).Equal(reserveBefore), Equals, true)

	c.Assert(prepareMempoolSwapRollbackEffectsCached(ctx, mgr, originalRecord), IsNil)
	poolAfterReplay, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfterReplay.BalanceRune.Equal(poolPrepared.BalanceRune), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestSpeculativeRevShareCannotPayBeforeFinality(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, originalRecord, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	mgr.Keeper().SetMimir(ctx, "REVSHARE-EFFECTS-AFFILIATE", 5000)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 10*common.One)

	name, err := mgr.Keeper().GetTHORName(ctx, mempoolSwapEffectsAffiliate)
	c.Assert(err, IsNil)
	collectorBefore, err := mgr.Keeper().GetAffiliateCollector(ctx, name.Owner)
	c.Assert(err, IsNil)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionSpeculative)
	c.Check(networkMgr.processRevShare(ctx, mgr).IsZero(), Equals, true)
	collectorBeforeFinality, err := mgr.Keeper().GetAffiliateCollector(ctx, name.Owner)
	c.Assert(err, IsNil)
	c.Check(collectorBeforeFinality, DeepEquals, collectorBefore)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0)

	current, _, err := mgr.Keeper().GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	c.Assert(err, IsNil)
	c.Assert(current.PendingEffects.Affiliates, HasLen, 1)
	feeRune := current.PendingEffects.Affiliates[0].FeeRune
	c.Assert(applyPendingMempoolSwapEffectsCached(ctx, mgr, originalRecord), IsNil)
	expectedPayout := common.GetSafeShare(cosmos.NewUint(5000), cosmos.NewUint(constants.MaxBasisPts), feeRune)
	c.Check(networkMgr.processRevShare(ctx, mgr).Equal(expectedPayout), Equals, true)
	collectorAfterFinality, err := mgr.Keeper().GetAffiliateCollector(ctx, name.Owner)
	c.Assert(err, IsNil)
	c.Check(collectorAfterFinality.RuneAmount.Sub(collectorBefore.RuneAmount).Equal(expectedPayout), Equals, true)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0)

	// Replaying both the finality journal and REVSHARE processing cannot pay a
	// second time.
	c.Assert(applyPendingMempoolSwapEffectsCached(ctx, mgr, originalRecord), IsNil)
	c.Check(networkMgr.processRevShare(ctx, mgr).IsZero(), Equals, true)
	collectorAfterReplay, err := mgr.Keeper().GetAffiliateCollector(ctx, name.Owner)
	c.Assert(err, IsNil)
	c.Check(collectorAfterReplay, DeepEquals, collectorAfterFinality)
}

func (s *MempoolSwapEffectsSuite) TestRollbackExecutionCreatesNoAccountingEffects(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, poolBefore := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	// This test exercises the reverse-swap execution contract itself. It does
	// not use the lifecycle record and intentionally removes it first.
	c.Assert(mgr.Keeper().DeleteMempoolSwap(ctx, msg.Tx.ID, msg.Index), IsNil)

	reserveBefore := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native())
	eventCountBefore := len(ctx.EventManager().Events())
	emit, event := executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionRollback)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	// The AMM emission is real, but rollback mode does not perform the extra
	// system-income deduction represented by LiquidityFeeInRune.
	c.Check(poolAfter.BalanceRune.Equal(common.SafeSub(poolBefore.BalanceRune, emit)), Equals, true)
	c.Check(event.LiquidityFeeInRune.IsZero(), Equals, false)
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, common.RuneNative.Native()).Equal(reserveBefore), Equals, true)
	fees, err := mgr.Keeper().GetPoolLiquidityFees(ctx, uint64(ctx.BlockHeight()), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(fees.IsZero(), Equals, true)
	slip, err := mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(slip.IsZero(), Equals, true)
	_, err = mgr.Keeper().GetVolume(ctx, common.BTCAsset)
	c.Check(err, NotNil)
	clout, err := mgr.Keeper().GetSwapperClout(ctx, msg.Tx.FromAddress)
	c.Assert(err, IsNil)
	c.Check(clout.Score.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetAffiliateLiquidityFees(ctx), HasLen, 0)
	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	dynamicAccumulator, err := mgr.Keeper().GetDynamicFeeAccumulator(ctx, mempoolSwapEffectsAffiliate, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(dynamicAccumulator.VolumeTor.IsZero(), Equals, true)
	c.Check(dynamicAccumulator.FeesTor.IsZero(), Equals, true)
	c.Check(len(ctx.EventManager().Events()), Equals, eventCountBefore)
}

// Normal mode intentionally retains the historical error-path behavior: a
// failed sender clout read does not consume half the fee allocation, so the
// destination receives the full score. This guards mode refactors from
// changing non-mempool swap semantics.
func (s *MempoolSwapEffectsSuite) TestNormalModePreservesCloutReadFailureSemantics(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg, _, _ := setupMempoolSwapEffectsSwap(c, ctx, mgr, 0)
	baseKeeper := mgr.Keeper()
	c.Assert(baseKeeper.DeleteMempoolSwap(ctx, msg.Tx.ID, msg.Index), IsNil)
	mgr.K = failOneCloutReadKeeper{Keeper: baseKeeper, failAddress: msg.Tx.FromAddress}

	_, event := executeMempoolSwapEffectsSwap(c, ctx, mgr, msg, mempoolSwapExecutionNormal)
	senderClout, err := baseKeeper.GetSwapperClout(ctx, msg.Tx.FromAddress)
	c.Assert(err, IsNil)
	c.Check(senderClout.Score.IsZero(), Equals, true)
	destinationClout, err := baseKeeper.GetSwapperClout(ctx, msg.Destination)
	c.Assert(err, IsNil)
	c.Check(destinationClout.Score.Equal(event.LiquidityFeeInRune), Equals, true)
}

// The direct AffiliateCollector mutation in SwapHandler is reachable only for
// protocol-created affiliate conversion swaps, which overwrite MsgSwap's
// destination after preserving a non-module destination in the signed memo.
// A lifecycle-backed inbound is constructed from its external memo and cannot
// create that mismatch: validation rejects the module destination first.
func (s *MempoolSwapEffectsSuite) TestExternalAffiliateCollectorDestinationCannotEnterLifecycle(c *C) {
	ctx, mgr := setupManagerForTest(c)
	collectorAddress, err := mgr.Keeper().GetModuleAddress(AffiliateCollectorName)
	c.Assert(err, IsNil)
	affiliateAlias := GetRandomTHORAddress()
	affiliate := NewTHORName("effects-affiliate", 1000, []THORNameAlias{{
		Chain:   common.THORChain,
		Address: affiliateAlias,
	}})
	mgr.Keeper().SetTHORName(ctx, affiliate)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"=:r:"+collectorAddress.String()+"::effects-affiliate:100",
	)
	tx.Chain = common.BTCChain
	msg := NewMsgSwap(
		tx,
		common.RuneAsset(),
		collectorAddress,
		cosmos.ZeroUint(),
		affiliateAlias,
		cosmos.NewUint(100),
		"", "", nil,
		MarketSwap,
		2, 1,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)

	err = NewSwapHandler(mgr).validate(ctx, *msg)
	c.Assert(err, ErrorMatches, "a network module cannot be the final destination of a swap memo")
}

func newStableReserveMempoolTestSwap(source, target common.Asset, index uint32) *MsgSwap {
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(source, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:"+target.String(),
	)
	msg := NewMsgSwap(
		tx,
		target,
		common.NoopAddress,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		MarketSwap,
		0, 0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	msg.Index = index
	return msg
}

// A direct stable-reserve fill mutates secured inventory rather than AMM pool
// state, so the reverse-swap rollback mechanism cannot reconstruct it. Keep
// only the direct-fill shape out of mempool execution; streams and limit swaps
// continue through the pools and remain eligible.
func (s *MempoolSwapEffectsSuite) TestStableReserveDirectFillIsNotMempoolEligible(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	amount := cosmos.NewUint(100 * common.One)

	c.Assert(willSwapExecuteAsStreaming(ctx, mgr, usdc, usdt, amount, MarketSwap, 0, 0), Equals, false)
	c.Check(isMempoolSwapEligible(ctx, mgr, usdc, usdt, amount, MarketSwap, 0, 0), Equals, false)

	// Explicit streams already bypass StableReserveManager.TrySwap.
	c.Check(isMempoolSwapEligible(ctx, mgr, usdc, usdt, amount, MarketSwap, 1, 2), Equals, true)
	// StableReserveManager.TrySwap only handles MARKET swaps.
	c.Check(isMempoolSwapEligible(ctx, mgr, usdc, usdt, amount, LimitSwap, 0, 0), Equals, true)
}

func (s *MempoolSwapEffectsSuite) TestRagnarokRejectsNewMempoolSwapAdmission(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(types.AdvSwapQueueModeEnabled))
	amount := cosmos.NewUint(common.One)
	c.Check(isMempoolSwapEligible(ctx, mgr, common.BTCAsset, common.ETHAsset, amount, MarketSwap, 1, 2), Equals, true)

	mgr.Keeper().SetRagnarokBlockHeight(ctx, ctx.BlockHeight())
	c.Check(isMempoolSwapEligible(ctx, mgr, common.BTCAsset, common.ETHAsset, amount, MarketSwap, 1, 2), Equals, false)
	c.Check(isMempoolSwapEligible(ctx, mgr, common.BTCAsset, common.ETHAsset, amount, LimitSwap, 0, 0), Equals, false)
}

// The eligibility check is intentionally paired with an execution-mode guard:
// stable eligibility can change between queue admission and execution, and a
// reverse rollback must never accidentally take a newly available direct fill.
func (s *MempoolSwapEffectsSuite) TestSpeculativeAndRollbackModesBypassStableReserve(c *C) {
	for _, mode := range []mempoolSwapExecutionMode{
		mempoolSwapExecutionSpeculative,
		mempoolSwapExecutionRollback,
	} {
		ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
		fundStableReserveSurplus(c, ctx, mgr, usdt, cosmos.NewUint(150*common.One))
		moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
		msg := newStableReserveMempoolTestSwap(usdc, usdt, 0)

		if mode == mempoolSwapExecutionSpeculative {
			record := types.MempoolSwap{
				TxId:               msg.Tx.ID,
				Index:              msg.Index,
				VaultPubKey:        GetRandomPubKey(),
				ReservedTor:        cosmos.NewUint(common.One),
				AdmittedHeight:     ctx.BlockHeight(),
				UpdatedHeight:      ctx.BlockHeight(),
				NextRecoveryHeight: ctx.BlockHeight() + 100,
				Status:             types.MempoolSwapStatus_EXECUTING,
				LatestSwap:         *msg,
				PendingEffects:     types.MempoolSwapPendingEffects{},
			}
			c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		}

		executionCtx := withMempoolSwapExecutionMode(ctx, mode)
		executionCtx = executionCtx.WithValue(constants.CtxSkipSwapOutbounds, true)
		_, emit, err := NewSwapHandler(mgr).RunWithEmit(executionCtx, msg)
		c.Assert(err, IsNil)
		c.Check(emit.IsZero(), Equals, false)

		// A normal direct fill would deposit 100 USDC and withdraw 100 USDT.
		// Both non-normal modes instead execute pool math and leave the reserve
		// inventory exactly as it was.
		c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
		c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
	}
}
