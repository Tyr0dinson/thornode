package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

type LPManagerV1 struct {
	k keeper.Keeper
}

// NewLPManager create a new instance of LPManagerV1
func NewLPManager(k keeper.Keeper) *LPManagerV1 {
	return &LPManagerV1{
		k: k,
	}
}

// EndBlock when a block get committed
func (lpm *LPManagerV1) EndBlock(ctx cosmos.Context, mgr Manager, version semver.Version, constAccessor constants.ConstantValues) error {
	// LPManager only available after version 55
	if version.LT(semver.MustParse("0.55.0")) {
		return nil
	}
	checkInterval, err := lpm.k.GetMimir(ctx, constants.PendingLiquidityCheckInterval.String())
	if checkInterval < 0 || err != nil {
		checkInterval = constAccessor.GetInt64Value(constants.PendingLiquidityCheckInterval)
	}
	if checkInterval == 0 {
		return nil
	}
	maxAge := constAccessor.GetInt64Value(constants.MaxPendingLiquidityAge)
	pools, err := lpm.k.GetPools(ctx)
	if err != nil {
		return fmt.Errorf("fail to get pools,err: %w", err)
	}
	for _, p := range pools {
		if p.Status != PoolAvailable {
			continue
		}
		lps, err := lpm.getPendingLiquidity(ctx, p, maxAge)
		if err != nil {
			ctx.Logger().Error("fail to get liquidity providers that has pending liquidity", "error", err)
			continue
		}
		if err := lpm.processPendingLiquidity(ctx, lps, p, mgr, version, constAccessor); err != nil {
			ctx.Logger().Error("fail to process pending liquidity", "error", err)
			continue
		}
	}

	return nil
}

func (lpm *LPManagerV1) getPendingLiquidity(ctx cosmos.Context, p Pool, age int64) ([]LiquidityProvider, error) {
	if p.IsEmpty() {
		return nil, nil
	}
	iter := lpm.k.GetLiquidityProviderIterator(ctx, p.Asset)
	defer func() {
		if err := iter.Close(); err != nil {
			ctx.Logger().Error("fail to close liquidity provider iterator", "error", err)
		}
	}()
	blockHeight := ctx.BlockHeight()
	var liquidityProviders []LiquidityProvider
	for ; iter.Valid(); iter.Next() {
		var lp LiquidityProvider
		if err := lpm.k.Cdc().UnmarshalBinaryBare(iter.Value(), &lp); err != nil {
			ctx.Logger().Error("fail to unmarshal liquidity provider", "error", err)
			continue
		}
		if lp.PendingAsset.IsZero() && lp.PendingRune.IsZero() {
			continue
		}
		if blockHeight-lp.LastAddHeight < age {
			continue
		}
		liquidityProviders = append(liquidityProviders, lp)
	}
	return liquidityProviders, nil
}

func (lpm *LPManagerV1) processPendingLiquidity(ctx cosmos.Context, lps []LiquidityProvider, p Pool, mgr Manager, version semver.Version, constAccessor constants.ConstantValues) error {
	handler := NewAddLiquidityHandler(lpm.k, mgr)
	addLiquidity := handler.addLiquidityV46
	for _, lp := range lps {
		if err := addLiquidity(ctx,
			p.Asset, lp.PendingRune,
			lp.PendingAsset,
			lp.RuneAddress,
			lp.AssetAddress, lp.PendingTxID,
			false, constAccessor); err != nil {
			ctx.Logger().Error("fail to add pending liquidity to pool", "error", err)
		}
	}
	return nil
}
