package thorchain

import (
	"fmt"
	"sort"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GasMgr implement GasManager interface which will store the gas related events happened in thorchain to memory
// emit GasEvent per block if there are any
type GasMgr struct {
	gasEvent          *EventGas
	outAssetGas       []OutAssetGas
	gasCount          map[common.Asset]int64
	constantsAccessor constants.ConstantValues
	keeper            keeper.Keeper
}

// newGasMgr create a new instance of GasMgr
func newGasMgr(constantsAccessor constants.ConstantValues, k keeper.Keeper) *GasMgr {
	return &GasMgr{
		gasEvent:          NewEventGas(),
		outAssetGas:       []OutAssetGas{},
		gasCount:          make(map[common.Asset]int64),
		constantsAccessor: constantsAccessor,
		keeper:            k,
	}
}

func (gm *GasMgr) reset() {
	gm.gasEvent = NewEventGas()
	gm.outAssetGas = []OutAssetGas{}
	gm.gasCount = make(map[common.Asset]int64)
}

// BeginBlock need to be called when a new block get created , update the internal EventGas to new one
func (gm *GasMgr) BeginBlock() {
	gm.reset()
}

// AddGasAsset for EndBlock's ProcessGas;
// add the outbound-Asset-associated Gas to the gas manager's outAssetGas,
// and optionally increment the gas manager's gasCount.
func (gm *GasMgr) AddGasAsset(outAsset common.Asset, gas common.Gas, increaseTxCount bool) {
	matched := false
	for i := range gm.outAssetGas {
		if !gm.outAssetGas[i].outAsset.Equals(outAsset) {
			continue
		}
		matched = true
		gm.outAssetGas[i].gas = gm.outAssetGas[i].gas.Add(gas...)
		break
	}
	if !matched {
		outAssetGas := OutAssetGas{
			outAsset: outAsset,
			gas:      common.Gas(common.NewCoins(gas...)), // Copied contents
		}
		gm.outAssetGas = append(gm.outAssetGas, outAssetGas)
	}

	// Update transaction count for each gas asset.
	if !increaseTxCount {
		return
	}

	incremented := map[common.Asset]bool{}
	for i := range gas {
		// Only increment each distinct gas asset's count by 1 maximum.
		if incremented[gas[i].Asset] {
			continue
		}
		gm.gasCount[gas[i].Asset]++
		incremented[gas[i].Asset] = true
	}
}

// GetGas return gas
func (gm *GasMgr) GetGas() common.Gas {
	// Collect gas by gas asset.
	gas := common.Gas{}
	for i := range gm.outAssetGas {
		gas = gas.Add(gm.outAssetGas[i].gas...)
	}
	return gas
}

// GetAssetOutboundFee returns current outbound fee for the asset. fee = chainBaseFee *
// assetDOFM (asset-specific Dynamic Outbound Fee Multiplier)
// - asset: the asset to calculate the fee for
// - inRune: whether the fee should be returned in RUNE. If false the fee is returned in
// asset units.
func (gm *GasMgr) GetAssetOutboundFee(ctx cosmos.Context, asset common.Asset, inRune bool) (cosmos.Uint, error) {
	// If the asset is native to THORChain, no need to charge an outbound fee.
	if asset.IsNative() {
		return cosmos.ZeroUint(), nil
	}

	chainOutboundFee, err := gm.keeper.GetNetworkFee(ctx, asset.GetChain())
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	if err = chainOutboundFee.Valid(); err != nil {
		// If the network fee is invalid, usually because consensus hasn't been reached, a
		// fee can't be deducted. So return 0 and no error
		return cosmos.ZeroUint(), nil
	}

	// Outbound fees are committed immediately, so price them without pool
	// mutations owned by still-reversible mempool swaps.
	poolView := newFinalityPoolView(gm.keeper)
	gasPool, _, err := poolView.getPool(ctx, asset.GetChain().GetGasAsset())
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	minOutboundUSD, err := gm.keeper.GetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String())
	if minOutboundUSD < 0 || err != nil {
		minOutboundUSD = gm.constantsAccessor.GetInt64Value(constants.MinimumL1OutboundFeeUSD)
	}
	runeUSDPrice := gm.keeper.DollarsPerRune(ctx)
	minAsset := cosmos.ZeroUint()
	if !runeUSDPrice.IsZero() {
		// since MinOutboundUSD is in USD value , thus need to figure out how much RUNE
		// here use GetShare instead GetSafeShare it is because minOutboundUSD can set to more than $1
		minOutboundInRune := common.GetUncappedShare(cosmos.NewUint(uint64(minOutboundUSD)),
			runeUSDPrice,
			cosmos.NewUint(common.One))

		minAsset = gasPool.RuneValueInAsset(minOutboundInRune)
	}

	outboundFeeWithheldRune, err := gm.keeper.GetOutboundFeeWithheldRune(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get outbound fee withheld rune", "outbound asset", asset, "error", err)
		outboundFeeWithheldRune = cosmos.ZeroUint()
	}
	outboundFeeSpentRune, err := gm.keeper.GetOutboundFeeSpentRune(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get outbound fee spent rune", "outbound asset", asset, "error", err)
		outboundFeeSpentRune = cosmos.ZeroUint()
	}

	targetOutboundFeeSurplus := gm.keeper.GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)
	maxMultiplierBasisPoints := gm.keeper.GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)
	minMultiplierBasisPoints := gm.keeper.GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)

	// Calculate outbound fee based on current fee multiplier
	nativeChainBaseFee := cosmos.NewUint(chainOutboundFee.TransactionSize).MulUint64(chainOutboundFee.TransactionFeeRate)
	chainBaseFee := asset.GetChain().NativeGasToThorchain(nativeChainBaseFee)
	feeMultiplierBps := gm.CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(targetOutboundFeeSurplus)), outboundFeeSpentRune, outboundFeeWithheldRune, cosmos.NewUint(uint64(maxMultiplierBasisPoints)), cosmos.NewUint(uint64(minMultiplierBasisPoints)))
	finalFee := common.GetUncappedShare(feeMultiplierBps, cosmos.NewUint(constants.MaxBasisPts), chainBaseFee)

	fee := cosmos.RoundToDecimal(
		finalFee,
		gasPool.Decimals,
	)

	// Ensure fee is always more than minAsset
	if fee.LT(minAsset) {
		fee = minAsset
	}

	// If feeAsset = gas asset && inRune = false, we are in the correct units, return
	if asset.Equals(asset.GetChain().GetGasAsset()) && !inRune {
		return fee, nil
	}

	if gasPool.BalanceAsset.IsZero() || gasPool.BalanceRune.IsZero() {
		ctx.Logger().Error("fail to calculate fee as gas pool balance is zero, returning 0 fee", "pool", gasPool.Asset.String(), "rune", gasPool.BalanceRune.String(), "asset", gasPool.BalanceAsset.String())
		return cosmos.ZeroUint(), nil
	}

	// Convert gas asset fee to rune, if inRune = true, return
	fee = gasPool.AssetValueInRune(fee)
	if inRune {
		return fee, nil
	}

	// convert rune value into non-gas asset value
	assetPool, _, err := poolView.getPool(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	if assetPool.BalanceAsset.IsZero() || assetPool.BalanceRune.IsZero() {
		ctx.Logger().Error("fail to calculate fee as asset pool balance is zero, returning 0 fee", "pool", assetPool.Asset.String(), "rune", assetPool.BalanceRune.String(), "asset", assetPool.BalanceAsset.String())
		return cosmos.ZeroUint(), nil
	}

	return assetPool.RuneValueInAsset(fee), nil
}

// CalcOutboundFeeMultiplier returns the current outbound fee multiplier based on current and target outbound fee surplus
func (gm *GasMgr) CalcOutboundFeeMultiplier(ctx cosmos.Context, targetSurplusRune, gasSpentRune, gasWithheldRune, maxMultiplier, minMultiplier cosmos.Uint) cosmos.Uint {
	// Sanity check
	if targetSurplusRune.Equal(cosmos.ZeroUint()) {
		ctx.Logger().Error("target gas surplus is zero")
		return maxMultiplier
	}
	if minMultiplier.GT(maxMultiplier) {
		ctx.Logger().Error("min multiplier greater than max multiplier", "minMultiplier", minMultiplier, "maxMultiplier", maxMultiplier)
		return cosmos.NewUint(30_000) // should never happen, return old default
	}

	// Find current surplus (gas withheld from user - gas spent by the reserve)
	surplusRune := common.SafeSub(gasWithheldRune, gasSpentRune)

	// How many BPs to reduce the multiplier
	multiplierReducedBps := common.GetSafeShare(surplusRune, targetSurplusRune, common.SafeSub(maxMultiplier, minMultiplier))
	return common.SafeSub(maxMultiplier, multiplierReducedBps)
}

// TODO: Replace combined GetMaxGas/GetGasRate calls with single GetGasDetails calls, so GetNetworkFee called only once.
// (If done completely, perhaps mark GetMaxGas/GetGasRate to be removed on hard fork.)
//
// GetGasDetails calculates a consistent MaxGas Coin and GasRate for the network's TransactionSize.
func (gm *GasMgr) GetGasDetails(ctx cosmos.Context, chain common.Chain) (common.Coin, int64, error) {
	networkFee, err := gm.GetNetworkFee(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get network fee", "error", err, "chain", chain)
		return common.NoCoin, 0, fmt.Errorf("fail to get network fee for chain(%s): %w", chain, err)
	}
	if err := networkFee.Valid(); err != nil {
		ctx.Logger().Error("network fee is invalid", "error", err, "chain", chain)
		return common.NoCoin, 0, fmt.Errorf("network fee for chain(%s) is invalid: %w", chain, err)
	}

	gasRate := cosmos.NewUint(networkFee.TransactionFeeRate)
	if !chain.IsTHORChain() {
		// THORChain has exactly-knowable gas costs, but otherwise overestimate the gas rate by 1.5x
		// to increase the likelihood of transaction acceptance.
		gasRate = gasRate.MulUint64(3).QuoUint64(2)
	}

	maxGasAmount := gasRate.MulUint64(networkFee.TransactionSize)
	maxGasCoin := chain.NativeGasToThorCoinCeil(maxGasAmount)

	return maxGasCoin, int64(gasRate.Uint64()), nil
}

// GetGasRate return the gas rate
func (gm *GasMgr) GetGasRate(ctx cosmos.Context, chain common.Chain) cosmos.Uint {
	_, gasRate, err := gm.GetGasDetails(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get gas rate", "chain", chain, "error", err)
		return cosmos.ZeroUint()
	}
	return cosmos.NewUint(uint64(gasRate))
}

func (gm *GasMgr) GetNetworkFee(ctx cosmos.Context, chain common.Chain) (types.NetworkFee, error) {
	switch chain {
	case common.THORChain:
		transactionFee := gm.keeper.GetOutboundTxFee(ctx)
		return types.NewNetworkFee(common.THORChain, 1, transactionFee.Uint64()), nil
	default:
		return gm.keeper.GetNetworkFee(ctx, chain)
	}
}

// GetMaxGas will calculate the maximum gas fee a tx can use
func (gm *GasMgr) GetMaxGas(ctx cosmos.Context, chain common.Chain) (common.Coin, error) {
	maxGasCoin, _, err := gm.GetGasDetails(ctx, chain)
	return maxGasCoin, err
}

// EndBlock emit the events
func (gm *GasMgr) EndBlock(ctx cosmos.Context, mgr Manager) {
	gm.ProcessGas(ctx, mgr)

	// Per-block native gas burn and outbound count, per gas asset. Emitted before
	// the empty-pools early return below so the loop is always reached.
	for _, p := range gm.gasEvent.Pools {
		assetLabels := []metrics.Label{telemetry.NewLabel("asset", p.Asset.String())}
		telemetry.IncrCounterWithLabels([]string{"thornode", "gas", "spent_asset"}, telem(p.AssetAmt), assetLabels)
		// GasPool.Count is set once from the already-final gm.gasCount; UpsertGasPool
		// merges never touch it, so this is the block's outbound tx count per gas asset.
		telemetry.IncrCounterWithLabels([]string{"thornode", "gas", "outbound_count"}, float32(p.Count), assetLabels)
	}

	if len(gm.gasEvent.Pools) == 0 {
		return
	}
	if err := mgr.EventMgr().EmitGasEvent(ctx, gm.gasEvent); nil != err {
		ctx.Logger().Error("fail to emit gas event", "error", err)
	}
	gm.reset() // do not remove, will cause consensus failures
}

// resolveConflictingMempoolSwapGasOwners resolves reversible pool state only
// when the block's aggregate gas cannot be debited from both the finalized and
// live balances. Below that boundary, subtracting gas commutes with removing or
// applying the overlay, so the speculative swap can remain active.
func (gm *GasMgr) resolveConflictingMempoolSwapGasOwners(ctx cosmos.Context, mgr Manager) map[common.Asset]bool {
	k := mgr.Keeper()
	totals := make(map[common.Asset]cosmos.Uint)
	for i := range gm.outAssetGas {
		for _, coin := range gm.outAssetGas[i].gas {
			if coin.IsEmpty() {
				continue
			}
			total, ok := totals[coin.Asset]
			if !ok {
				total = cosmos.ZeroUint()
			}
			totals[coin.Asset] = total.Add(coin.Amount)
		}
	}

	assets := make([]common.Asset, 0, len(totals))
	// analyze-ignore(map-iteration) - extracting keys to sort them immediately after
	for asset := range totals {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool {
		return assets[i].String() < assets[j].String()
	})

	blocked := make(map[common.Asset]bool)
	for _, asset := range assets {
		livePool, err := k.GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get live gas pool before resolving mempool swap owner", "asset", asset, "error", err)
			blocked[asset] = true
			continue
		}
		if err = livePool.Valid(); err != nil {
			ctx.Logger().Error("invalid gas pool before resolving mempool swap owner", "asset", asset, "error", err)
			blocked[asset] = true
			continue
		}

		finalizedPool, affected, adjustErr := newFinalityPoolView(k).adjust(ctx, livePool)
		if !affected {
			if adjustErr != nil {
				ctx.Logger().Error("fail to inspect mempool swap gas pool overlay", "asset", asset, "error", adjustErr)
				blocked[asset] = true
			}
			continue
		}
		if adjustErr == nil &&
			!totals[asset].GT(finalizedPool.BalanceAsset) &&
			!totals[asset].GT(livePool.BalanceAsset) {
			continue
		}

		preemptCtx, commit := ctx.CacheContext()
		if err := preemptMempoolSwapPoolOwnerCached(preemptCtx, mgr, asset); err != nil {
			ctx.Logger().Error(
				"fail to resolve mempool swap owner before gas accounting",
				"asset", asset,
				"gas amount", totals[asset],
				"error", err,
			)
			blocked[asset] = true
			continue
		}
		// Resolution is safe to commit independently of best-effort gas
		// settlement: rollback sends an unfinalized inbound through its ordinary
		// finalized path, while completion only accepts an already-finalized one.
		commit()
	}
	return blocked
}

// ProcessGas to subsidise the gas asset pools with RUNE for the gas they have spent
func (gm *GasMgr) ProcessGas(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()
	if k.RagnarokInProgress(ctx) {
		// ragnarok is in progress , stop
		return
	}

	blockedGasAssets := gm.resolveConflictingMempoolSwapGasOwners(ctx, mgr)
	reserveRune := k.GetRuneBalanceOfModule(ctx, ReserveName)
	// Gas reimbursements are irreversible end-block accounting. Use finalized
	// pool depths for valuation and mutation, then reapply active overlays
	// before writing the resulting live pool.
	poolView := newFinalityPoolView(k)
	valuationPoolCache := map[common.Asset]Pool{}
	for i := range gm.outAssetGas {
		feeSpentRune := cosmos.ZeroUint()
		for _, coin := range gm.outAssetGas[i].gas {
			// if the coin is empty, don't need to do anything
			if coin.IsEmpty() {
				continue
			}
			if blockedGasAssets[coin.Asset] {
				continue
			}

			pool, ok := valuationPoolCache[coin.Asset]
			if !ok {
				var err error // Declare error variable to prevent 'pool' shadowing
				pool, _, err = poolView.getPool(ctx, coin.Asset)
				if err != nil {
					ctx.Logger().Error("fail to get pool", "pool", coin.Asset, "error", err)
					continue
				}
				if err = pool.Valid(); err != nil {
					// Cache the invalid pool, logging only when added to the cache.
					ctx.Logger().Error("invalid pool", "pool", coin.Asset, "error", err)
				}
				valuationPoolCache[coin.Asset] = pool
			}
			if err := pool.Valid(); err != nil {
				continue
			}

			// TODO:  Use RuneReimbursementForAssetWithdrawal and, within this range, do cached pool updates before a single later SetPool?
			// Currently this uses a constant AssetValueInRune ratio without ensuring a constant depths-product,
			// as a result of which asset-associated reimbursement order does not matter.
			runeGas := pool.AssetValueInRune(coin.Amount) // Convert to Rune (gas will never be RUNE)
			if runeGas.IsZero() {
				continue
			}
			// Keep track of whether the Reserve RUNE will be enough for all reimbursements.
			// Only skip the reimbursement when the reserve has strictly less than runeGas;
			// an exact match (runeGas == reserveRune) still has enough to cover this gas, so
			// it should be reimbursed and leave the reserve at zero for subsequent coins.
			if runeGas.GT(reserveRune) {
				// since we don't have enough in the reserve to cover the gas used,
				// no further rune is added to gas pools, sorry LPs!
				// This is the only observable signal of Reserve exhaustion: the skip
				// emits no event and no log. Note every later coin in this block also
				// lands here once reserveRune is zeroed.
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "gas", "reserve_shortfall"},
					1,
					[]metrics.Label{telemetry.NewLabel("asset", coin.Asset.String())},
				)
				runeGas = cosmos.ZeroUint()
				reserveRune = cosmos.ZeroUint()
			} else {
				reserveRune = reserveRune.Sub(runeGas)
			}

			gasPool := GasPool{
				Asset:    coin.Asset,
				AssetAmt: coin.Amount,
				RuneAmt:  runeGas,
				Count:    gm.gasCount[coin.Asset],
			}
			gm.gasEvent.UpsertGasPool(gasPool)

			feeSpentRune = feeSpentRune.Add(runeGas)
		}
		// Add RUNE spent on gas by the reserve
		if err := k.AddToOutboundFeeSpentRune(ctx, gm.outAssetGas[i].outAsset, feeSpentRune); err != nil {
			ctx.Logger().Error("fail to add to outbound fee spent rune", "outbound asset", gm.outAssetGas[i].outAsset, "error", err)
		}
	}

	// Carry out the actual reimbursement and Set the pools.
	for i := range gm.gasEvent.Pools {
		pool, ok := valuationPoolCache[gm.gasEvent.Pools[i].Asset]
		if !ok {
			ctx.Logger().Error("pool asset in gas event for which no finalized pool was cached", "asset", gm.gasEvent.Pools[i].Asset)
			continue
		}
		// Conflicting owners were resolved before valuation. For an overlay that
		// remains active, the aggregate debit fits both views and therefore
		// commutes with later finalization or rollback.
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, gm.gasEvent.Pools[i].AssetAmt)

		// First validate the complete finalized mutation with the active overlay
		// reapplied. Directly subtracting from the live pool is not equivalent
		// when an active overlay already decreases this balance and can underflow.
		poolWithCredit := pool
		poolWithCredit.BalanceRune = poolWithCredit.BalanceRune.Add(gm.gasEvent.Pools[i].RuneAmt)
		livePool, _, err := poolView.apply(ctx, poolWithCredit)
		if err != nil {
			ctx.Logger().Error("fail to reapply mempool swap overlay to gas pool", "asset", pool.Asset, "error", err)
			continue
		}
		if !gm.gasEvent.Pools[i].RuneAmt.IsZero() {
			coin := common.NewCoin(common.RuneNative, gm.gasEvent.Pools[i].RuneAmt)
			if err := k.SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(coin)); err != nil {
				ctx.Logger().Error("fail to transfer funds from reserve to asgard", "pool", gm.gasEvent.Pools[i].Asset, "error", err)
				// Preserve the historical asset debit when reimbursement transfer
				// fails, but do not credit RUNE the pool did not receive.
				livePool, _, err = poolView.apply(ctx, pool)
				if err != nil {
					ctx.Logger().Error("fail to reapply mempool swap overlay to unreimbursed gas pool", "asset", pool.Asset, "error", err)
					continue
				}
			}
		}
		if err := k.SetPool(ctx, livePool); err != nil {
			ctx.Logger().Error("fail to set pool", "pool", livePool.Asset, "error", err)
		}
	}
}
