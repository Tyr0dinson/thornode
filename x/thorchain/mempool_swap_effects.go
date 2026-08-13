package thorchain

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// mempoolSwapExecutionMode makes the three swap execution contracts explicit.
// Normal swaps commit every ancillary effect immediately. Speculative swaps
// commit only the pool/custody transition and journal ancillary effects in the
// durable lifecycle record. Rollback swaps perform only the inverse pool and
// custody transition and must not create a second set of accounting effects.
type mempoolSwapExecutionMode uint8

const (
	mempoolSwapExecutionNormal mempoolSwapExecutionMode = iota
	mempoolSwapExecutionSpeculative
	mempoolSwapExecutionRollback
)

type (
	mempoolSwapExecutionModeContextKey     struct{}
	mempoolSwapExecutionIdentityContextKey struct{}
)

type mempoolSwapExecutionIdentity struct {
	txID  common.TxID
	index uint32
}

// withMempoolSwapExecutionMode selects the effect contract for all swap legs
// executed with ctx. Callers do not need to set the normal mode explicitly.
func withMempoolSwapExecutionMode(ctx cosmos.Context, mode mempoolSwapExecutionMode) cosmos.Context {
	return ctx.WithValue(mempoolSwapExecutionModeContextKey{}, mode)
}

func withMempoolSwapExecutionIdentity(ctx cosmos.Context, txID common.TxID, index uint32) cosmos.Context {
	return ctx.WithValue(mempoolSwapExecutionIdentityContextKey{}, mempoolSwapExecutionIdentity{
		txID:  txID,
		index: index,
	})
}

func getMempoolSwapExecutionMode(ctx cosmos.Context) mempoolSwapExecutionMode {
	mode, ok := ctx.Value(mempoolSwapExecutionModeContextKey{}).(mempoolSwapExecutionMode)
	if !ok {
		return mempoolSwapExecutionNormal
	}
	switch mode {
	case mempoolSwapExecutionSpeculative, mempoolSwapExecutionRollback:
		return mode
	default:
		return mempoolSwapExecutionNormal
	}
}

func getMempoolSwapExecutionIdentity(ctx cosmos.Context) (mempoolSwapExecutionIdentity, bool) {
	identity, ok := ctx.Value(mempoolSwapExecutionIdentityContextKey{}).(mempoolSwapExecutionIdentity)
	if !ok || identity.txID.IsEmpty() {
		return mempoolSwapExecutionIdentity{}, false
	}
	return identity, true
}

func updatePendingMempoolSwapEffects(
	ctx cosmos.Context,
	mgr Manager,
	update func(*types.MempoolSwapPendingEffects),
) error {
	identity, ok := getMempoolSwapExecutionIdentity(ctx)
	if !ok {
		return fmt.Errorf("speculative swap is missing its lifecycle identity")
	}
	k := mgr.Keeper()
	record, found, err := k.GetMempoolSwap(ctx, identity.txID, identity.index)
	if err != nil {
		return fmt.Errorf("fail to get speculative swap lifecycle record: %w", err)
	}
	if !found {
		return fmt.Errorf("speculative swap lifecycle record not found: %s/%d", identity.txID, identity.index)
	}
	update(&record.PendingEffects)
	if err := k.SetMempoolSwap(ctx, record); err != nil {
		return fmt.Errorf("fail to save speculative swap effects: %w", err)
	}
	return nil
}

func newMempoolSwapPoolEffect(asset common.Asset) types.MempoolSwapPoolEffect {
	return types.MempoolSwapPoolEffect{
		Asset:                asset.GetLayer1Asset(),
		LiquidityFeeRune:     cosmos.ZeroUint(),
		SwapSlip:             cosmos.ZeroUint(),
		VolumeRune:           cosmos.ZeroUint(),
		VolumeAsset:          cosmos.ZeroUint(),
		ReserveRune:          cosmos.ZeroUint(),
		TcyRune:              cosmos.ZeroUint(),
		BalanceRuneIncrease:  cosmos.ZeroUint(),
		BalanceRuneDecrease:  cosmos.ZeroUint(),
		BalanceAssetIncrease: cosmos.ZeroUint(),
		BalanceAssetDecrease: cosmos.ZeroUint(),
	}
}

func findOrAppendPoolEffect(effects *types.MempoolSwapPendingEffects, asset common.Asset) *types.MempoolSwapPoolEffect {
	asset = asset.GetLayer1Asset()
	for i := range effects.Pools {
		if effects.Pools[i].Asset.Equals(asset) {
			return &effects.Pools[i]
		}
	}
	effects.Pools = append(effects.Pools, newMempoolSwapPoolEffect(asset))
	sort.Slice(effects.Pools, func(i, j int) bool {
		return effects.Pools[i].Asset.String() < effects.Pools[j].Asset.String()
	})
	for i := range effects.Pools {
		if effects.Pools[i].Asset.Equals(asset) {
			return &effects.Pools[i]
		}
	}
	panic("newly appended mempool swap pool effect not found")
}

func captureMempoolSwapPoolEffect(
	ctx cosmos.Context,
	mgr Manager,
	asset common.Asset,
	liquidityFeeRune, swapSlip, reserveRune, tcyRune cosmos.Uint,
) error {
	if liquidityFeeRune.IsZero() && swapSlip.IsZero() && reserveRune.IsZero() && tcyRune.IsZero() {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		effect := findOrAppendPoolEffect(effects, asset)
		effect.LiquidityFeeRune = effect.LiquidityFeeRune.Add(liquidityFeeRune)
		effect.SwapSlip = effect.SwapSlip.Add(swapSlip)
		effect.ReserveRune = effect.ReserveRune.Add(reserveRune)
		effect.TcyRune = effect.TcyRune.Add(tcyRune)
	})
}

// captureMempoolSwapPoolBalanceEffect records the exact balance mutation which
// speculative swap math has already written to a pool. It is not replayed at
// finality: end-block consumers subtract this overlay while the inbound is
// unfinalized, and finality makes the live balances authoritative by changing
// the lifecycle status.
func captureMempoolSwapPoolBalanceEffect(
	ctx cosmos.Context,
	mgr Manager,
	asset common.Asset,
	beforeRune, afterRune, beforeAsset, afterAsset cosmos.Uint,
) error {
	if beforeRune.Equal(afterRune) && beforeAsset.Equal(afterAsset) {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		effect := findOrAppendPoolEffect(effects, asset)
		if afterRune.GT(beforeRune) {
			effect.BalanceRuneIncrease = effect.BalanceRuneIncrease.Add(afterRune.Sub(beforeRune))
		} else if beforeRune.GT(afterRune) {
			effect.BalanceRuneDecrease = effect.BalanceRuneDecrease.Add(beforeRune.Sub(afterRune))
		}
		if afterAsset.GT(beforeAsset) {
			effect.BalanceAssetIncrease = effect.BalanceAssetIncrease.Add(afterAsset.Sub(beforeAsset))
		} else if beforeAsset.GT(afterAsset) {
			effect.BalanceAssetDecrease = effect.BalanceAssetDecrease.Add(beforeAsset.Sub(afterAsset))
		}
	})
}

func findOrAppendSupplyEffect(
	effects *types.MempoolSwapPendingEffects,
	asset common.Asset,
) *types.MempoolSwapSupplyEffect {
	for i := range effects.Supplies {
		if effects.Supplies[i].Asset.Native() == asset.Native() {
			return &effects.Supplies[i]
		}
	}
	effects.Supplies = append(effects.Supplies, types.MempoolSwapSupplyEffect{
		Asset:          asset,
		SupplyIncrease: cosmos.ZeroUint(),
		SupplyDecrease: cosmos.ZeroUint(),
	})
	sort.Slice(effects.Supplies, func(i, j int) bool {
		return effects.Supplies[i].Asset.Native() < effects.Supplies[j].Asset.Native()
	})
	for i := range effects.Supplies {
		if effects.Supplies[i].Asset.Native() == asset.Native() {
			return &effects.Supplies[i]
		}
	}
	panic("newly appended mempool swap supply effect not found")
}

// captureMempoolSwapSupplyEffect records an actual bank supply mutation made
// by speculative synth/derived execution. Rollback mode never calls this: its
// native mint/burn is the inverse operation which removes the live mutation,
// while lifecycle deletion removes only the aggregate ownership record.
func captureMempoolSwapSupplyEffect(
	ctx cosmos.Context,
	mgr Manager,
	asset common.Asset,
	amount cosmos.Uint,
	increase bool,
) error {
	if amount.IsZero() {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		effect := findOrAppendSupplyEffect(effects, asset)
		if increase {
			effect.SupplyIncrease = effect.SupplyIncrease.Add(amount)
		} else {
			effect.SupplyDecrease = effect.SupplyDecrease.Add(amount)
		}
	})
}

type mempoolSwapRollbackSupplyTarget struct {
	asset  common.Asset
	supply cosmos.Uint
}

// getMempoolSwapRollbackSupplyTargets derives the exact raw bank supply which
// must remain after this lifecycle is unwound. It removes only this record's
// gross contribution, preserving supply changes owned by later transactions.
// Raw bank supply is required here: GetTotalSupply intentionally reports zero
// after pool ragnarok and is therefore unsuitable for mint/burn reconciliation.
func getMempoolSwapRollbackSupplyTargets(
	ctx cosmos.Context,
	k keeper.Keeper,
	record types.MempoolSwap,
) ([]mempoolSwapRollbackSupplyTarget, error) {
	type aggregate struct {
		asset    common.Asset
		increase cosmos.Uint
		decrease cosmos.Uint
	}
	aggregates := make(map[string]aggregate, len(record.PendingEffects.Supplies))
	keys := make([]string, 0, len(record.PendingEffects.Supplies))
	for _, effect := range record.PendingEffects.Supplies {
		key := effect.Asset.Native()
		current, ok := aggregates[key]
		if !ok {
			keys = append(keys, key)
			current = aggregate{
				asset:    effect.Asset,
				increase: cosmos.ZeroUint(),
				decrease: cosmos.ZeroUint(),
			}
		}
		current.increase = current.increase.Add(effect.SupplyIncrease)
		current.decrease = current.decrease.Add(effect.SupplyDecrease)
		aggregates[key] = current
	}

	sort.Strings(keys)
	targets := make([]mempoolSwapRollbackSupplyTarget, 0, len(keys))
	for _, key := range keys {
		effect := aggregates[key]
		live := k.GetBankSupply(ctx, effect.asset)
		withDecrease := live.Add(effect.decrease)
		if effect.increase.GT(withDecrease) {
			return nil, fmt.Errorf(
				"mempool swap rollback supply effect exceeds live supply for %s: increase %s, live+decrease %s",
				effect.asset, effect.increase, withDecrease,
			)
		}
		targets = append(targets, mempoolSwapRollbackSupplyTarget{
			asset:  effect.asset,
			supply: withDecrease.Sub(effect.increase),
		})
	}
	return targets, nil
}

// reconcileMempoolSwapRollbackSupplies removes inverse-AMM rounding from bank
// supply. Reverse swaps are intentionally accounting-neutral but need not mint
// exactly what the speculative leg burned (notably for derived pools). The
// lifecycle's exact gross deltas define the authoritative post-rollback target.
//
// The caller owns an outer CacheContext. Every correction is verified against
// raw bank supply before returning; a RUNE cap fallback to Reserve therefore
// fails closed and the caller discards both the transfer and the rollback.
// Reconciliation adds no synthetic accounting effects or extra events; it is
// part of the inverse operation rather than a second economic action.
func reconcileMempoolSwapRollbackSupplies(
	ctx cosmos.Context,
	mgr Manager,
	targets []mempoolSwapRollbackSupplyTarget,
) error {
	k := mgr.Keeper()
	for _, target := range targets {
		current := k.GetBankSupply(ctx, target.asset)
		switch {
		case current.LT(target.supply):
			amount := target.supply.Sub(current)
			coin := common.NewCoin(target.asset, amount)
			if err := k.MintToModule(ctx, ModuleName, coin); err != nil {
				return fmt.Errorf("fail to reconcile mempool rollback supply mint for %s: %w", target.asset, err)
			}
			if actual := k.GetBankSupply(ctx, target.asset); !actual.Equal(target.supply) {
				return fmt.Errorf(
					"mempool rollback supply mint for %s reached %s, expected %s",
					target.asset, actual, target.supply,
				)
			}
			if err := k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)); err != nil {
				return fmt.Errorf("fail to move reconciled mempool rollback supply for %s to Asgard: %w", target.asset, err)
			}
		case current.GT(target.supply):
			amount := current.Sub(target.supply)
			coin := common.NewCoin(target.asset, amount)
			if err := k.SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(coin)); err != nil {
				return fmt.Errorf("fail to stage mempool rollback supply burn for %s: %w", target.asset, err)
			}
			if err := k.BurnFromModule(ctx, ModuleName, coin); err != nil {
				return fmt.Errorf("fail to reconcile mempool rollback supply burn for %s: %w", target.asset, err)
			}
			if actual := k.GetBankSupply(ctx, target.asset); !actual.Equal(target.supply) {
				return fmt.Errorf(
					"mempool rollback supply burn for %s reached %s, expected %s",
					target.asset, actual, target.supply,
				)
			}
		}
	}
	return nil
}

func captureMempoolSwapCloutEffect(ctx cosmos.Context, mgr Manager, address common.Address, score cosmos.Uint) error {
	if address.IsEmpty() || score.IsZero() {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		for i := range effects.Clout {
			if effects.Clout[i].Address.Equals(address) {
				effects.Clout[i].Score = effects.Clout[i].Score.Add(score)
				return
			}
		}
		effects.Clout = append(effects.Clout, types.MempoolSwapCloutEffect{
			Address: address,
			Score:   score,
		})
		sort.Slice(effects.Clout, func(i, j int) bool {
			return effects.Clout[i].Address.String() < effects.Clout[j].Address.String()
		})
	})
}

func captureMempoolSwapAffiliateEffect(ctx cosmos.Context, mgr Manager, thorname string, feeRune cosmos.Uint) error {
	if strings.TrimSpace(thorname) == "" || feeRune.IsZero() {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		for i := range effects.Affiliates {
			if strings.EqualFold(effects.Affiliates[i].Thorname, thorname) {
				effects.Affiliates[i].FeeRune = effects.Affiliates[i].FeeRune.Add(feeRune)
				return
			}
		}
		effects.Affiliates = append(effects.Affiliates, types.MempoolSwapAffiliateEffect{
			Thorname: thorname,
			FeeRune:  feeRune,
		})
		sort.Slice(effects.Affiliates, func(i, j int) bool {
			return strings.ToUpper(effects.Affiliates[i].Thorname) < strings.ToUpper(effects.Affiliates[j].Thorname)
		})
	})
}

// resolveAffiliateLiquidityFeeThorname preserves the user-swap REVSHARE
// attribution rule while returning the already-resolved THORName for a
// speculative journal. Protocol-internal affiliate conversion swaps are
// excluded so the same user fee cannot be attributed twice.
func resolveAffiliateLiquidityFeeThorname(
	ctx cosmos.Context,
	k keeper.Keeper,
	tx common.Tx,
	target common.Asset,
	destination common.Address,
) (string, bool) {
	attributeThorname, ok := resolveFirstAffiliateTHORName(ctx, k, tx.Memo)
	if !ok {
		return "", false
	}
	if affColAddr, err := k.GetModuleAddress(AffiliateCollectorName); err == nil && destination.Equals(affColAddr) {
		return "", false
	}
	if target.Equals(common.RuneAsset()) {
		if tn, err := k.GetTHORName(ctx, attributeThorname); err == nil && !tn.Owner.Empty() {
			if destination.String() == tn.Owner.String() || destination.Equals(tn.GetAlias(common.THORChain)) {
				return "", false
			}
		}
	}
	return attributeThorname, true
}

func captureMempoolSwapVolumeEffects(ctx cosmos.Context, mgr Manager, swapEvents []*EventSwap) error {
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		for _, evt := range swapEvents {
			if evt == nil || len(evt.InTx.Coins) == 0 || !isBaseLayerPool(evt.Pool) {
				continue
			}
			effect := findOrAppendPoolEffect(effects, evt.Pool)
			if evt.EmitAsset.Asset.GetLayer1Asset().Equals(evt.Pool.GetLayer1Asset()) {
				effect.VolumeAsset = effect.VolumeAsset.Add(evt.EmitAsset.Amount)
				effect.VolumeRune = effect.VolumeRune.Add(evt.InTx.Coins[0].Amount)
			} else {
				effect.VolumeAsset = effect.VolumeAsset.Add(evt.InTx.Coins[0].Amount)
				effect.VolumeRune = effect.VolumeRune.Add(evt.EmitAsset.Amount)
			}
		}
	})
}

func captureMempoolSwapDynamicFeeEffect(
	ctx cosmos.Context,
	mgr Manager,
	thorname string,
	pairAssetA, pairAssetB common.Asset,
	volumeTOR, feesTOR cosmos.Uint,
) error {
	if thorname == "" || (volumeTOR.IsZero() && feesTOR.IsZero()) {
		return nil
	}
	return updatePendingMempoolSwapEffects(ctx, mgr, func(effects *types.MempoolSwapPendingEffects) {
		for i := range effects.DynamicFees {
			effect := &effects.DynamicFees[i]
			if strings.EqualFold(effect.Thorname, thorname) &&
				effect.PairAssetA.Equals(pairAssetA) && effect.PairAssetB.Equals(pairAssetB) {
				effect.VolumeTor = effect.VolumeTor.Add(volumeTOR)
				effect.FeesTor = effect.FeesTor.Add(feesTOR)
				return
			}
		}
		effects.DynamicFees = append(effects.DynamicFees, types.MempoolSwapDynamicFeeEffect{
			Thorname:   thorname,
			PairAssetA: pairAssetA,
			PairAssetB: pairAssetB,
			VolumeTor:  volumeTOR,
			FeesTor:    feesTOR,
		})
		sort.Slice(effects.DynamicFees, func(i, j int) bool {
			left := strings.ToUpper(effects.DynamicFees[i].Thorname) + "|" + effects.DynamicFees[i].PairAssetA.String() + "|" + effects.DynamicFees[i].PairAssetB.String()
			right := strings.ToUpper(effects.DynamicFees[j].Thorname) + "|" + effects.DynamicFees[j].PairAssetA.String() + "|" + effects.DynamicFees[j].PairAssetB.String()
			return left < right
		})
	})
}

func loadCurrentMempoolSwapRecord(ctx cosmos.Context, mgr Manager, record types.MempoolSwap) (types.MempoolSwap, bool, error) {
	if record.TxId.IsEmpty() {
		return types.MempoolSwap{}, false, fmt.Errorf("mempool swap lifecycle identity is empty")
	}
	k := mgr.Keeper()
	current, found, err := k.GetMempoolSwap(ctx, record.TxId, record.Index)
	if err != nil {
		return types.MempoolSwap{}, false, err
	}
	if !found {
		// A completed finalization or rollback deletes the lifecycle record. A
		// replay is therefore an idempotent no-op, not an error.
		return types.MempoolSwap{}, false, nil
	}
	return current, true, nil
}

// applyPendingMempoolSwapEffectsCached commits all finality-gated effects and
// clears the journal in one nested cache transaction. It reloads the canonical
// record by identity so replaying a stale caller value cannot double-apply an
// already-cleared journal.
func applyPendingMempoolSwapEffectsCached(ctx cosmos.Context, mgr Manager, record types.MempoolSwap) error {
	cacheCtx, commit := ctx.CacheContext()
	current, found, err := loadCurrentMempoolSwapRecord(cacheCtx, mgr, record)
	if err != nil {
		return fmt.Errorf("fail to load mempool swap effects for finalization: %w", err)
	}
	if !found {
		return nil
	}

	for _, effect := range current.PendingEffects.Pools {
		if !effect.ReserveRune.IsZero() {
			coin := common.NewCoin(common.RuneAsset(), effect.ReserveRune)
			if err := mgr.Keeper().SendFromModuleToModule(cacheCtx, AsgardName, ReserveName, common.NewCoins(coin)); err != nil {
				return fmt.Errorf("fail to finalize speculative reserve liquidity fee for %s: %w", effect.Asset, err)
			}
		}
		if !effect.TcyRune.IsZero() {
			coin := common.NewCoin(common.RuneAsset(), effect.TcyRune)
			if err := mgr.Keeper().SendFromModuleToModule(cacheCtx, AsgardName, TCYClaimingName, common.NewCoins(coin)); err != nil {
				return fmt.Errorf("fail to finalize speculative TCY liquidity fee for %s: %w", effect.Asset, err)
			}
		}
		if !effect.LiquidityFeeRune.IsZero() {
			if err := mgr.Keeper().AddToLiquidityFees(cacheCtx, effect.Asset, effect.LiquidityFeeRune); err != nil {
				return fmt.Errorf("fail to finalize speculative liquidity fee for %s: %w", effect.Asset, err)
			}
		}
		if !effect.SwapSlip.IsZero() {
			if err := mgr.Keeper().AddToSwapSlip(cacheCtx, effect.Asset, cosmos.NewIntFromBigInt(effect.SwapSlip.BigInt())); err != nil {
				return fmt.Errorf("fail to finalize speculative swap slip for %s: %w", effect.Asset, err)
			}
		}
		if !effect.VolumeRune.IsZero() || !effect.VolumeAsset.IsZero() {
			volume, getErr := mgr.Keeper().GetVolume(cacheCtx, effect.Asset)
			if getErr != nil {
				volume = types.NewVolume(effect.Asset)
			}
			volume.ChangeRune = volume.ChangeRune.Add(effect.VolumeRune)
			volume.ChangeAsset = volume.ChangeAsset.Add(effect.VolumeAsset)
			if err := mgr.Keeper().SetVolume(cacheCtx, volume); err != nil {
				return fmt.Errorf("fail to finalize speculative volume for %s: %w", effect.Asset, err)
			}
		}
	}

	for _, effect := range current.PendingEffects.Clout {
		clout, err := mgr.Keeper().GetSwapperClout(cacheCtx, effect.Address)
		if err != nil {
			return fmt.Errorf("fail to get swapper clout for finality effect %s: %w", effect.Address, err)
		}
		clout.Score = clout.Score.Add(effect.Score)
		if err := mgr.Keeper().SetSwapperClout(cacheCtx, clout); err != nil {
			return fmt.Errorf("fail to finalize swapper clout for %s: %w", effect.Address, err)
		}
	}

	for _, effect := range current.PendingEffects.Affiliates {
		if err := mgr.Keeper().AddAffiliateLiquidityFee(cacheCtx, effect.Thorname, effect.FeeRune); err != nil {
			return fmt.Errorf("fail to finalize affiliate liquidity fee for %s: %w", effect.Thorname, err)
		}
	}

	for _, effect := range current.PendingEffects.DynamicFees {
		if err := mgr.DynamicFeeMgr().CreditSwapResolved(
			cacheCtx,
			effect.Thorname,
			effect.PairAssetA,
			effect.PairAssetB,
			effect.VolumeTor,
			effect.FeesTor,
		); err != nil {
			return fmt.Errorf("fail to finalize dynamic fee credit for %s: %w", effect.Thorname, err)
		}
	}

	for _, effect := range current.PendingEffects.Cleanups {
		if err := applyUnrefundableCoinCleanupStrictWithKey(
			cacheCtx,
			mgr,
			effect.Item,
			effect.BurnReason,
			effect.CleanupKey,
		); err != nil {
			return fmt.Errorf("fail to finalize deferred cleanup %s: %w", effect.CleanupKey, err)
		}
	}

	current.PendingEffects = types.MempoolSwapPendingEffects{}
	if err := mgr.Keeper().SetMempoolSwap(cacheCtx, current); err != nil {
		return fmt.Errorf("fail to clear finalized mempool swap effects: %w", err)
	}
	commit()
	return nil
}

// prepareMempoolSwapRollbackEffectsCached restores system-liquidity-fee RUNE
// to its pool before the inverse swap executes. Speculative execution deducted
// this RUNE from pool depth but deliberately left it in Asgard, so no module
// transfer is needed. Clearing the journal in the same cache prevents a retry
// in the current transaction from restoring it twice; an outer rollback still
// discards both changes together if the inverse swap later fails.
func prepareMempoolSwapRollbackEffectsCached(ctx cosmos.Context, mgr Manager, record types.MempoolSwap) error {
	cacheCtx, commit := ctx.CacheContext()
	current, found, err := loadCurrentMempoolSwapRecord(cacheCtx, mgr, record)
	if err != nil {
		return fmt.Errorf("fail to load mempool swap effects for rollback: %w", err)
	}
	if !found {
		return nil
	}

	for _, effect := range current.PendingEffects.Pools {
		restoreRune := effect.ReserveRune.Add(effect.TcyRune)
		if restoreRune.IsZero() {
			continue
		}
		pool, err := mgr.Keeper().GetPool(cacheCtx, effect.Asset)
		if err != nil {
			return fmt.Errorf("fail to get pool %s for speculative fee rollback: %w", effect.Asset, err)
		}
		pool.BalanceRune = pool.BalanceRune.Add(restoreRune)
		if err := mgr.Keeper().SetPool(cacheCtx, pool); err != nil {
			return fmt.Errorf("fail to restore speculative fee to pool %s: %w", effect.Asset, err)
		}
	}

	current.PendingEffects = types.MempoolSwapPendingEffects{}
	if err := mgr.Keeper().SetMempoolSwap(cacheCtx, current); err != nil {
		return fmt.Errorf("fail to clear rolled-back mempool swap effects: %w", err)
	}
	commit()
	return nil
}
