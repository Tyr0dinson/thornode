package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// NodeChainSupportHandler processes chain support add/remove messages
type NodeChainSupportHandler struct {
	mgr Manager
}

// NewNodeChainSupportHandler creates new instance of NodeChainSupportHandler
func NewNodeChainSupportHandler(mgr Manager) NodeChainSupportHandler {
	return NodeChainSupportHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to process chain support messages
func (h NodeChainSupportHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	switch msg := m.(type) {
	case *MsgAddNodeChainSupport:
		return h.handleAddChainSupport(ctx, *msg)
	case *MsgRemoveNodeChainSupport:
		return h.handleRemoveChainSupport(ctx, *msg)
	default:
		return nil, errInvalidMessage
	}
}

func (h NodeChainSupportHandler) handleAddChainSupport(ctx cosmos.Context, msg MsgAddNodeChainSupport) (*cosmos.Result, error) {
	ctx.Logger().Info("receive add node chain support", "node", msg.Signer, "chain", msg.Chain)
	if err := h.validateAddChainSupport(ctx, msg); err != nil {
		ctx.Logger().Error("msg add node chain support failed validation", "error", err)
		return nil, err
	}
	if err := h.processAddChainSupport(ctx, msg); err != nil {
		ctx.Logger().Error("fail to process add node chain support", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h NodeChainSupportHandler) handleRemoveChainSupport(ctx cosmos.Context, msg MsgRemoveNodeChainSupport) (*cosmos.Result, error) {
	ctx.Logger().Info("receive remove node chain support", "node", msg.Signer, "chain", msg.Chain)
	if err := h.validateRemoveChainSupport(ctx, msg); err != nil {
		ctx.Logger().Error("msg remove node chain support failed validation", "error", err)
		return nil, err
	}
	if err := h.processRemoveChainSupport(ctx, msg); err != nil {
		ctx.Logger().Error("fail to process remove node chain support", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

// validateChainSupportMessage validates common fields between add and remove chain support messages
func (h NodeChainSupportHandler) validateChainSupportMessage(ctx cosmos.Context, signer cosmos.AccAddress, chainStr string) (NodeAccount, common.Chain, error) {
	// Validate that signer is a node (in any status, not just active)
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, signer)
	if err != nil {
		return NodeAccount{}, common.EmptyChain, fmt.Errorf("signer is not a node account: %w", err)
	}
	if nodeAccount.IsEmpty() {
		return NodeAccount{}, common.EmptyChain, fmt.Errorf("node account not found")
	}

	// Validate chain format
	chain, err := common.NewChain(chainStr)
	if err != nil {
		return NodeAccount{}, common.EmptyChain, fmt.Errorf("invalid chain: %s", chainStr)
	}
	if chain.IsEmpty() {
		return NodeAccount{}, common.EmptyChain, fmt.Errorf("chain cannot be empty")
	}

	return nodeAccount, chain, nil
}

func (h NodeChainSupportHandler) validateAddChainSupport(ctx cosmos.Context, msg MsgAddNodeChainSupport) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if chain-aware consensus is enabled
	enableChainConsensus := h.mgr.Keeper().GetConfigInt64(ctx, constants.EnableNodeChainConsensus)
	if enableChainConsensus <= 0 {
		return fmt.Errorf("chain-aware consensus feature is not enabled")
	}

	// Validate common fields
	_, chain, err := h.validateChainSupportMessage(ctx, msg.Signer, msg.Chain)
	if err != nil {
		return err
	}

	// Check if chain exists in AllChains
	validChain := false
	for _, supportedChain := range common.AllChains {
		if supportedChain.Equals(chain) {
			validChain = true
			break
		}
	}
	if !validChain {
		return fmt.Errorf("chain %s is not supported by THORChain", chain.String())
	}

	return nil
}

func (h NodeChainSupportHandler) validateRemoveChainSupport(ctx cosmos.Context, msg MsgRemoveNodeChainSupport) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Validate common fields
	_, _, err := h.validateChainSupportMessage(ctx, msg.Signer, msg.Chain)
	if err != nil {
		return err
	}

	return nil
}

func (h NodeChainSupportHandler) processAddChainSupport(ctx cosmos.Context, msg MsgAddNodeChainSupport) error {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}

	// Store the canonical chain name (common.NewChain uppercases), so "btc"
	// and "BTC" are one entry everywhere SupportedChains is consumed.
	chain, err := common.NewChain(msg.Chain)
	if err != nil {
		return fmt.Errorf("invalid chain: %s", msg.Chain)
	}

	// Check if chain is already supported to maintain uniqueness. Compare
	// canonical forms so an entry stored before normalization also matches.
	for _, supportedChain := range nodeAccount.SupportedChains {
		if existing, parseErr := common.NewChain(supportedChain); parseErr == nil && existing.Equals(chain) {
			return nil // Already supported, nothing to do
		}
	}

	// Add chain to supported chains
	nodeAccount.SupportedChains = append(nodeAccount.SupportedChains, chain.String())

	// Save updated node account
	if err := h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	ctx.Logger().Info("node added chain support", "node", msg.Signer, "chain", chain.String())
	return nil
}

func (h NodeChainSupportHandler) processRemoveChainSupport(ctx cosmos.Context, msg MsgRemoveNodeChainSupport) error {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return fmt.Errorf("fail to get node account: %w", err)
	}

	chain, err := common.NewChain(msg.Chain)
	if err != nil {
		return fmt.Errorf("invalid chain: %s", msg.Chain)
	}

	// Remove every entry whose canonical form is the chain, including case
	// variants stored before add normalized its input.
	newSupportedChains := make([]string, 0, len(nodeAccount.SupportedChains))
	for _, supportedChain := range nodeAccount.SupportedChains {
		if existing, parseErr := common.NewChain(supportedChain); parseErr == nil && existing.Equals(chain) {
			continue
		}
		newSupportedChains = append(newSupportedChains, supportedChain)
	}

	// Check minimum chain support requirement
	minChainSupport := h.mgr.GetConstants().GetInt64Value(constants.MinNodeChainSupport)
	if int64(len(newSupportedChains)) < minChainSupport {
		return fmt.Errorf("cannot remove chain: node must support at least %d chain(s), would have %d after removal",
			minChainSupport, len(newSupportedChains))
	}

	nodeAccount.SupportedChains = newSupportedChains

	// Save updated node account
	if err := h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	ctx.Logger().Info("node removed chain support", "node", msg.Signer, "chain", chain.String())
	return nil
}

// NodeChainSupportAnteHandler called by the ante handler to gate mempool entry.
// Any validator node account (regardless of status) may update its supported
// chains, but like the other node config messages (set IP address, set node
// keys, set version) each message is charged the native tx fee from bond. Add
// messages are rejected here while chain-aware consensus is off, so they do not
// reach the mempool only to fail in the handler.
func NodeChainSupportAnteHandler(ctx cosmos.Context, k keeper.Keeper, msg cosmos.Msg) (cosmos.Context, error) {
	var signer cosmos.AccAddress
	switch m := msg.(type) {
	case *MsgAddNodeChainSupport:
		if k.GetConfigInt64(ctx, constants.EnableNodeChainConsensus) <= 0 {
			return ctx, cosmos.ErrUnauthorized("chain-aware consensus feature is not enabled")
		}
		signer = m.Signer
	case *MsgRemoveNodeChainSupport:
		signer = m.Signer
	default:
		return ctx, fmt.Errorf("invalid message type for node chain support ante handler")
	}
	if !isSignedByNodeAccounts(ctx, k, []cosmos.AccAddress{signer}) {
		return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("%s is not authorized", signer))
	}
	return ctx, k.DeductNativeTxFeeFromBond(ctx, signer)
}
