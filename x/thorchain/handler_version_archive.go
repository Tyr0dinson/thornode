package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (h VersionHandler) handleV115(ctx cosmos.Context, msg MsgSetVersion) error {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		return cosmos.ErrUnauthorized(fmt.Errorf("unable to find account(%s):%w", msg.Signer, err).Error())
	}

	version, err := msg.GetVersion()
	if err != nil {
		return fmt.Errorf("fail to parse version: %w", err)
	}

	if nodeAccount.GetVersion().LT(version) {
		nodeAccount.Version = version.String()
	}

	if err := h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("set_version",
			cosmos.NewAttribute("thor_address", msg.Signer.String()),
			cosmos.NewAttribute("version", msg.Version)))

	if nodeAccount.Status == NodeActive {
		// This could affect the MinJoinVersion, so update it.
		h.mgr.Keeper().SetMinJoinLast(ctx)
	}

	return nil
}
