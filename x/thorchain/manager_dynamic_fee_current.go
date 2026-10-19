package thorchain

import (
	"fmt"
	"slices"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type DynamicFeeMgr struct {
	keeper keeper.Keeper
}

func newDynamicFeeMgr(keeper keeper.Keeper) *DynamicFeeMgr {
	return &DynamicFeeMgr{keeper: keeper}
}

func isL1Asset(asset common.Asset) bool {
	return !asset.IsSyntheticAsset() && !asset.IsTradeAsset() && !asset.IsDerivedAsset() && !asset.IsSecuredAsset()
}

func dynamicFeePairKey(source, target common.Asset) (string, string, bool) {
	if !isL1Asset(source) || !isL1Asset(target) {
		return "", "", false
	}
	var a, b string
	switch {
	case source.IsRune():
		a, b = types.NormalizePairAssets(target.String(), common.RuneAsset().String())
	case target.IsRune():
		a, b = types.NormalizePairAssets(source.String(), common.RuneAsset().String())
	default:
		a, b = types.NormalizePairAssets(source.String(), target.String())
	}
	return a, b, true
}

func dynamicFeeWhitelistState(ctx cosmos.Context, k keeper.Keeper, thorname string) int64 {
	key := fmt.Sprintf("DYNAMICFEE-WHITELIST-%s", strings.ToUpper(thorname))
	val, err := k.GetMimir(ctx, key)
	if err != nil {
		return 0
	}
	return val
}

// dominantEligibleAffiliate returns the largest-bps affiliate that is both an
// existing THORName and whose dynamic-fee whitelist state appears in
// allowedStates. Filtering ineligible entries out of the selection (rather
// than picking-then-rejecting) prevents an integrator from listing an
// unparsable / address-form / unwhitelisted secondary affiliate at higher
// bps to steer the dynamic-fee selector to "no match" and collapse the floor
// to static — see ADR §4 free-rider discussion. First-wins on ties matches
// the prior largest-bps-only behavior.
//
// It also returns the winner's whitelist state, which selects the per-pair or
// global controller. Returns "", 0, false when the inputs are malformed, empty,
// or no affiliate satisfies the eligibility predicate.
func (m *DynamicFeeMgr) dominantEligibleAffiliate(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, allowedStates ...int64) (string, int64, bool) {
	if len(affiliates) == 0 || len(affiliates) != len(affiliateBps) {
		return "", 0, false
	}
	bestIdx := -1
	bestState := int64(0)
	bestBps := cosmos.ZeroUint()
	for i, bps := range affiliateBps {
		aff := affiliates[i]
		if aff == "" {
			continue
		}
		if !m.keeper.THORNameExists(ctx, aff) {
			continue
		}
		state := dynamicFeeWhitelistState(ctx, m.keeper, aff)
		if !slices.Contains(allowedStates, state) {
			continue
		}
		if bps.GT(bestBps) {
			bestBps = bps
			bestIdx = i
			bestState = state
		}
	}
	if bestIdx < 0 {
		return "", 0, false
	}
	return affiliates[bestIdx], bestState, true
}

// dynamicFeeRecordPair maps an L1 pair to the record key for a whitelist
// state: the pair itself in per-pair mode, the global placeholder in global
// mode.
func dynamicFeeRecordPair(state int64, pairA, pairB string) (string, string) {
	if types.IsGlobalDynamicFeeState(state) {
		return types.DynamicFeeGlobalPair, types.DynamicFeeGlobalPair
	}
	return pairA, pairB
}

// CreditSwap accumulates volume and fees for a (thorname, pair) into the
// current-epoch accumulator. volumeTor and feesTor are denominated in TOR
// (1e8-precision USD value) so that the gradient signal does not drift with
// the RUNE/USD price.
// countAsSwap distinguishes the first sub-swap of a streaming swap from its
// continuations so that SwapCount measures user swaps rather than intervals —
// otherwise a single 30-interval stream would clear a 30-swap liquidity gate
// on its own.
//
// It reports whether it credited a global-mode record. Every leg of a
// multi-leg swap lands on that same record, so the caller credits the swap's
// volume and count on the first such leg only.
func (m *DynamicFeeMgr) CreditSwap(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset, volumeTor, feesTor cosmos.Uint, countAsSwap bool) bool {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		return false
	}

	pairA, pairB, ok := dynamicFeePairKey(sourceAsset, targetAsset)
	if !ok {
		return false
	}

	// Monitor states (3, 4) accumulate the revenue signal too, without
	// affecting live fee charging.
	aff, state, ok := m.dominantEligibleAffiliate(ctx, affiliates, affiliateBps,
		types.DynamicFeeStatePerPair, types.DynamicFeeStateGlobal,
		types.DynamicFeeStatePerPairMonitor, types.DynamicFeeStateGlobalMonitor)
	if !ok {
		return false
	}
	pairA, pairB = dynamicFeeRecordPair(state, pairA, pairB)

	// Lazily create DynamicFeeRecord first, then write the accumulator —
	// DeleteAllDynamicFeeStateForThorname iterates records only and relies
	// on the invariant that every accumulator has a matching record. Writing
	// the record first ensures a record-write failure leaves no orphaned
	// accumulator behind.
	rec, err := m.keeper.GetDynamicFeeRecord(ctx, aff, pairA, pairB)
	if err != nil {
		ctx.Logger().Error("fail to get dynamic fee record", "error", err)
		return false
	}
	if rec.DynamicBps == 0 && len(rec.History) == 0 {
		rec.DynamicBps = nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
		rec.Thorname = aff
		rec.PairAssetA = pairA
		rec.PairAssetB = pairB
		if setErr := m.keeper.SetDynamicFeeRecord(ctx, rec); setErr != nil {
			ctx.Logger().Error("fail to create dynamic fee record", "error", setErr)
			return false
		}
	}

	affAcc, err := m.keeper.GetDynamicFeeAccumulator(ctx, aff, pairA, pairB)
	if err != nil {
		ctx.Logger().Error("fail to get thorname accumulator", "error", err)
		return false
	}
	affAcc.VolumeTor = affAcc.VolumeTor.Add(volumeTor)
	affAcc.FeesTor = affAcc.FeesTor.Add(feesTor)
	if countAsSwap {
		affAcc.SwapCount++
	}
	if setErr := m.keeper.SetDynamicFeeAccumulator(ctx, affAcc); setErr != nil {
		ctx.Logger().Error("fail to set thorname accumulator", "error", setErr)
		return false
	}
	return types.IsGlobalDynamicFeeState(state)
}

// CreditPoolVolume accumulates the pair's total swap volume for the current
// epoch, regardless of affiliate. This is the denominator that normalizes each
// thorname's fee signal against market-wide activity: without it the gradient
// reads epoch-to-epoch swings in overall trading as a response to the fee
// floor. It must not be conditioned on the affiliate list.
func (m *DynamicFeeMgr) CreditPoolVolume(ctx cosmos.Context, sourceAsset, targetAsset common.Asset, volumeTor cosmos.Uint) {
	if m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled) != 1 {
		return
	}
	pairA, pairB, ok := dynamicFeePairKey(sourceAsset, targetAsset)
	if !ok {
		return
	}
	acc, err := m.keeper.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	if err != nil {
		ctx.Logger().Error("fail to get dynamic fee pool accumulator", "error", err)
		return
	}
	acc.VolumeTor = acc.VolumeTor.Add(volumeTor)
	if err := m.keeper.SetDynamicFeePoolAccumulator(ctx, acc); err != nil {
		ctx.Logger().Error("fail to set dynamic fee pool accumulator", "error", err)
	}
}

// creditDynamicFeeForLegs credits the dynamic-fee accumulators for every
// L1↔RUNE leg in a swap event list. RUNE-denominated volume and fee values
// are converted to TOR (1e8-precision USD value) via DollarsPerRune so the
// gradient signal is independent of RUNE price moves.
//
// When DollarsPerRune is unavailable (no TOR anchors, or all halted), the
// entire credit is skipped: recording zeros would drag the window-mean fee
// signal toward zero and trigger spurious controller reversals on the next
// epoch boundary.
func creditDynamicFeeForLegs(ctx cosmos.Context, mgr Manager, swapEvents []*EventSwap, affiliates []string, affiliateBps []cosmos.Uint, countAsSwap bool) {
	dollarsPerRune := mgr.Keeper().DollarsPerRune(ctx)
	if dollarsPerRune.IsZero() {
		return
	}
	one := cosmos.NewUint(common.One)
	// A global-mode thorname credits every leg to one record. The user swap's
	// volume and count go in once, on the first leg; later legs add only their
	// fees, which keeps the fee numerator aligned with the per-leg pool volume.
	creditedGlobal := false
	for _, evt := range swapEvents {
		if evt == nil || len(evt.InTx.Coins) == 0 {
			continue
		}
		legSource := evt.InTx.Coins[0].Asset
		legTarget := evt.EmitAsset.Asset
		var legVolumeRune cosmos.Uint
		switch {
		case legSource.IsRune():
			legVolumeRune = evt.InTx.Coins[0].Amount
		case legTarget.IsRune():
			legVolumeRune = evt.EmitAsset.Amount.Add(evt.LiquidityFeeInRune)
		default:
			// No RUNE side on this leg — skip. Synth/trade/secured
			// legs don't touch an L1 pool.
			continue
		}
		legVolumeTor := legVolumeRune.Mul(dollarsPerRune).Quo(one)
		legFeesTor := evt.LiquidityFeeInRune.Mul(dollarsPerRune).Quo(one)
		// Pool volume first, and unconditionally: it is the normalization
		// denominator and must count non-affiliate flow too. Crediting it
		// before the affiliate credit also guarantees that any sealed epoch
		// carrying fees carries non-zero pool volume, which is what lets
		// sealEpochForRecord treat a zero pool volume as "pre-upgrade entry".
		//
		// ponytail: protocol-internal swaps that call swap() directly (POL,
		// affiliate-fee conversion) bypass this handler and are excluded.
		// That is arguably correct for a market-activity denominator; move
		// the hook to swap_current.go if it ever needs to be literally every
		// swap.
		mgr.DynamicFeeMgr().CreditPoolVolume(ctx, legSource, legTarget, legVolumeTor)
		if len(affiliates) == 0 {
			continue
		}
		creditVolumeTor, creditAsSwap := legVolumeTor, countAsSwap
		if creditedGlobal {
			creditVolumeTor, creditAsSwap = cosmos.ZeroUint(), false
		}
		if mgr.DynamicFeeMgr().CreditSwap(ctx, affiliates, affiliateBps,
			legSource, legTarget, creditVolumeTor, legFeesTor, creditAsSwap) {
			creditedGlobal = true
		}
	}
}

func (m *DynamicFeeMgr) GetDynamicBps(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset) uint64 {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		return 0
	}

	pairA, pairB, ok := dynamicFeePairKey(sourceAsset, targetAsset)
	if !ok {
		return 0
	}

	// Only the charging states govern live fees; monitor states (3, 4) are
	// excluded so they do not drive the floor.
	thorname, state, ok := m.dominantEligibleAffiliate(ctx, affiliates, affiliateBps,
		types.DynamicFeeStatePerPair, types.DynamicFeeStateGlobal)
	if !ok {
		return 0
	}
	pairA, pairB = dynamicFeeRecordPair(state, pairA, pairB)

	rec, err := m.keeper.GetDynamicFeeRecord(ctx, thorname, pairA, pairB)
	if err != nil {
		return 0
	}
	if rec.DynamicBps == 0 && len(rec.History) == 0 {
		return nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
	}

	return rec.DynamicBps
}

func (m *DynamicFeeMgr) EndBlock(ctx cosmos.Context) error {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		// Drop any accumulator rows still carrying pre-disable volume/fees. If
		// we left them, a later re-enable would add new volume on top of old
		// and seal the mix into a wrong epoch.
		m.clearAccumulators(ctx)
		m.clearPoolAccumulators(ctx)
		return nil
	}

	epochBlocks := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEpochBlocks)
	if epochBlocks <= 0 {
		return nil
	}

	if ctx.BlockHeight()%epochBlocks != 0 {
		return nil
	}

	// Reclaim state for thornames removed from the whitelist. Runs at epoch
	// boundaries only; live fee charging and accumulation are already gated
	// on the whitelist mimir, so delaying cleanup to epoch has no user-visible
	// effect and avoids an O(records) iterator scan every block.
	m.cleanupRemovedThornames(ctx)

	// Rebase stored LastActiveEpoch values when governance has changed
	// L1DynamicFeeEpochBlocks since the last seal. Without this, a shrink
	// inflates (currentEpoch - LastActiveEpoch) and would prematurely prune
	// otherwise-live records; a grow was already handled (underflow guard
	// below). Must run before sealing so fresh seals and pruning both see
	// a consistent epoch scale.
	m.rebaseOnEpochBlocksChange(ctx, epochBlocks)

	currentEpoch := uint64(ctx.BlockHeight() / epochBlocks)
	// Negative mimir values are treated as zero to prevent int64→uint64 wrap
	// (a -1 ceiling would otherwise clamp every record to MaxUint64 bps).
	floorBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
	ceilingBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeCeilingBPS))
	stepBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeStepBPS))
	deadbandBps := cosmos.NewUint(nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeDeadbandBPS)))
	windowEpochs := clampWindowEpochs(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeWindowEpochs))
	tenK := cosmos.NewUint(10000)
	minSwaps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeMinSwaps))
	// Mimir carries whole USD; widen in Uint so a fat-fingered value cannot
	// overflow uint64 on the way to TOR (1e8) precision.
	minVolumeTor := cosmos.NewUint(nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeMinVolumeUSD))).MulUint64(common.One)
	params := dynamicFeeParams{
		floorBps:     floorBps,
		ceilingBps:   ceilingBps,
		stepBps:      stepBps,
		windowEpochs: windowEpochs,
		deadbandBps:  deadbandBps,
		tenK:         tenK,
		minSwaps:     minSwaps,
		minVolumeTor: minVolumeTor,
		probeEpochs:  max(1, nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeProbeEpochs))),
	}

	// Seal all accumulators. Only thorname-keyed accumulators exist under the
	// revenue-gradient algorithm; the pair-wide accumulator used by the older
	// share-based design is gone.
	accIter := m.keeper.GetDynamicFeeAccumulatorIterator(ctx)
	var thornameAccs []types.DynamicFeeAccumulator
	for ; accIter.Valid(); accIter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := m.keeper.Cdc().Unmarshal(accIter.Value(), &acc); err != nil {
			continue
		}
		// Accumulators written before pool volume was tracked unmarshal with a
		// nil Uint, which panics on Add/Marshal.
		if acc.PoolVolumeTor.IsNil() {
			acc.PoolVolumeTor = cosmos.ZeroUint()
		}
		thornameAccs = append(thornameAccs, acc)
	}
	accIter.Close()

	// A global-mode accumulator spans every L1 pair, so its denominator is
	// the epoch's volume across all of them.
	totalPoolVolumeTor := dynamicFeeTotalPoolVolume(ctx, m.keeper)

	for _, acc := range thornameAccs {
		// Fold this epoch's pair-wide volume into the thorname accumulator
		// before deciding. A rolled accumulator then carries a pool volume
		// spanning exactly the same epochs as its fees, which keeps the
		// normalized signal comparable across merged epochs.
		if acc.PairAssetA == types.DynamicFeeGlobalPair {
			acc.PoolVolumeTor = acc.PoolVolumeTor.Add(totalPoolVolumeTor)
		} else {
			poolAcc, err := m.keeper.GetDynamicFeePoolAccumulator(ctx, acc.PairAssetA, acc.PairAssetB)
			if err != nil {
				ctx.Logger().Error("fail to get dynamic fee pool accumulator", "error", err)
			} else {
				acc.PoolVolumeTor = acc.PoolVolumeTor.Add(poolAcc.VolumeTor)
			}
		}

		sealed, err := m.sealEpochForRecord(ctx, acc, currentEpoch, params)
		if err != nil {
			ctx.Logger().Error("fail to seal dynamic fee epoch", "error", err, "thorname", acc.Thorname)
			continue
		}
		if !sealed {
			// Too thin to carry signal — roll the accumulator into the next
			// epoch instead of stepping the floor on noise.
			if err := m.keeper.SetDynamicFeeAccumulator(ctx, acc); err != nil {
				ctx.Logger().Error("fail to roll thorname accumulator", "error", err)
			}
			continue
		}
		if err := m.keeper.DeleteDynamicFeeAccumulator(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB); err != nil {
			ctx.Logger().Error("fail to delete thorname accumulator", "error", err)
		}
	}

	// Pool accumulators are per-epoch by construction; every thorname
	// accumulator that needed this epoch's slice has already folded it in.
	m.clearPoolAccumulators(ctx)

	// Prune inactive records. Collect-then-delete to avoid mutating the store
	// under an open IAVL iterator, which can skip entries or panic depending
	// on the backend.
	recIter := m.keeper.GetDynamicFeeRecordIterator(ctx)
	var staleRecs []types.DynamicFeeRecord
	for ; recIter.Valid(); recIter.Next() {
		var rec types.DynamicFeeRecord
		if err := m.keeper.Cdc().Unmarshal(recIter.Value(), &rec); err != nil {
			continue
		}
		// Guard against uint64 underflow if epochBlocks was increased via Mimir
		// (which shrinks currentEpoch) while older records still carry the
		// larger LastActiveEpoch computed under the old epoch size.
		if rec.LastActiveEpoch > currentEpoch {
			continue
		}
		if currentEpoch-rec.LastActiveEpoch >= types.MaxDynamicFeeHistory {
			staleRecs = append(staleRecs, rec)
		}
	}
	recIter.Close()

	for _, rec := range staleRecs {
		if err := m.keeper.DeleteDynamicFeeRecord(ctx, rec.Thorname, rec.PairAssetA, rec.PairAssetB); err != nil {
			ctx.Logger().Error("fail to prune dynamic fee record", "error", err)
		}
	}

	return nil
}

// dynamicFeeParams carries the governance-controlled controller settings,
// read once per epoch boundary.
type dynamicFeeParams struct {
	floorBps, ceilingBps, stepBps uint64
	windowEpochs                  int
	deadbandBps, tenK             cosmos.Uint
	minSwaps                      uint64
	minVolumeTor                  cosmos.Uint
	probeEpochs                   uint64
}

// sealEpochForRecord appends this epoch's entry, decides the next
// dynamic_bps (via the bracket search until it converges, then the
// closed-loop revenue gradient), and emits a dynamic_l1_fee_update event on a
// bps change or when the search converges.
//
// It returns sealed=false when the epoch is too thin to carry signal (below
// the minimum swap count or volume). The caller then keeps the accumulator so
// this epoch's volume, fees and swap count roll into the next one. Rolling is
// deliberately unbounded: an integration that never reaches the threshold
// should never step the floor, which is the whole point of the gate. It is
// safe because the decision signal is normalized against pool volume and so is
// comparable across spans of different length.
func (m *DynamicFeeMgr) sealEpochForRecord(ctx cosmos.Context, acc types.DynamicFeeAccumulator, currentEpoch uint64, p dynamicFeeParams) (bool, error) {
	rec, err := m.keeper.GetDynamicFeeRecord(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB)
	if err != nil {
		return false, err
	}

	if acc.SwapCount < p.minSwaps || acc.VolumeTor.LT(p.minVolumeTor) {
		// Keep the record alive while it rolls, otherwise a permanently
		// sub-threshold record is pruned after MaxDynamicFeeHistory idle
		// epochs and leaves its accumulator orphaned.
		//
		// ponytail: the roll fires every epoch the accumulator exists, even
		// one that saw no new swaps, so an integration that stops entirely
		// pins its record forever (one record + one accumulator per dead
		// pair; de-whitelisting still reclaims it). Advance LastActiveEpoch
		// only on a non-zero SwapCount delta if that ever matters.
		rec.LastActiveEpoch = currentEpoch
		return false, m.keeper.SetDynamicFeeRecord(ctx, rec)
	}

	// Drop history written before pool volume was tracked: those entries hold
	// raw fee levels, which are not comparable with the normalized signal.
	// Safe to run on every seal because CreditPoolVolume is credited ahead of
	// CreditSwap for the same leg, so a sealed epoch with fees always has
	// non-zero pool volume.
	rec.History = slices.DeleteFunc(rec.History, func(e types.DynamicFeeEpochRecord) bool {
		return e.PoolVolumeTor.IsNil() || e.PoolVolumeTor.IsZero()
	})

	oldBps := rec.DynamicBps

	// Append the new epoch entry first so the gradient calculation can treat
	// the most-recent history entry as "this epoch". bps_at_close records the
	// bps that actually governed this epoch's fees_tor; rec.DynamicBps is then
	// advanced to the next epoch's setting below.
	rec.AppendHistory(types.DynamicFeeEpochRecord{
		Epoch:         currentEpoch,
		VolumeTor:     acc.VolumeTor,
		FeesTor:       acc.FeesTor,
		BpsAtClose:    oldBps,
		SwapCount:     acc.SwapCount,
		PoolVolumeTor: acc.PoolVolumeTor,
	})

	var reason string
	var newBps uint64
	var feesBefore, feesAfter, deltaPctBps cosmos.Uint
	if rec.SearchDone {
		reason, newBps, feesBefore, feesAfter, deltaPctBps = decideNextBps(rec, oldBps, p.floorBps, p.ceilingBps, p.stepBps, p.windowEpochs, p.deadbandBps, p.tenK)
	} else {
		reason, newBps, feesBefore, feesAfter = searchStep(&rec, oldBps, p)
		deltaPctBps = cosmos.ZeroUint()
	}
	rec.DynamicBps = newBps
	rec.LastActiveEpoch = currentEpoch

	if err := m.keeper.SetDynamicFeeRecord(ctx, rec); err != nil {
		return false, err
	}

	if newBps != oldBps || reason == "search_converged" {
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent(
				types.DynamicL1FeeUpdateEventType,
				cosmos.NewAttribute("thorname", acc.Thorname),
				cosmos.NewAttribute("pair", fmt.Sprintf("%s|%s", acc.PairAssetA, acc.PairAssetB)),
				cosmos.NewAttribute("epoch", fmt.Sprintf("%d", currentEpoch)),
				cosmos.NewAttribute("old_bps", fmt.Sprintf("%d", oldBps)),
				cosmos.NewAttribute("new_bps", fmt.Sprintf("%d", newBps)),
				cosmos.NewAttribute("fees_before", feesBefore.String()),
				cosmos.NewAttribute("fees_after", feesAfter.String()),
				cosmos.NewAttribute("delta_pct_bps", deltaPctBps.String()),
				cosmos.NewAttribute("reason", reason),
				cosmos.NewAttribute("swap_count", fmt.Sprintf("%d", acc.SwapCount)),
				cosmos.NewAttribute("pool_volume_tor", acc.PoolVolumeTor.String()),
			),
		)
	}

	return true, nil
}

// decideNextBps implements the closed-loop revenue gradient rule for one
// (thorname, pair) record. The newest history entry must already hold this
// epoch's FeesTor. It returns the reason string, next bps, and the window
// averages used in the decision (for event emission).
//
// Rule:
//   - No prior bps change in history → cold_start_probe (move up one step,
//     or down one step when already at the ceiling).
//   - Previous direction == +1 and fees went up → continue_up (raise).
//   - Previous direction == +1 and fees went down → reverse_down (lower).
//   - Previous direction == -1 and fees went up → continue_down (lower).
//   - Previous direction == -1 and fees went down → reverse_up (raise).
//   - |Δ%| below deadband → hold.
func decideNextBps(rec types.DynamicFeeRecord, oldBps, floorBps, ceilingBps, stepBps uint64, windowEpochs int, deadbandBps, tenK cosmos.Uint) (reason string, newBps uint64, feesBefore, feesAfter, deltaPctBps cosmos.Uint) {
	changeIdx, hasChange := lastBpsChangeBefore(rec.History, len(rec.History)-1)
	if !hasChange {
		// Probe up. A record already at the ceiling cannot, and a clamped
		// probe records no bps change, so it would cold-start -- and stay
		// put -- every epoch from then on, never testing a lower fee. Probe
		// down instead.
		if up := clampBps(oldBps+stepBps, floorBps, ceilingBps); up != oldBps {
			return "cold_start_probe", up, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()
		}
		return "cold_start_probe", clampBpsDown(oldBps, stepBps, floorBps), cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()
	}

	directionUp := rec.History[changeIdx].BpsAtClose > rec.History[changeIdx-1].BpsAtClose
	feesBefore = windowMeanSignal(rec.History, changeIdx-windowEpochs, changeIdx)
	// Use only the latest post-change epochs instead of averaging every
	// epoch since the last bps move.
	feesAfter = windowMeanSignal(rec.History, max(changeIdx, len(rec.History)-windowEpochs), len(rec.History))

	if feesBefore.IsZero() {
		return "hold", oldBps, feesBefore, feesAfter, cosmos.ZeroUint()
	}

	var absDelta cosmos.Uint
	feesIncreased := feesAfter.GT(feesBefore)
	if feesIncreased {
		absDelta = feesAfter.Sub(feesBefore)
	} else {
		absDelta = feesBefore.Sub(feesAfter)
	}
	deltaPctBps = common.GetSafeShare(absDelta, feesBefore, tenK)

	if deltaPctBps.LT(deadbandBps) {
		return "hold", oldBps, feesBefore, feesAfter, deltaPctBps
	}

	switch {
	case directionUp && feesIncreased:
		return "continue_up", clampBps(oldBps+stepBps, floorBps, ceilingBps), feesBefore, feesAfter, deltaPctBps
	case directionUp && !feesIncreased:
		return "reverse_down", clampBpsDown(oldBps, stepBps, floorBps), feesBefore, feesAfter, deltaPctBps
	case !directionUp && feesIncreased:
		return "continue_down", clampBpsDown(oldBps, stepBps, floorBps), feesBefore, feesAfter, deltaPctBps
	default:
		return "reverse_up", clampBps(oldBps+stepBps, floorBps, ceilingBps), feesBefore, feesAfter, deltaPctBps
	}
}

// searchStep runs one epoch of the bracket search for a record whose newest
// history entry was governed by oldBps. It credits that epoch's signal to the
// probe for oldBps; once the probe holds probeEpochs epochs it moves to the
// next untested bps, or converges. It returns the reason, the next bps, and
// the best and current probe means (for event emission).
//
// On convergence the record switches to the +/-step controller at the best
// bps. Its history is cleared so that controller starts fresh there instead of
// reading the search's jumps as its own steps; the probes are kept so queries
// still show what the search measured.
func searchStep(rec *types.DynamicFeeRecord, oldBps uint64, p dynamicFeeParams) (reason string, newBps uint64, bestMean, currentMean cosmos.Uint) {
	bestMean, currentMean = cosmos.ZeroUint(), cosmos.ZeroUint()
	latest, ok := rec.LatestHistory()
	if !ok {
		return "hold", oldBps, bestMean, currentMean
	}
	signal, ok := latest.NormalizedSignal()
	if !ok {
		// No pool volume to normalize against: this epoch says nothing about
		// oldBps, so measure it again.
		return "hold", oldBps, bestMean, currentMean
	}

	idx := slices.IndexFunc(rec.Probes, func(pr types.DynamicFeeProbe) bool { return pr.Bps == oldBps })
	if idx < 0 {
		rec.Probes = append(rec.Probes, types.DynamicFeeProbe{Bps: oldBps, SignalSum: cosmos.ZeroUint()})
		idx = len(rec.Probes) - 1
	}
	probe := &rec.Probes[idx]
	if probe.SignalSum.IsNil() {
		probe.SignalSum = cosmos.ZeroUint()
	}
	probe.SignalSum = probe.SignalSum.Add(signal)
	probe.Epochs++
	currentMean = probe.Mean()
	if probe.Epochs < p.probeEpochs {
		return "search_measure", oldBps, bestMean, currentMean
	}

	best, next, done := nextSearchBps(rec.Probes, p.floorBps, p.ceilingBps, p.deadbandBps, p.tenK)
	if bi := slices.IndexFunc(rec.Probes, func(pr types.DynamicFeeProbe) bool { return pr.Bps == best }); bi >= 0 {
		bestMean = rec.Probes[bi].Mean()
	}
	if done {
		rec.SearchDone = true
		rec.History = nil
		return "search_converged", best, bestMean, currentMean
	}
	return "search_probe", next, bestMean, currentMean
}

// nextSearchBps picks the next bps for the bracket search from the completed
// probes within [floorBps, ceilingBps].
//
// The best probe is the one with the highest mean signal, except that a
// higher bps only displaces a lower one when it beats it by more than the
// deadband: on a near-tie the lower fee wins. Assuming revenue is unimodal in
// bps, the optimum lies between the best probe's nearest tested neighbours, so
// only those two gaps are searched. The larger gap is tested next (ties go to
// the lower side): an untested floor or ceiling at the edge of a gap is tested
// first, otherwise the gap's midpoint. From a fresh record at the floor this
// tests the floor, the ceiling, then the midpoint (1, 20, 10 on defaults),
// then halves the gap around the best value. When both gaps are empty the
// search is done and best is the answer.
//
// With no completed probe in range (the range moved under the search), it
// starts again from the floor.
func nextSearchBps(probes []types.DynamicFeeProbe, floorBps, ceilingBps uint64, deadbandBps, tenK cosmos.Uint) (best, next uint64, done bool) {
	inRange := make([]types.DynamicFeeProbe, 0, len(probes))
	for _, pr := range probes {
		if pr.Epochs > 0 && pr.Bps >= floorBps && pr.Bps <= ceilingBps {
			inRange = append(inRange, pr)
		}
	}
	if len(inRange) == 0 {
		return floorBps, floorBps, false
	}
	slices.SortFunc(inRange, func(a, b types.DynamicFeeProbe) int {
		switch {
		case a.Bps < b.Bps:
			return -1
		case a.Bps > b.Bps:
			return 1
		}
		return 0
	})

	bestIdx := 0
	hurdle := tenK.Add(deadbandBps)
	for i := 1; i < len(inRange); i++ {
		// mean_i > mean_best * (1 + deadband), kept in integers.
		if inRange[i].Mean().Mul(tenK).GT(inRange[bestIdx].Mean().Mul(hurdle)) {
			bestIdx = i
		}
	}
	best = inRange[bestIdx].Bps

	// Nearest tested values strictly below and above best. Scanning by value
	// rather than taking the adjacent indices keeps the gap arithmetic from
	// underflowing should two probes ever share a bps.
	var lo, hi uint64
	foundLo, foundHi := false, false
	for _, pr := range inRange {
		if pr.Bps < best {
			lo, foundLo = pr.Bps, true
		}
		if pr.Bps > best && !foundHi {
			hi, foundHi = pr.Bps, true
		}
	}

	var leftCount, leftNext, rightCount, rightNext uint64
	if foundLo {
		leftCount, leftNext = best-lo-1, (lo+best)/2
	} else if best > floorBps {
		leftCount, leftNext = best-floorBps, floorBps
	}
	if foundHi {
		rightCount, rightNext = hi-best-1, (best+hi)/2
	} else if best < ceilingBps {
		rightCount, rightNext = ceilingBps-best, ceilingBps
	}

	switch {
	case leftCount == 0 && rightCount == 0:
		return best, best, true
	case rightCount > leftCount:
		return best, rightNext, false
	default:
		return best, leftNext, false
	}
}

// dynamicFeeTotalPoolVolume sums this epoch's pool accumulators across every
// L1 pair: the denominator of a global-mode record.
func dynamicFeeTotalPoolVolume(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	total := cosmos.ZeroUint()
	iter := k.GetDynamicFeePoolAccumulatorIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeePoolAccumulator
		if err := k.Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		if !acc.VolumeTor.IsNil() {
			total = total.Add(acc.VolumeTor)
		}
	}
	return total
}

// lastBpsChangeBefore walks history backward before upToExclusive and returns
// the index of the most-recent entry whose bps_at_close differs from its
// predecessor. The entry at upToExclusive is intentionally excluded so the
// latest bps transition is not evaluated until another epoch has been sealed.
// Returns (0, false) when history lacks two entries to compare.
func lastBpsChangeBefore(history []types.DynamicFeeEpochRecord, upToExclusive int) (int, bool) {
	if upToExclusive < 1 {
		return 0, false
	}
	for i := upToExclusive - 1; i >= 1; i-- {
		if history[i].BpsAtClose != history[i-1].BpsAtClose {
			return i, true
		}
	}
	return 0, false
}

// windowMeanSignal returns the mean normalized fee signal
// (fees_tor / pool_volume_tor, scaled by types.SignalScale) across
// history[lo:hi] with bounds clamped into the slice. Normalizing against the
// pair's total volume removes market-wide activity from the gradient: the
// median epoch-volume CV of ~1.5 otherwise swamps a 1 bps step worth ~33% of
// the fee rate. Entries with no recorded pool volume are skipped; an empty
// window returns zero, which decideNextBps reads as "hold".
func windowMeanSignal(history []types.DynamicFeeEpochRecord, lo, hi int) cosmos.Uint {
	if lo < 0 {
		lo = 0
	}
	if hi > len(history) {
		hi = len(history)
	}
	sum := cosmos.ZeroUint()
	count := uint64(0)
	for i := lo; i < hi; i++ {
		signal, ok := history[i].NormalizedSignal()
		if !ok {
			continue
		}
		sum = sum.Add(signal)
		count++
	}
	if count == 0 {
		return cosmos.ZeroUint()
	}
	return sum.QuoUint64(count)
}

func clampBps(candidate, floorBps, ceilingBps uint64) uint64 {
	if candidate > ceilingBps {
		return ceilingBps
	}
	if candidate < floorBps {
		return floorBps
	}
	return candidate
}

func clampBpsDown(oldBps, stepBps, floorBps uint64) uint64 {
	if oldBps > stepBps && oldBps-stepBps >= floorBps {
		return oldBps - stepBps
	}
	return floorBps
}

// nonNegUint64 converts a governance-controlled int64 to uint64, treating
// negatives as 0. Without this, a negative mimir value wraps to a very large
// uint64 and would make clampBps pin every record to MaxUint64.
func nonNegUint64(raw int64) uint64 {
	if raw < 0 {
		return 0
	}
	return uint64(raw)
}

// clampWindowEpochs bounds the configured gradient-window size to [1,
// MaxDynamicFeeHistory]. The lower bound avoids a degenerate empty "before"
// window; the upper bound avoids overrunning the bounded history buffer.
func clampWindowEpochs(raw int64) int {
	if raw < 1 {
		return 1
	}
	if raw > int64(types.MaxDynamicFeeHistory) {
		return types.MaxDynamicFeeHistory
	}
	return int(raw)
}

// rebaseOnEpochBlocksChange converts stored per-record LastActiveEpoch (and
// history epoch labels) from the previously-observed epoch scale to the
// current scale, preserving approximate block-height recency. Runs at most
// once per governance change; updates the stored observation afterward.
//
// Formula: newEpoch = oldEpoch * oldEB / newEB. This keeps the block-height
// range each epoch index corresponds to stable across the change, so the
// MaxDynamicFeeHistory pruning window continues to mean "records inactive
// for ~MaxDynamicFeeHistory * newEB blocks".
//
// History epoch labels are rebased alongside LastActiveEpoch to keep them
// coherent in querier output. Under a large shrink (newEB ≪ oldEB) distinct
// old epochs can collapse to the same new epoch number — this is cosmetic
// only since decideNextBps walks history by index, not epoch number.
func (m *DynamicFeeMgr) rebaseOnEpochBlocksChange(ctx cosmos.Context, currentEpochBlocks int64) {
	if currentEpochBlocks <= 0 {
		return
	}
	observed, err := m.keeper.GetLastObservedDynamicFeeEpochBlocks(ctx)
	if err != nil {
		ctx.Logger().Error("fail to read last-observed dynamic fee epoch blocks", "error", err)
		return
	}
	if observed == currentEpochBlocks {
		return
	}
	// First observation (fresh store, or feature newly enabled): just record
	// and return — nothing to rebase from.
	if observed <= 0 {
		m.keeper.SetLastObservedDynamicFeeEpochBlocks(ctx, currentEpochBlocks)
		return
	}

	iter := m.keeper.GetDynamicFeeRecordIterator(ctx)
	var records []types.DynamicFeeRecord
	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := m.keeper.Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	iter.Close()

	oldEB := uint64(observed)
	newEB := uint64(currentEpochBlocks)
	for _, rec := range records {
		rec.LastActiveEpoch = rec.LastActiveEpoch * oldEB / newEB
		for i := range rec.History {
			rec.History[i].Epoch = rec.History[i].Epoch * oldEB / newEB
		}
		if err := m.keeper.SetDynamicFeeRecord(ctx, rec); err != nil {
			ctx.Logger().Error("fail to rebase dynamic fee record", "error", err, "thorname", rec.Thorname)
		}
	}

	m.keeper.SetLastObservedDynamicFeeEpochBlocks(ctx, currentEpochBlocks)
}

// clearAccumulators deletes every DynamicFeeAccumulator row. Collect-then-delete
// to avoid mutating the store under an open IAVL iterator.
func (m *DynamicFeeMgr) clearAccumulators(ctx cosmos.Context) {
	iter := m.keeper.GetDynamicFeeAccumulatorIterator(ctx)
	var accs []types.DynamicFeeAccumulator
	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := m.keeper.Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		accs = append(accs, acc)
	}
	iter.Close()
	for _, acc := range accs {
		if err := m.keeper.DeleteDynamicFeeAccumulator(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB); err != nil {
			ctx.Logger().Error("fail to clear dynamic fee accumulator", "error", err, "thorname", acc.Thorname)
		}
	}
}

// clearPoolAccumulators drops every pair-wide volume accumulator. Called at
// each epoch boundary after the folds, and when the feature is disabled so a
// later re-enable does not fold pre-disable volume into its first epoch.
func (m *DynamicFeeMgr) clearPoolAccumulators(ctx cosmos.Context) {
	iter := m.keeper.GetDynamicFeePoolAccumulatorIterator(ctx)
	var accs []types.DynamicFeePoolAccumulator
	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeePoolAccumulator
		if err := m.keeper.Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		accs = append(accs, acc)
	}
	iter.Close()
	for _, acc := range accs {
		if err := m.keeper.DeleteDynamicFeePoolAccumulator(ctx, acc.PairAssetA, acc.PairAssetB); err != nil {
			ctx.Logger().Error("fail to delete dynamic fee pool accumulator", "error", err)
		}
	}
}

func (m *DynamicFeeMgr) cleanupRemovedThornames(ctx cosmos.Context) {
	for _, thorname := range m.keeper.GetRemovedDynamicFeeWhitelistThornames(ctx) {
		if err := m.keeper.DeleteAllDynamicFeeStateForThorname(ctx, thorname); err != nil {
			ctx.Logger().Error("fail to cleanup dynamic fee state for removed thorname", "thorname", thorname, "error", err)
		}
	}
}
