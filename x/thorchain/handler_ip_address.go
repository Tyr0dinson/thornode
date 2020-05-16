package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/constants"
)

// IPAddressHandler is to handle ip address message
type IPAddressHandler struct {
	keeper Keeper
}

// NewIPAddressHandler create new instance of IPAddressHandler
func NewIPAddressHandler(keeper Keeper) IPAddressHandler {
	return IPAddressHandler{
		keeper: keeper,
	}
}

// Run it the main entry point to execute ip address logic
func (h IPAddressHandler) Run(ctx sdk.Context, m sdk.Msg, version semver.Version, _ constants.ConstantValues) (*sdk.Result, error) {
	msg, ok := m.(MsgSetIPAddress)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive ip address", "address", msg.IPAddress)
	if err := h.validate(ctx, msg, version); err != nil {
		ctx.Logger().Error("msg set version failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, msg, version); err != nil {
		ctx.Logger().Error("fail to process msg set version", "error", err)
		return nil, err
	}

	return &sdk.Result{}, nil
}

func (h IPAddressHandler) validate(ctx sdk.Context, msg MsgSetIPAddress, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	} else {
		return errBadVersion
	}
}

func (h IPAddressHandler) validateV1(ctx sdk.Context, msg MsgSetIPAddress) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	nodeAccount, err := h.keeper.GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		ctx.Logger().Error("fail to get node account", "error", err, "address", msg.Signer.String())
		return common.ErrUnauthorized(fmt.Sprintf("%s is not authorizaed", msg.Signer))
	}
	if nodeAccount.IsEmpty() {
		ctx.Logger().Error("unauthorized account", "address", msg.Signer.String())
		return common.ErrUnauthorized(fmt.Sprintf("%s is not authorizaed", msg.Signer))
	}

	return nil
}

func (h IPAddressHandler) handle(ctx sdk.Context, msg MsgSetIPAddress, version semver.Version) error {
	ctx.Logger().Info("handleMsgSetIPAddress request", "ip address", msg.IPAddress)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg)
	} else {
		ctx.Logger().Error(errInvalidVersion.Error())
		return errBadVersion
	}
}

func (h IPAddressHandler) handleV1(ctx sdk.Context, msg MsgSetIPAddress) error {
	nodeAccount, err := h.keeper.GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		ctx.Logger().Error("fail to get node account", "error", err, "address", msg.Signer.String())
		return common.ErrUnauthorized(fmt.Sprintf("unable to find account: %s", msg.Signer))
	}

	nodeAccount.IPAddress = msg.IPAddress
	if err := h.keeper.SetNodeAccount(ctx, nodeAccount); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent("set_ip_address",
			sdk.NewAttribute("thor_address", msg.Signer.String()),
			sdk.NewAttribute("address", msg.IPAddress)))

	return nil
}
