package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

type Scope int
type RuleType int
type Feature int

const (
	GlobalScope Scope = iota + 1
	ChainScope
	PoolScope
)

const (
	Trading Feature = iota + 1
	Lending
	LP
	Synths
	Savers
	POL
	TORAnchor
)

type Rule struct {
	Scope             Scope
	Features          []Feature
	isDisabledChecker func(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool
	Reason            string
}

var rules = []Rule{
	{Scope: GlobalScope, Features: []Feature{Trading, Lending, LP, Synths, Savers}, isDisabledChecker: ruleIsGlobalChainHalted, Reason: "Global chain is halted"},
	{Scope: GlobalScope, Features: []Feature{Trading, Lending, LP, Synths, Savers}, isDisabledChecker: ruleIsGlobalChainHaltedByNode, Reason: "Global chain is halted by node"},
	{Scope: GlobalScope, Features: []Feature{Lending, LP, Synths, Savers}, isDisabledChecker: ruleIsAtTLVCaps, Reason: "At TLV Caps"},
	{Scope: GlobalScope, Features: []Feature{Lending}, isDisabledChecker: ruleIsGlobalLendingPaused, Reason: "Global lending is paused"},
	{Scope: GlobalScope, Features: []Feature{Lending}, isDisabledChecker: ruleIsAtLendingCaps, Reason: "At Lending Caps"},
	{Scope: GlobalScope, Features: []Feature{LP}, isDisabledChecker: ruleIsGlobalLpPaused, Reason: "Global LP actions are paused"},
	{Scope: GlobalScope, Features: []Feature{Trading}, isDisabledChecker: ruleIsGlobalTradingHalted, Reason: "Global trading halted"},
	{Scope: GlobalScope, Features: []Feature{Synths}, isDisabledChecker: ruleIsGlobalSynthsPaused, Reason: "Global synths paused"},
	{Scope: ChainScope, Features: []Feature{Trading}, isDisabledChecker: ruleIsChainTradingHalted, Reason: "Chain trading halted"},
	{Scope: ChainScope, Features: []Feature{Trading}, isDisabledChecker: ruleIsChainTradingHaltedBySolvencyChecker, Reason: "Chain trading halted by solvency checker"},
	{Scope: PoolScope, Features: []Feature{Lending}, isDisabledChecker: ruleIsLendingDisabeldByPool, Reason: "Lending disable by pool with mimir"},
	{Scope: PoolScope, Features: []Feature{POL}, isDisabledChecker: ruleIsPOLDisabeldByPool, Reason: "POL disable by pool with mimir"},
	{Scope: PoolScope, Features: []Feature{TORAnchor}, isDisabledChecker: ruleIsTORAnchorDisabeldByPool, Reason: "TORAnchor disable by pool with mimir"},
	{Scope: PoolScope, Features: []Feature{Synths}, isDisabledChecker: ruleIsSynthsDisabeldByPool, Reason: "Synths paused by pool"},
}

func ruleIsGlobalTradingHalted(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isGlobalTradingPaused := mgr.Keeper().IsGlobalTradingHalted(ctx)
	return isGlobalTradingPaused
}

func ruleIsGlobalLendingPaused(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isGlobalLendingPaused := mgr.Keeper().IsGlobalLendingPaused(ctx)
	return isGlobalLendingPaused
}

func ruleIsGlobalLpPaused(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isGlobalLpPaused := mgr.Keeper().IsGlobalLPPaused(ctx)
	return isGlobalLpPaused
}

func ruleIsGlobalSynthsPaused(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	burnSynths, errGettingBurn := mgr.Keeper().GetMimir(ctx, "BurnSynths")                                    // TODO: 1 means enable or disable ?
	mintSynths, errGettingMint := mgr.Keeper().GetMimir(ctx, "MintSynths")                                    // TODO: 1 means enable or disable ?
	isChainSynthsPaused := errGettingBurn == nil && burnSynths > 0 && errGettingMint == nil && mintSynths > 0 // For synths, there is a BurnSynths and MintSynths mimir which controls if swapping to/from synths are enabled
	return isChainSynthsPaused
}

func ruleIsAtTLVCaps(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isAtTLVCap := atTVLCap(ctx, nil, mgr)
	return isAtTLVCap
}

func ruleIsAtLendingCaps(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isAtTLVCap := atLendingCapGlobal(ctx, nil, mgr)
	return isAtTLVCap
}

func ruleIsGlobalChainHalted(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	haltChain, err := mgr.Keeper().GetMimir(ctx, "HaltChainGlobal")
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		return true
	}
	return false
}

func ruleIsGlobalChainHaltedByNode(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	haltChain, err := mgr.Keeper().GetMimir(ctx, "NodePauseChainGlobal")
	if err == nil && haltChain > ctx.BlockHeight() {
		return true
	}
	return false
}

func ruleIsChainTradingHalted(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	mimirKey := fmt.Sprintf("Halt%sTrading", chain)
	haltChainTrading, err := mgr.Keeper().GetMimir(ctx, mimirKey)
	if err == nil && (haltChainTrading > 0 && haltChainTrading < ctx.BlockHeight()) {
		return true
	}
	return false
}

func ruleIsChainTradingHaltedBySolvencyChecker(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	solvencyHaltMimirKey := fmt.Sprintf("SolvencyHalt%sChain", chain)
	haltChain, err := mgr.Keeper().GetMimir(ctx, solvencyHaltMimirKey)
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		return true
	}
	return false
}

func getStatusFlagsFromMimirValue(ctx cosmos.Context, mgr Manager, prefix, asset string) (bool, error) {
	key := prefix + "-" + asset
	val, err := mgr.Keeper().GetMimir(ctx, key)
	if err != nil {
		return false, fmt.Errorf("Fail to fetch key %s: %w", key, err)
	}

	return val > 0, nil
}

func ruleIsLendingDisabeldByPool(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isLendingEnable, err := getStatusFlagsFromMimirValue(ctx, mgr, "LENDING", pool.Asset.GetDerivedAsset().MimirString())
	return err == nil && !isLendingEnable
}

func ruleIsPOLDisabeldByPool(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isPOLEnable, err := getStatusFlagsFromMimirValue(ctx, mgr, "POL", pool.Asset.GetDerivedAsset().MimirString())
	return err == nil && !isPOLEnable
}

func ruleIsTORAnchorDisabeldByPool(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	isTORAnchorEnable, err := getStatusFlagsFromMimirValue(ctx, mgr, "TORANCHOR", pool.Asset.GetDerivedAsset().MimirString())
	return err == nil && !isTORAnchorEnable
}

func ruleIsSynthsDisabeldByPool(mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) bool {
	synthMintPausedErr := isSynthMintPaused(ctx, mgr, pool.Asset, cosmos.ZeroUint())
	return synthMintPausedErr != nil
}

func getStatus(scope Scope, functionality Feature, mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) (bool, string) {
	isDisabled, reason := checkRules(GlobalScope, functionality, mgr, ctx, chain, pool)
	if isDisabled {
		return !isDisabled, reason
	}

	if scope >= ChainScope {
		isDisabled, reason = checkRules(ChainScope, functionality, mgr, ctx, chain, pool)
		if isDisabled {
			return !isDisabled, reason
		}
	}

	if scope == PoolScope { // todo >)
		isDisabled, reason = checkRules(PoolScope, functionality, mgr, ctx, chain, pool)
		if isDisabled {
			return !isDisabled, reason
		}
	}

	return true, ""
}

func checkRules(scope Scope, feature Feature, mgr *Mgrs, ctx cosmos.Context, chain common.Chain, pool *types.Pool) (bool, string) {
	for _, rule := range rules {
		if rule.Scope == scope && containsFeature(rule.Features, feature) {
			isDisabled := rule.isDisabledChecker(mgr, ctx, chain, pool)
			if isDisabled {
				return isDisabled, rule.Reason
			}
		}
	}
	return false, ""
}

func containsFeature(features []Feature, feature Feature) bool {
	for _, f := range features {
		if f == feature {
			return true
		}
	}
	return false
}
