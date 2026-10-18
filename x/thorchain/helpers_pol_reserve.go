package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// polReserveMaxDrainRunePerBlock caps DrainRunePerBlock at 100 RUNE.
const polReserveMaxDrainRunePerBlock = 100 * common.One

// Upper-case key prefixes of the per-pool POL Reserve mimirs, e.g.
// SYSTEMINCOMEPOLPOOL-SOL-SOL.
var (
	systemIncomePOLPoolMimirPrefix = strings.ToUpper(strings.TrimSuffix(constants.MimirTemplateSystemIncomePOLPool, "%s"))
	drainPOLPoolMimirPrefix        = strings.ToUpper(strings.TrimSuffix(constants.MimirTemplateDrainPOLPool, "%s"))
)

// polReserveDeployable reports whether the POL Reserve may deposit into pool.
// The global trading halt is left to the callers that deposit.
func polReserveDeployable(ctx cosmos.Context, mgr Manager, pool Pool) bool {
	k := mgr.Keeper()
	return pool.Asset.IsExternalL1Asset() &&
		pool.Status == PoolAvailable &&
		!pool.BalanceRune.IsZero() &&
		!k.IsChainTradingHalted(ctx, pool.Asset.GetChain()) &&
		!k.IsRagnarok(ctx, []common.Asset{pool.Asset}) &&
		!k.IsLPPaused(ctx, pool.Asset.GetChain()) &&
		!k.IsPoolDepositPaused(ctx, pool.Asset) &&
		k.IsPOLReserveEligiblePool(ctx, pool.Asset)
}

// polReserveDrainable reports whether the POL Reserve position lp may be
// withdrawn from pool this block.
func polReserveDrainable(ctx cosmos.Context, mgr Manager, pool Pool, lp LiquidityProvider) bool {
	k := mgr.Keeper()
	lockup := k.GetConfigInt64(ctx, constants.LiquidityLockUpBlocks)
	return pool.Status == PoolAvailable &&
		!k.IsGlobalTradingHalted(ctx) &&
		!k.IsChainTradingHalted(ctx, pool.Asset.GetChain()) &&
		!k.IsLPPaused(ctx, pool.Asset.GetChain()) &&
		!k.IsRagnarok(ctx, []common.Asset{pool.Asset}) &&
		ctx.BlockHeight() >= lp.LastAddHeight+lockup
}

// polPoolFromMimirSuffix maps the asset part of a SystemIncomePOLPool- or
// DrainPOLPool- key (an Asset MimirString such as ETH-USDT-0X...) to its pool.
func polPoolFromMimirSuffix(ctx cosmos.Context, mgr Manager, suffix string) (Pool, bool) {
	chain, symbol, found := strings.Cut(suffix, "-")
	if !found || chain == "" || symbol == "" {
		return Pool{}, false
	}
	asset, err := common.NewAsset(chain + "." + symbol)
	if err != nil {
		return Pool{}, false
	}
	pool, err := mgr.Keeper().GetPool(ctx, asset)
	if err != nil || pool.IsEmpty() || !strings.EqualFold(pool.Asset.MimirString(), suffix) {
		return Pool{}, false
	}
	return pool, true
}

// setPOLPoolMimirKeys returns the upper-case keys under prefix that are set
// (value > 0), in store order.
func setPOLPoolMimirKeys(ctx cosmos.Context, mgr Manager, prefix string) ([]string, error) {
	mimirs, err := mgr.Keeper().ListMimirsWithPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, mimir := range mimirs {
		if mimir.Value > 0 {
			keys = append(keys, strings.ToUpper(mimir.Key))
		}
	}
	return keys, nil
}

// resolvePOLPoolMimir returns the one pool selected by the <prefix><asset>
// mimirs. None, several, or an unknown pool resolve to no pool.
func resolvePOLPoolMimir(ctx cosmos.Context, mgr Manager, prefix string) (Pool, bool) {
	keys, err := setPOLPoolMimirKeys(ctx, mgr, prefix)
	if err != nil {
		ctx.Logger().Error("fail to list pool mimirs", "prefix", prefix, "error", err)
		return Pool{}, false
	}
	if len(keys) != 1 {
		if len(keys) > 1 {
			ctx.Logger().Error("more than one pool mimir set, ignoring them", "prefix", prefix, "keys", strings.Join(keys, ","))
		}
		return Pool{}, false
	}
	return polPoolFromMimirSuffix(ctx, mgr, strings.TrimPrefix(keys[0], prefix))
}

// activePOLDrain returns the pool and per-block RUNE rate of the POL Reserve
// drain when DrainPOLOn, DrainRunePerBlock and a single DrainPOLPool are set.
func activePOLDrain(ctx cosmos.Context, mgr Manager) (Pool, cosmos.Uint, bool) {
	k := mgr.Keeper()
	if k.GetConfigInt64(ctx, constants.DrainPOLOn) != 1 {
		return Pool{}, cosmos.ZeroUint(), false
	}
	rate := k.GetConfigInt64(ctx, constants.DrainRunePerBlock)
	if rate <= 0 {
		return Pool{}, cosmos.ZeroUint(), false
	}
	if rate > polReserveMaxDrainRunePerBlock {
		rate = polReserveMaxDrainRunePerBlock
	}
	pool, ok := resolvePOLPoolMimir(ctx, mgr, drainPOLPoolMimirPrefix)
	if !ok {
		return Pool{}, cosmos.ZeroUint(), false
	}
	return pool, cosmos.NewUint(uint64(rate)), true
}

// validatePOLReserveMimirVote rejects node votes that would set a POL Reserve
// mimir before its preconditions hold. A negative value withdraws the node's
// vote and is always allowed.
func validatePOLReserveMimirVote(ctx cosmos.Context, mgr Manager, upperKey string, value int64) error {
	if value < 0 {
		return nil
	}
	k := mgr.Keeper()
	switch {
	case strings.EqualFold(upperKey, constants.POLSelectAlgo.String()):
		if value != 1 && value != 2 {
			return cosmos.ErrUnknownRequest("POLSelectAlgo must be 1 or 2")
		}
		if value == 2 {
			pool, ok := resolvePOLPoolMimir(ctx, mgr, systemIncomePOLPoolMimirPrefix)
			if !ok || !polReserveDeployable(ctx, mgr, pool) {
				return cosmos.ErrUnknownRequest("POLSelectAlgo 2 requires SystemIncomePOLPool to be set to one eligible pool")
			}
		}
	case strings.EqualFold(upperKey, constants.POLCycle.String()):
		if value < 1 {
			return cosmos.ErrUnknownRequest("POLCycle must be at least 1")
		}
	case strings.EqualFold(upperKey, constants.DrainRunePerBlock.String()):
		if value > polReserveMaxDrainRunePerBlock {
			return cosmos.ErrUnknownRequest("DrainRunePerBlock cannot exceed 100 RUNE (10000000000)")
		}
	case strings.EqualFold(upperKey, constants.DrainPOLOn.String()):
		if value != 0 && value != 1 {
			return cosmos.ErrUnknownRequest("DrainPOLOn must be 0 or 1")
		}
		if value == 1 {
			if _, ok := resolvePOLPoolMimir(ctx, mgr, drainPOLPoolMimirPrefix); !ok {
				return cosmos.ErrUnknownRequest("DrainPOLOn requires DrainPOLPool to be set to one pool")
			}
			if k.GetConfigInt64(ctx, constants.DrainRunePerBlock) <= 0 {
				return cosmos.ErrUnknownRequest("DrainPOLOn requires DrainRunePerBlock to be set")
			}
		}
	case strings.HasPrefix(upperKey, systemIncomePOLPoolMimirPrefix):
		pool, err := validatePOLPoolMimirVote(ctx, mgr, upperKey, value, systemIncomePOLPoolMimirPrefix, "SystemIncomePOLPool")
		if err != nil || value == 0 {
			return err
		}
		if !polReserveDeployable(ctx, mgr, pool) {
			return cosmos.ErrUnknownRequest("SystemIncomePOLPool must be a pool eligible for POL Reserve deployment")
		}
	case strings.HasPrefix(upperKey, drainPOLPoolMimirPrefix):
		pool, err := validatePOLPoolMimirVote(ctx, mgr, upperKey, value, drainPOLPoolMimirPrefix, "DrainPOLPool")
		if err != nil || value == 0 {
			return err
		}
		polAddress, err := k.GetModuleAddress(POLReserveName)
		if err != nil {
			return ErrInternal(err, "fail to get pol_reserve address")
		}
		lp, err := k.GetLiquidityProvider(ctx, pool.Asset, polAddress)
		if err != nil || lp.Units.IsZero() {
			return cosmos.ErrUnknownRequest("DrainPOLPool must be a pool holding POL Reserve liquidity")
		}
	}
	return nil
}

// validatePOLPoolMimirVote checks the shared rules of the per-pool mimirs: the
// value is 0 or 1, and setting a pool needs an existing pool and no other pool
// already set under the same prefix.
func validatePOLPoolMimirVote(ctx cosmos.Context, mgr Manager, upperKey string, value int64, prefix, name string) (Pool, error) {
	if value != 0 && value != 1 {
		return Pool{}, cosmos.ErrUnknownRequest(name + " must be 0 or 1")
	}
	if value == 0 {
		return Pool{}, nil
	}
	pool, ok := polPoolFromMimirSuffix(ctx, mgr, strings.TrimPrefix(upperKey, prefix))
	if !ok {
		return Pool{}, cosmos.ErrUnknownRequest(name + " must name an existing pool")
	}
	keys, err := setPOLPoolMimirKeys(ctx, mgr, prefix)
	if err != nil {
		return Pool{}, ErrInternal(err, "fail to list "+name+" mimirs")
	}
	for _, key := range keys {
		if key != upperKey {
			return Pool{}, cosmos.ErrUnknownRequest(name + " is already set for another pool; unset it first")
		}
	}
	return pool, nil
}
