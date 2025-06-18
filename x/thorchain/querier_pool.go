// querier_pool.go contains all pool and liquidity-related query handlers.
// This includes queries for pools, liquidity providers, savers, and borrowers.

package thorchain

import (
	"errors"
	"fmt"
	"strings"

	sdkmath "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Pool Queries ============

func (qs queryServer) queryPool(ctx cosmos.Context, req *types.QueryPoolRequest) (*types.QueryPoolResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("asset: %s is a derived asset", req.Asset)
	}

	pool, err := qs.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("could not get pool: %w", err)
	}
	if pool.IsEmpty() {
		return nil, fmt.Errorf("pool: %s doesn't exist", req.Asset)
	}

	// Get Savers Vault for this L1 pool if it's a gas asset
	saversAsset := pool.Asset.GetSyntheticAsset()
	saversPool, err := qs.mgr.Keeper().GetPool(ctx, saversAsset)
	if err != nil {
		return nil, fmt.Errorf("fail to unmarshal savers vault: %w", err)
	}

	saversDepth := saversPool.BalanceAsset
	saversUnits := saversPool.LPUnits
	synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)

	synthMintPausedErr := isSynthMintPaused(ctx, qs.mgr, saversAsset, cosmos.ZeroUint())
	synthSupplyRemaining, _ := getSynthSupplyRemaining(ctx, qs.mgr, saversAsset)

	maxSynthsForSaversYield := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxSynthsForSaversYield)
	// Capping the synths at double the pool balance of Assets.
	maxSynthsForSaversYieldUint := common.GetUncappedShare(cosmos.NewUint(uint64(maxSynthsForSaversYield)), cosmos.NewUint(constants.MaxBasisPts), pool.BalanceAsset.MulUint64(2))

	saversFillBps := common.GetUncappedShare(synthSupply, maxSynthsForSaversYieldUint, cosmos.NewUint(constants.MaxBasisPts))
	saversCapacityRemaining := common.SafeSub(maxSynthsForSaversYieldUint, synthSupply)

	totalCollateral, err := qs.mgr.Keeper().GetTotalCollateral(ctx, pool.Asset)
	if err != nil {
		return nil, fmt.Errorf("fail to fetch total loan collateral: %w", err)
	}

	loanHandler := NewLoanOpenHandler(qs.mgr)
	// getPoolCR and GetLoanCollateralRemainingForPool
	// are expected to error for block heights earlier than 12241034
	// from negative MaxRuneSupply, so dropping the errors for both
	// and instead displaying both as zero when unretrievable.
	cr, _ := loanHandler.getPoolCR(ctx, pool, cosmos.OneUint())
	loanCollateralRemaining, _ := loanHandler.GetLoanCollateralRemainingForPool(ctx, pool)

	runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, asset)
	dpool, _ := qs.mgr.Keeper().GetPool(ctx, asset.GetDerivedAsset())
	dbps := common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
	if dpool.Status != PoolAvailable {
		dbps = cosmos.ZeroUint()
	}

	tradingHalted := qs.mgr.Keeper().IsGlobalTradingHalted(ctx)

	l1Asset := pool.Asset.GetLayer1Asset()
	chain := l1Asset.GetChain()

	if !pool.IsAvailable() {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsChainTradingHalted(ctx, chain) {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsChainHalted(ctx, chain) {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsRagnarok(ctx, []common.Asset{l1Asset}) {
		tradingHalted = true
	}

	p := types.QueryPoolResponse{
		Asset:               pool.Asset.String(),
		ShortCode:           pool.Asset.ShortCode(),
		Status:              pool.Status.String(),
		Decimals:            pool.Decimals,
		PendingInboundAsset: pool.PendingInboundAsset.String(),
		PendingInboundRune:  pool.PendingInboundRune.String(),
		BalanceAsset:        pool.BalanceAsset.String(),
		BalanceRune:         pool.BalanceRune.String(),
		PoolUnits:           pool.GetPoolUnits().String(),
		LPUnits:             pool.LPUnits.String(),
		SynthUnits:          pool.SynthUnits.String(),
		TradingHalted:       tradingHalted,
	}
	p.SynthSupply = synthSupply.String()
	p.SaversDepth = saversDepth.String()
	p.SaversUnits = saversUnits.String()
	p.SaversFillBps = saversFillBps.String()
	p.SaversCapacityRemaining = saversCapacityRemaining.String()
	p.SynthMintPaused = (synthMintPausedErr != nil)
	p.SynthSupplyRemaining = synthSupplyRemaining.String()
	p.LoanCollateral = totalCollateral.String()
	p.LoanCollateralRemaining = loanCollateralRemaining.String()
	p.DerivedDepthBps = dbps.String()
	p.LoanCr = cr.String()

	if !pool.BalanceAsset.IsZero() && !pool.BalanceRune.IsZero() {
		dollarsPerRune := dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper())
		p.AssetTorPrice = dollarsPerRune.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).String()
	}

	return &p, nil
}

func (qs queryServer) queryPools(ctx cosmos.Context, _ *types.QueryPoolsRequest) (*types.QueryPoolsResponse, error) {
	dollarsPerRune := dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper())

	isGlobalTradingHalted := qs.mgr.Keeper().IsGlobalTradingHalted(ctx)
	isChainOrChainTradingHalted := map[common.Chain]bool{}

	pools := make([]*types.QueryPoolResponse, 0)
	iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		// ignore pool if no liquidity provider units
		if pool.LPUnits.IsZero() {
			continue
		}

		// Ignore synth asset pool (savers). Info will be on the L1 pool
		if pool.Asset.IsSyntheticAsset() {
			continue
		}

		// Ignore derived assets (except TOR)
		if pool.Asset.IsDerivedAsset() {
			continue
		}

		// Get Savers Vault
		saversAsset := pool.Asset.GetSyntheticAsset()
		saversPool, err := qs.mgr.Keeper().GetPool(ctx, saversAsset)
		if err != nil {
			return nil, fmt.Errorf("fail to unmarshal savers vault: %w", err)
		}

		saversDepth := saversPool.BalanceAsset
		saversUnits := saversPool.LPUnits

		synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		pool.CalcUnits(synthSupply)

		synthMintPausedErr := isSynthMintPaused(ctx, qs.mgr, pool.Asset, cosmos.ZeroUint())
		synthSupplyRemaining, _ := getSynthSupplyRemaining(ctx, qs.mgr, pool.Asset)

		maxSynthsForSaversYield := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxSynthsForSaversYield)
		// Capping the synths at double the pool balance of Assets.
		maxSynthsForSaversYieldUint := common.GetUncappedShare(cosmos.NewUint(uint64(maxSynthsForSaversYield)), cosmos.NewUint(constants.MaxBasisPts), pool.BalanceAsset.MulUint64(2))

		saversFillBps := common.GetUncappedShare(synthSupply, maxSynthsForSaversYieldUint, cosmos.NewUint(constants.MaxBasisPts))
		saversCapacityRemaining := common.SafeSub(maxSynthsForSaversYieldUint, synthSupply)

		totalCollateral, err := qs.mgr.Keeper().GetTotalCollateral(ctx, pool.Asset)
		if err != nil {
			return nil, fmt.Errorf("fail to fetch total loan collateral: %w", err)
		}

		loanHandler := NewLoanOpenHandler(qs.mgr)
		// getPoolCR and GetLoanCollateralRemainingForPool
		// are expected to error for block heights earlier than 12241034
		// from negative MaxRuneSupply, so dropping the errors for both
		// and instead displaying both as zero when unretrievable.
		cr, _ := loanHandler.getPoolCR(ctx, pool, cosmos.OneUint())
		loanCollateralRemaining, _ := loanHandler.GetLoanCollateralRemainingForPool(ctx, pool)

		runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, pool.Asset)
		dpool, _ := qs.mgr.Keeper().GetPool(ctx, pool.Asset.GetDerivedAsset())
		dbps := common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
		if dpool.Status != PoolAvailable {
			dbps = cosmos.ZeroUint()
		}

		tradingHalted := isGlobalTradingHalted

		l1Asset := pool.Asset.GetLayer1Asset()
		chain := l1Asset.GetChain()

		_, found := isChainOrChainTradingHalted[chain]
		if !found {
			isChainHalted := qs.mgr.Keeper().IsChainHalted(ctx, chain)
			isChainTradingHalted := qs.mgr.Keeper().IsChainTradingHalted(ctx, chain)

			isChainOrChainTradingHalted[chain] = isChainHalted || isChainTradingHalted
		}

		if !tradingHalted {
			tradingHalted = isChainOrChainTradingHalted[chain]
		}

		if !pool.IsAvailable() {
			tradingHalted = true
		}

		if qs.mgr.Keeper().IsRagnarok(ctx, []common.Asset{l1Asset}) {
			tradingHalted = true
		}

		p := types.QueryPoolResponse{
			Asset:               pool.Asset.String(),
			ShortCode:           pool.Asset.ShortCode(),
			Status:              pool.Status.String(),
			Decimals:            pool.Decimals,
			PendingInboundAsset: pool.PendingInboundAsset.String(),
			PendingInboundRune:  pool.PendingInboundRune.String(),
			BalanceAsset:        pool.BalanceAsset.String(),
			BalanceRune:         pool.BalanceRune.String(),
			PoolUnits:           pool.GetPoolUnits().String(),
			LPUnits:             pool.LPUnits.String(),
			SynthUnits:          pool.SynthUnits.String(),
			TradingHalted:       tradingHalted,
		}

		p.SynthSupply = synthSupply.String()
		p.SaversDepth = saversDepth.String()
		p.SaversUnits = saversUnits.String()
		p.SaversFillBps = saversFillBps.String()
		p.SaversCapacityRemaining = saversCapacityRemaining.String()
		p.SynthMintPaused = (synthMintPausedErr != nil)
		p.SynthSupplyRemaining = synthSupplyRemaining.String()
		p.LoanCollateral = totalCollateral.String()
		p.LoanCollateralRemaining = loanCollateralRemaining.String()
		p.DerivedDepthBps = dbps.String()
		p.LoanCr = cr.String()

		if !pool.BalanceAsset.IsZero() && !pool.BalanceRune.IsZero() {
			p.AssetTorPrice = dollarsPerRune.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).String()
		}

		pools = append(pools, &p)
	}
	return &types.QueryPoolsResponse{Pools: pools}, nil
}

func (qs queryServer) queryDerivedPool(ctx cosmos.Context, req *types.QueryDerivedPoolRequest) (*types.QueryDerivedPoolResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if !asset.IsDerivedAsset() {
		return nil, fmt.Errorf("asset is not a derived asset: %s", asset)
	}

	// call begin block so the derived depth matches the next block execution state
	_ = qs.mgr.NetworkMgr().BeginBlock(ctx.WithBlockHeight(ctx.BlockHeight()+1), qs.mgr)

	// sum rune depth of anchor pools
	runeDepth := sdkmath.ZeroUint()
	for _, anchor := range qs.mgr.Keeper().GetAnchors(ctx, asset) {
		aPool, _ := qs.mgr.Keeper().GetPool(ctx, anchor)
		runeDepth = runeDepth.Add(aPool.BalanceRune)
	}

	dpool, _ := qs.mgr.Keeper().GetPool(ctx, asset.GetDerivedAsset())
	dbps := cosmos.ZeroUint()
	if dpool.Status == PoolAvailable {
		dbps = common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
	}

	p := types.QueryDerivedPoolResponse{
		Asset:        dpool.Asset.String(),
		Status:       dpool.Status.String(),
		Decimals:     dpool.Decimals,
		BalanceAsset: dpool.BalanceAsset.String(),
		BalanceRune:  dpool.BalanceRune.String(),
	}
	p.DerivedDepthBps = dbps.String()

	return &p, nil
}

func (qs queryServer) queryDerivedPools(ctx cosmos.Context, _ *types.QueryDerivedPoolsRequest) (*types.QueryDerivedPoolsResponse, error) {
	pools := make([]*types.QueryDerivedPoolResponse, 0)
	iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		// Ignore derived assets (except TOR)
		if !pool.Asset.IsDerivedAsset() {
			continue
		}

		runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, pool.Asset)
		dpool, _ := qs.mgr.Keeper().GetPool(ctx, pool.Asset.GetDerivedAsset())
		dbps := cosmos.ZeroUint()
		if dpool.Status == PoolAvailable {
			dbps = common.GetUncappedShare(
				dpool.BalanceRune,
				runeDepth,
				cosmos.NewUint(constants.MaxBasisPts),
			)
		}

		p := types.QueryDerivedPoolResponse{
			Asset:        dpool.Asset.String(),
			Status:       dpool.Status.String(),
			Decimals:     dpool.Decimals,
			BalanceAsset: dpool.BalanceAsset.String(),
			BalanceRune:  dpool.BalanceRune.String(),
		}
		p.DerivedDepthBps = dbps.String()

		pools = append(pools, &p)
	}
	return &types.QueryDerivedPoolsResponse{Pools: pools}, nil
}

func (qs queryServer) queryPoolSlips(ctx cosmos.Context, asset string) (*types.QueryPoolSlipsResponse, error) {
	var assets []common.Asset
	if len(asset) > 0 {
		assetObj, err := common.NewAsset(asset)
		if err != nil {
			ctx.Logger().Error("fail to parse asset", "error", err, "asset", asset)
			return nil, fmt.Errorf("fail to parse asset (%s): %w", asset, err)
		}
		assets = []common.Asset{assetObj}
	} else {
		iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
		for ; iterator.Valid(); iterator.Next() {
			var pool Pool
			if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
				return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
			}

			// Display the swap slips of Available-pool Layer 1 assets.
			if pool.Status != PoolAvailable || pool.Asset.IsNative() {
				continue
			}
			assets = append(assets, pool.Asset)
		}
	}

	result := make([]*types.QueryPoolSlipResponse, len(assets))
	for i := range assets {
		result[i] = &types.QueryPoolSlipResponse{}
		result[i].Asset = assets[i].String()

		poolSlip, err := qs.mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get swap slip for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].PoolSlip = poolSlip.Int64()

		rollupCount, err := qs.mgr.Keeper().GetRollupCount(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get rollup count for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].RollupCount = rollupCount

		longRollup, err := qs.mgr.Keeper().GetLongRollup(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get long rollup for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].LongRollup = longRollup

		rollup, err := qs.mgr.Keeper().GetCurrentRollup(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get rollup count for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].Rollup = rollup
	}

	// For performance, only sum the rollup swap slip for comparison
	// when a single asset has been specified.
	if len(assets) == 1 {
		maxAnchorBlocks := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxAnchorBlocks)
		var summedRollup int64
		for i := ctx.BlockHeight() - maxAnchorBlocks; i < ctx.BlockHeight(); i++ {
			poolSlip, err := qs.mgr.Keeper().GetPoolSwapSlip(ctx, i, assets[0])
			if err != nil {
				// Log the error, zero the sum, and exit the loop.
				ctx.Logger().Error("fail to get swap slip", "error", err, "asset", assets[0], "height", i)
				summedRollup = 0
				break
			}
			summedRollup += poolSlip.Int64()
		}
		result[0].SummedRollup = summedRollup
	}

	return &types.QueryPoolSlipsResponse{PoolSlips: result}, nil
}

// ============ Liquidity Provider Queries ============

// queryLiquidityProvider
func (qs queryServer) queryLiquidityProvider(ctx cosmos.Context, req *types.QueryLiquidityProviderRequest) (*types.QueryLiquidityProviderResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	if len(req.Address) == 0 {
		return nil, errors.New("lp not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}

	if asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is a SaversPool")
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	lp, err := qs.mgr.Keeper().GetLiquidityProvider(ctx, asset, addr)
	if err != nil {
		ctx.Logger().Error("fail to get liquidity provider", "error", err)
		return nil, fmt.Errorf("fail to liquidity provider: %w", err)
	}

	poolAsset := asset

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, poolAsset.GetSyntheticAsset())
	_, runeRedeemValue := lp.GetRuneRedeemValue(pool, synthSupply)
	_, assetRedeemValue := lp.GetAssetRedeemValue(pool, synthSupply)
	_, luviDepositValue := lp.GetLuviDepositValue(pool)
	_, luviRedeemValue := lp.GetLuviRedeemValue(runeRedeemValue, assetRedeemValue)

	lgp := cosmos.NewDec(0)
	if !luviDepositValue.IsZero() {
		ldv := cosmos.NewDec(luviDepositValue.BigInt().Int64())
		lrv := cosmos.NewDec(luviRedeemValue.BigInt().Int64())
		lgp = lrv.Sub(ldv)
		lgp = lgp.Quo(ldv)
	}

	liqp := types.QueryLiquidityProviderResponse{
		Asset:              lp.Asset.GetLayer1Asset().String(),
		RuneAddress:        lp.RuneAddress.String(),
		AssetAddress:       lp.AssetAddress.String(),
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		Units:              lp.Units.String(),
		PendingRune:        lp.PendingRune.String(),
		PendingAsset:       lp.PendingAsset.String(),
		PendingTxId:        lp.PendingTxID.String(),
		RuneDepositValue:   lp.RuneDepositValue.String(),
		AssetDepositValue:  lp.AssetDepositValue.String(),
		RuneRedeemValue:    runeRedeemValue.String(),
		AssetRedeemValue:   assetRedeemValue.String(),
		LuviDepositValue:   luviDepositValue.String(),
		LuviRedeemValue:    luviRedeemValue.String(),
		LuviGrowthPct:      lgp.String(),
	}

	return &liqp, nil
}

// queryLiquidityProviders
func (qs queryServer) queryLiquidityProviders(ctx cosmos.Context, req *types.QueryLiquidityProvidersRequest) (*types.QueryLiquidityProvidersResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}
	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}
	if asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is a SaversPool")
	}

	var lps []*types.QueryLiquidityProviderResponse
	iterator := qs.mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &lp)
		lps = append(lps, &types.QueryLiquidityProviderResponse{
			// No redeem or LUVI calculations for the array response.
			Asset:              lp.Asset.GetLayer1Asset().String(),
			RuneAddress:        lp.RuneAddress.String(),
			AssetAddress:       lp.AssetAddress.String(),
			LastAddHeight:      lp.LastAddHeight,
			LastWithdrawHeight: lp.LastWithdrawHeight,
			Units:              lp.Units.String(),
			PendingRune:        lp.PendingRune.String(),
			PendingAsset:       lp.PendingAsset.String(),
			PendingTxId:        lp.PendingTxID.String(),
			RuneDepositValue:   lp.RuneDepositValue.String(),
			AssetDepositValue:  lp.AssetDepositValue.String(),
		})
	}
	return &types.QueryLiquidityProvidersResponse{LiquidityProviders: lps}, nil
}

// ============ Saver Queries ============

// querySaver
// isSavers is true if request is for the savers of a Savers Pool, if false the request is for an L1 pool
func (qs queryServer) querySaver(ctx cosmos.Context, req *types.QuerySaverRequest) (*types.QuerySaverResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	if len(req.Address) == 0 {
		return nil, errors.New("lp not provided")
	}
	req.Asset = strings.Replace(req.Asset, ".", "/", 1)
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}

	if !asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is not a SaversPool")
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	lp, err := qs.mgr.Keeper().GetLiquidityProvider(ctx, asset, addr)
	if err != nil {
		ctx.Logger().Error("fail to get liquidity provider", "error", err)
		return nil, fmt.Errorf("fail to liquidity provider: %w", err)
	}

	poolAsset := asset.GetSyntheticAsset()

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	saver := newSaver(lp, pool)

	return saver, nil
}

// querySavers
func (qs queryServer) querySavers(ctx cosmos.Context, req *types.QuerySaversRequest) (*types.QuerySaversResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	req.Asset = strings.Replace(req.Asset, ".", "/", 1)
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}
	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}
	if !asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is not a SaversPool")
	}

	poolAsset := asset.GetSyntheticAsset()

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	var savers []*types.QuerySaverResponse
	iterator := qs.mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &lp)
		savers = append(savers, newSaver(lp, pool))
	}

	return &types.QuerySaversResponse{Savers: savers}, nil
}

// ============ Borrower Queries ============

// queryBorrower
func (qs queryServer) queryBorrower(ctx cosmos.Context, req *types.QueryBorrowerRequest) (*types.QueryBorrowerResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	if len(req.Address) == 0 {
		return nil, errors.New("loan not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}

	loan, err := qs.mgr.Keeper().GetLoan(ctx, asset, addr)
	if err != nil {
		ctx.Logger().Error("fail to get borrower", "error", err)
		return nil, fmt.Errorf("fail to borrower: %w", err)
	}

	borrower := types.QueryBorrowerResponse{
		Owner:               loan.Owner.String(),
		Asset:               loan.Asset.String(),
		DebtIssued:          loan.DebtIssued.String(),
		DebtRepaid:          loan.DebtRepaid.String(),
		DebtCurrent:         loan.Debt().String(),
		CollateralDeposited: loan.CollateralDeposited.String(),
		CollateralWithdrawn: loan.CollateralWithdrawn.String(),
		CollateralCurrent:   loan.Collateral().String(),
		LastOpenHeight:      loan.LastOpenHeight,
		LastRepayHeight:     loan.LastRepayHeight,
	}

	return &borrower, nil
}

func (qs queryServer) queryBorrowers(ctx cosmos.Context, req *types.QueryBorrowersRequest) (*types.QueryBorrowersResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	var loans Loans
	iterator := qs.mgr.Keeper().GetLoanIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var loan Loan
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &loan)
		if loan.CollateralDeposited.Equal(loan.CollateralWithdrawn) && loan.DebtIssued.Equal(loan.DebtRepaid) {
			continue
		}
		loans = append(loans, loan)
	}

	borrowers := make([]*types.QueryBorrowerResponse, len(loans))
	for i, loan := range loans {
		borrowers[i] = &types.QueryBorrowerResponse{
			Owner:               loan.Owner.String(),
			Asset:               loan.Asset.String(),
			DebtIssued:          loan.DebtIssued.String(),
			DebtRepaid:          loan.DebtRepaid.String(),
			DebtCurrent:         loan.Debt().String(),
			CollateralDeposited: loan.CollateralDeposited.String(),
			CollateralWithdrawn: loan.CollateralWithdrawn.String(),
			CollateralCurrent:   loan.Collateral().String(),
			LastOpenHeight:      loan.LastOpenHeight,
			LastRepayHeight:     loan.LastRepayHeight,
		}
	}

	return &types.QueryBorrowersResponse{Borrowers: borrowers}, nil
}