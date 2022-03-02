package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

func (h SwapHandler) handleV81(ctx cosmos.Context, msg MsgSwap) (*cosmos.Result, error) {
	// test that the network we are running matches the destination network
	if !common.GetCurrentChainNetwork().SoftEquals(msg.Destination.GetNetwork(msg.Destination.GetChain())) {
		return nil, fmt.Errorf("address(%s) is not same network", msg.Destination)
	}
	transactionFee := h.mgr.GasMgr().GetFee(ctx, msg.Destination.GetChain(), common.RuneAsset())
	synthVirtualDepthMult, err := h.mgr.Keeper().GetMimir(ctx, constants.VirtualMultSynths.String())
	if synthVirtualDepthMult < 1 || err != nil {
		synthVirtualDepthMult = h.mgr.GetConstants().GetInt64Value(constants.VirtualMultSynths)
	}

	if msg.TargetAsset.IsRune() && !msg.TargetAsset.IsNativeRune() {
		return nil, fmt.Errorf("target asset can't be %s", msg.TargetAsset.String())
	}

	swapper := newSwapperV81()
	_, _, swapErr := swapper.swap(
		ctx,
		h.mgr.Keeper(),
		msg.Tx,
		msg.TargetAsset,
		msg.Destination,
		msg.TradeTarget,
		transactionFee,
		synthVirtualDepthMult,
		h.mgr)
	if swapErr != nil {
		return nil, swapErr
	}
	return &cosmos.Result{}, nil
}
