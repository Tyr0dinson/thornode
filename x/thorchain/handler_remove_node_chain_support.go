package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type RemoveNodeChainSupportHandler struct {
	mgr Manager
}

func NewRemoveNodeChainSupportHandler(mgr Manager) RemoveNodeChainSupportHandler {
	return RemoveNodeChainSupportHandler{
		mgr: mgr,
	}
}

func (h RemoveNodeChainSupportHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgRemoveNodeChainSupport)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive remove node chain support", "node", msg.Signer, "chain", msg.Chain)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg remove node chain support failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg remove node chain support", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h RemoveNodeChainSupportHandler) validate(ctx cosmos.Context, msg MsgRemoveNodeChainSupport) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// THORChain is the native chain and must remain in every node's effective
	// chain set; otherwise partitionByChainSupport can produce a keygen
	// partition without RUNE support.
	if msg.Chain.Equals(common.THORChain) {
		return cosmos.ErrUnknownRequest("cannot remove THORChain from a node's supported chains")
	}

	na, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}
	if na.IsEmpty() {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%s is not a node account", msg.Signer))
	}

	chainStr := msg.Chain.String()
	effectiveChains := getNodeSupportedChains(ctx, h.mgr, na)

	found := false
	for _, c := range effectiveChains {
		if c.String() == chainStr {
			found = true
			break
		}
	}
	if !found {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("chain %s is not in node's supported chains", msg.Chain))
	}

	minSupport := h.mgr.Keeper().GetConfigInt64(ctx, constants.MinNodeChainSupport)
	if int64(len(effectiveChains))-1 < minSupport {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("removing chain %s would drop below minimum chain support (%d)", msg.Chain, minSupport))
	}

	return nil
}

func (h RemoveNodeChainSupportHandler) handle(ctx cosmos.Context, msg MsgRemoveNodeChainSupport) error {
	na, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}

	// If SupportedChains is empty/nil, materialize the full list first
	if len(na.SupportedChains) == 0 {
		for _, c := range getNodeSupportedChains(ctx, h.mgr, na) {
			na.SupportedChains = append(na.SupportedChains, c.String())
		}
	}

	chainStr := msg.Chain.String()
	newChains := make([]string, 0, len(na.SupportedChains))
	for _, c := range na.SupportedChains {
		if c != chainStr {
			newChains = append(newChains, c)
		}
	}
	na.SupportedChains = newChains

	if err := h.mgr.Keeper().SetNodeAccount(ctx, na); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	evt := NewEventChainSupportRemoved(msg.Signer, msg.Chain, ctx.BlockHeight())
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit chain_support_removed event", "error", err)
	}

	return nil
}

func RemoveNodeChainSupportAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgRemoveNodeChainSupport) (cosmos.Context, error) {
	return nodeAccountSignerPriority(ctx, k, msg.GetSigners())
}
