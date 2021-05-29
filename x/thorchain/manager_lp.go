package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
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
	// Mimir PendingLiquidityCheckInterval to 0 will disable this
	if checkInterval == 0 {
		return nil
	}
	if ctx.BlockHeight()%checkInterval != 0 {
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
		if lps == nil {
			continue
		}
		if err := lpm.processPendingLiquidity(ctx, lps, p.Asset, mgr); err != nil {
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
		// not time to be committed
		if blockHeight-lp.LastAddHeight < age {
			continue
		}
		liquidityProviders = append(liquidityProviders, lp)
	}
	return liquidityProviders, nil
}

func (lpm *LPManagerV1) processPendingLiquidity(ctx cosmos.Context, lps []LiquidityProvider, asset common.Asset, mgr Manager) error {
	for _, lp := range lps {
		p, err := lpm.k.GetPool(ctx, asset)
		if err != nil {
			return fmt.Errorf("fail to get pool(%s),err:%w", asset, err)
		}
		if err := lpm.commitPendingLiquidity(ctx, asset, p, lp, mgr); err != nil {
			ctx.Logger().Error("fail to commit pending liquidity", "error", err)
			// roll back the changes in pool
			if savePoolErr := lpm.k.SetPool(ctx, p); savePoolErr != nil {
				return fmt.Errorf("fail to restore pool(%s),err: %w", asset, err)
			}
		}
	}
	return nil
}

func (lpm *LPManagerV1) commitPendingLiquidity(ctx cosmos.Context, asset common.Asset, pool Pool, lp LiquidityProvider, mgr Manager) error {
	pendingRuneAmt := lp.PendingRune
	pendingAssetAmt := lp.PendingAsset
	runeTxID := common.BlankTxID
	assetTxID := common.BlankTxID
	if !lp.PendingRune.IsZero() {
		runeTxID = lp.PendingTxID
	}
	if !lp.PendingAsset.IsZero() {
		assetTxID = lp.PendingTxID
	}
	pool.PendingInboundRune = common.SafeSub(pool.PendingInboundRune, lp.PendingRune)
	pool.PendingInboundAsset = common.SafeSub(pool.PendingInboundAsset, lp.PendingAsset)
	lp.PendingAsset = cosmos.ZeroUint()
	lp.PendingRune = cosmos.ZeroUint()
	lp.PendingTxID = ""

	ctx.Logger().Info(fmt.Sprintf("Pre-Pool: %sRUNE %sAsset", pool.BalanceRune, pool.BalanceAsset))
	ctx.Logger().Info(fmt.Sprintf("Adding Liquidity: %sRUNE %sAsset", pendingRuneAmt, pendingAssetAmt))

	balanceRune := pool.BalanceRune
	balanceAsset := pool.BalanceAsset

	oldPoolUnits := pool.PoolUnits
	newPoolUnits, liquidityUnits, err := calculatePoolUnitsV1(oldPoolUnits, balanceRune, balanceAsset, pendingRuneAmt, pendingAssetAmt)
	if err != nil {
		return ErrInternal(err, "fail to calculate pool unit")
	}

	ctx.Logger().Info(fmt.Sprintf("current pool units : %s ,liquidity units : %s", newPoolUnits, liquidityUnits))
	poolRune := balanceRune.Add(pendingRuneAmt)
	poolAsset := balanceAsset.Add(pendingAssetAmt)
	pool.PoolUnits = newPoolUnits
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	ctx.Logger().Info(fmt.Sprintf("Post-Pool: %sRUNE %sAsset", pool.BalanceRune, pool.BalanceAsset))
	if pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero() {
		return ErrInternal(err, "pool cannot have zero rune or asset balance")
	}

	if err := lpm.k.SetPool(ctx, pool); err != nil {
		return ErrInternal(err, "fail to save pool")
	}

	lp.Units = lp.Units.Add(liquidityUnits)
	lp.RuneDepositValue = lp.RuneDepositValue.Add(common.GetShare(liquidityUnits, pool.PoolUnits, pool.BalanceRune))
	lp.AssetDepositValue = lp.AssetDepositValue.Add(common.GetShare(liquidityUnits, pool.PoolUnits, pool.BalanceAsset))
	lpm.k.SetLiquidityProvider(ctx, lp)

	evt := NewEventAddLiquidity(asset, liquidityUnits, lp.RuneAddress, pendingRuneAmt, pendingAssetAmt, runeTxID, assetTxID, lp.AssetAddress)
	if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit add liquidity event", "error", err)
	}
	return nil
}
