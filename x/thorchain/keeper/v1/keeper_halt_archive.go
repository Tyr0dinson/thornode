package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) IsGlobalTradingHaltedV1(ctx cosmos.Context) bool {
	haltTrading, err := k.GetMimir(ctx, "HaltTrading")
	if err == nil && ((haltTrading > 0 && haltTrading < ctx.BlockHeight()) || k.RagnarokInProgress(ctx)) {
		return true
	}
	return false
}

func (k KVStore) IsChainTradingHaltedV1(ctx cosmos.Context, chain common.Chain) bool {
	mimirKey := fmt.Sprintf("Halt%sTrading", chain)
	haltChainTrading, err := k.GetMimir(ctx, mimirKey)
	if err == nil && (haltChainTrading > 0 && haltChainTrading < ctx.BlockHeight()) {
		ctx.Logger().Debug("trading is halt", "chain", chain)
		return true
	}
	// further to check whether the chain is halted
	return k.IsChainHalted(ctx, chain)
}

func (k KVStore) IsChainHaltedV1(ctx cosmos.Context, chain common.Chain) bool {
	haltChain, err := k.GetMimir(ctx, "HaltChainGlobal")
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		ctx.Logger().Debug("global is halt")
		return true
	}

	haltChain, err = k.GetMimir(ctx, "NodePauseChainGlobal")
	if err == nil && haltChain > ctx.BlockHeight() {
		ctx.Logger().Debug("node global is halt")
		return true
	}

	haltMimirKey := fmt.Sprintf("Halt%sChain", chain)
	haltChain, err = k.GetMimir(ctx, haltMimirKey)
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		ctx.Logger().Debug("chain is halt via admin or double-spend check", "chain", chain)
		return true
	}

	solvencyHaltMimirKey := fmt.Sprintf("SolvencyHalt%sChain", chain)
	haltChain, err = k.GetMimir(ctx, solvencyHaltMimirKey)
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		ctx.Logger().Debug("chain is halt via solvency check", "chain", chain)
		return true
	}
	return false
}

func (k KVStore) IsLPPausedV1(ctx cosmos.Context, chain common.Chain) bool {
	// check if global LP is paused
	pauseLPGlobal, err := k.GetMimir(ctx, "PauseLP")
	if err == nil && pauseLPGlobal > 0 && pauseLPGlobal < ctx.BlockHeight() {
		return true
	}

	pauseLP, err := k.GetMimir(ctx, fmt.Sprintf("PauseLP%s", chain))
	if err == nil && pauseLP > 0 && pauseLP < ctx.BlockHeight() {
		ctx.Logger().Debug("chain has paused LP actions", "chain", chain)
		return true
	}
	return false
}
