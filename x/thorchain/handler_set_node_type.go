package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// SetNodeTypeHandler processes MsgSetNodeType
// MsgSetNodeType is used by operators after the node account has been white listed to indicate which type of node they are running (Lite or Validator)
type SetNodeTypeHandler struct {
	mgr Manager
}

// NewSetNodeTypeHandler create a new instance of SetNodeTypeHandler
func NewSetNodeTypeHandler(mgr Manager) SetNodeTypeHandler {
	return SetNodeTypeHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to process MsgSetNodeType
func (h SetNodeTypeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSetNodeType)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSetNodeType failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSetNodeType", "error", err)
	}
	return result, err
}

func (h SetNodeTypeHandler) validate(ctx cosmos.Context, msg MsgSetNodeType) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.70.0")) {
		return h.validateV70(ctx, msg)
	}
	return errInvalidVersion
}

func (h SetNodeTypeHandler) validateV70(ctx cosmos.Context, msg MsgSetNodeType) error {
	return h.validateCurrent(ctx, msg)
}

func (h SetNodeTypeHandler) validateCurrent(ctx cosmos.Context, msg MsgSetNodeType) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return cosmos.ErrUnauthorized(fmt.Sprintf("fail to get node account(%s):%s", msg.Signer.String(), err))
	}
	if nodeAccount.IsEmpty() {
		return cosmos.ErrUnauthorized(fmt.Sprintf("unauthorized account(%s)", msg.Signer))
	}
	if nodeAccount.Type != NodeTypeUnknown {
		return fmt.Errorf("node type is already set, cannot update")
	}

	return nil
}

func (h SetNodeTypeHandler) handle(ctx cosmos.Context, msg MsgSetNodeType) (*cosmos.Result, error) {
	ctx.Logger().Info("handleMsgSetNodeType request")
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.70.0")) {
		return h.handleV70(ctx, msg)
	}
	return nil, errBadVersion
}

func (h SetNodeTypeHandler) handleV70(ctx cosmos.Context, msg MsgSetNodeType) (*cosmos.Result, error) {
	return h.handleCurrent(ctx, msg)
}

func (h SetNodeTypeHandler) handleCurrent(ctx cosmos.Context, msg MsgSetNodeType) (*cosmos.Result, error) {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		ctx.Logger().Error("fail to get node account", "error", err, "address", msg.Signer.String())
		return nil, cosmos.ErrUnauthorized(fmt.Sprintf("%s is not authorized", msg.Signer))
	}

	c, err := h.mgr.Keeper().GetMimir(ctx, constants.NativeTransactionFee.String())
	if err != nil || c < 0 {
		c = h.mgr.GetConstants().GetInt64Value(constants.NativeTransactionFee)
	}
	cost := cosmos.NewUint(uint64(c))
	if cost.GT(nodeAccount.Bond) {
		cost = nodeAccount.Bond
	}

	// set node type and subtract cost from bond
	nodeAccount.Type = msg.NodeType
	nodeAccount.Bond = common.SafeSub(nodeAccount.Bond, cost)

	if err := h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		return nil, fmt.Errorf("fail to save node account: %w", err)
	}

	// add cost to the reserve
	coin := common.NewCoin(common.RuneNative, cost)
	if !cost.IsZero() {
		if err := h.mgr.Keeper().SendFromModuleToModule(ctx, BondName, ReserveName, common.NewCoins(coin)); err != nil {
			ctx.Logger().Error("fail to transfer funds from bond to reserve", "error", err)
			return nil, err
		}
	}

	tx := common.Tx{}
	tx.ID = common.BlankTxID
	tx.FromAddress = nodeAccount.BondAddress
	bondEvent := NewEventBond(cost, BondCost, tx)
	if err := h.mgr.EventMgr().EmitEvent(ctx, bondEvent); err != nil {
		return nil, fmt.Errorf("fail to emit bond event: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("set_node_type",
			cosmos.NewAttribute("node_address", msg.Signer.String()),
			cosmos.NewAttribute("node_type", msg.NodeType.String())))

	return &cosmos.Result{}, nil
}
