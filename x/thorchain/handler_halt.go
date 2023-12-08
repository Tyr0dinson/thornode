package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/mimir"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

func IsTradingHalt(ctx cosmos.Context, keeper keeper.Keeper, msg cosmos.Msg) bool {
	switch m := msg.(type) {
	case *MsgSwap:
		source := common.EmptyChain
		if len(m.Tx.Coins) > 0 {
			source = m.Tx.Coins[0].Asset.GetLayer1Asset().Chain
		}
		target := m.TargetAsset.GetLayer1Asset().Chain
		return IsChainTradingHalted(ctx, keeper, source) || IsChainTradingHalted(ctx, keeper, target) || IsGlobalTradingHalted(ctx, keeper)
	case *MsgAddLiquidity:
		return IsChainTradingHalted(ctx, keeper, m.Asset.Chain) || IsGlobalTradingHalted(ctx, keeper)
	default:
		return IsGlobalTradingHalted(ctx, keeper)
	}
}

func IsGlobalTradingHalted(ctx cosmos.Context, keeper keeper.Keeper) bool {
	haltTrading := mimir.NewPauseSwaps("").FetchValue(ctx, keeper)
	if (haltTrading > 0 && haltTrading < ctx.BlockHeight()) || keeper.RagnarokInProgress(ctx) {
		return true
	}
	return false
}

func IsChainTradingHalted(ctx cosmos.Context, keeper keeper.Keeper, chain common.Chain) bool {
	haltChainTrading := mimir.NewPauseSwaps(chain.String()).FetchValue(ctx, keeper)
	if haltChainTrading > 0 && haltChainTrading < ctx.BlockHeight() {
		ctx.Logger().Info("trading is halt", "chain", chain)
		return true
	}
	// further to check whether the chain is halted
	return IsChainHalted(ctx, keeper, chain)
}

func IsChainHalted(ctx cosmos.Context, keeper keeper.Keeper, chain common.Chain) bool {
	haltChain := mimir.NewPauseSwaps("").FetchValue(ctx, keeper)
	if haltChain > 0 && haltChain < ctx.BlockHeight() {
		ctx.Logger().Info("global is halt")
		return true
	}

	haltChain, err := keeper.GetMimir(ctx, "NodePauseChainGlobal")
	if err == nil && haltChain > ctx.BlockHeight() {
		ctx.Logger().Info("node global is halt")
		return true
	}

	haltChain = mimir.NewPauseChain(chain.String()).FetchValue(ctx, keeper)
	if haltChain > 0 && haltChain < ctx.BlockHeight() {
		ctx.Logger().Info("chain is halt via admin or double-spend check", "chain", chain)
		return true
	}

	solvencyHaltMimirKey := fmt.Sprintf("SolvencyHalt%sChain", chain)
	haltChain, err = keeper.GetMimir(ctx, solvencyHaltMimirKey)
	if err == nil && (haltChain > 0 && haltChain < ctx.BlockHeight()) {
		ctx.Logger().Info("chain is halt via solvency check", "chain", chain)
		return true
	}
	return false
}

func IsLPPaused(ctx cosmos.Context, keeper keeper.Keeper, chain common.Chain) bool {
	// check if global LP is paused
	pauseLPGlobal := mimir.NewPauseLP("").FetchValue(ctx, keeper)
	// k.GetMimir(ctx, "PauseLP")
	if pauseLPGlobal > 0 && pauseLPGlobal < ctx.BlockHeight() {
		return true
	}

	pauseLP := mimir.NewPauseLP(chain.String()).FetchValue(ctx, keeper)
	if pauseLP > 0 && pauseLP < ctx.BlockHeight() {
		ctx.Logger().Info("chain has paused LP actions", "chain", chain)
		return true
	}
	return false
}
