package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type AddNodeChainSupportHandler struct {
	mgr Manager
}

func NewAddNodeChainSupportHandler(mgr Manager) AddNodeChainSupportHandler {
	return AddNodeChainSupportHandler{
		mgr: mgr,
	}
}

func (h AddNodeChainSupportHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgAddNodeChainSupport)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive add node chain support", "node", msg.Signer, "chain", msg.Chain)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg add node chain support failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg add node chain support", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h AddNodeChainSupportHandler) validate(ctx cosmos.Context, msg MsgAddNodeChainSupport) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	na, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}
	if na.IsEmpty() {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%s is not a node account", msg.Signer))
	}

	supported := false
	for _, c := range common.AllChains {
		if c.Equals(msg.Chain) {
			supported = true
			break
		}
	}
	if !supported {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("chain %s is not supported", msg.Chain))
	}

	return nil
}

func (h AddNodeChainSupportHandler) handle(ctx cosmos.Context, msg MsgAddNodeChainSupport) error {
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
	for _, c := range na.SupportedChains {
		if c == chainStr {
			// already present, idempotent no-op
			return nil
		}
	}

	na.SupportedChains = append(na.SupportedChains, chainStr)
	if err := h.mgr.Keeper().SetNodeAccount(ctx, na); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	evt := NewEventChainSupportAdded(msg.Signer, msg.Chain, ctx.BlockHeight())
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit chain_support_added event", "error", err)
	}

	return nil
}

func AddNodeChainSupportAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgAddNodeChainSupport) (cosmos.Context, error) {
	return nodeAccountSignerPriority(ctx, k, msg.GetSigners())
}

func nodeAccountSignerPriority(ctx cosmos.Context, k keeper.Keeper, signers []cosmos.AccAddress) (cosmos.Context, error) {
	for _, signer := range signers {
		na, err := k.GetNodeAccount(ctx, signer)
		if err != nil {
			return ctx, fmt.Errorf("fail to get node account: %w", err)
		}
		if na.IsEmpty() {
			return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("%s is not a node account", signer))
		}
	}
	return ctx.WithPriority(ActiveNodePriority), nil
}
