package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (h ManageTHORNameHandler) validateV116(ctx cosmos.Context, msg MsgManageTHORName) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// TODO on hard fork move network check to ValidateBasic
	if !common.CurrentChainNetwork.SoftEquals(msg.Address.GetNetwork(msg.Address.GetChain())) {
		return fmt.Errorf("address(%s) is not same network", msg.Address)
	}

	exists := h.mgr.Keeper().THORNameExists(ctx, msg.Name)

	if !exists {
		// thorname doesn't appear to exist, let's validate the name
		if err := h.validateName(msg.Name); err != nil {
			return err
		}
		registrationFee := h.mgr.Keeper().GetTHORNameRegisterFee(ctx)
		if msg.Coin.Amount.LTE(registrationFee) {
			return fmt.Errorf("not enough funds")
		}
	} else {
		name, err := h.mgr.Keeper().GetTHORName(ctx, msg.Name)
		if err != nil {
			return err
		}

		// if this thorname is already owned, check signer has ownership. If
		// expiration is past, allow different user to take ownership
		if !name.Owner.Equals(msg.Signer) && ctx.BlockHeight() <= name.ExpireBlockHeight {
			ctx.Logger().Error("no authorization", "owner", name.Owner)
			return fmt.Errorf("no authorization: owned by %s", name.Owner)
		}

		// ensure user isn't inflating their expire block height artificaially
		if name.ExpireBlockHeight < msg.ExpireBlockHeight {
			return errors.New("cannot artificially inflate expire block height")
		}
	}

	// validate preferred asset pool exists and is active
	if !msg.PreferredAsset.IsEmpty() {
		if !h.mgr.Keeper().PoolExist(ctx, msg.PreferredAsset) {
			return fmt.Errorf("pool %s does not exist", msg.PreferredAsset)
		}
		pool, err := h.mgr.Keeper().GetPool(ctx, msg.PreferredAsset)
		if err != nil {
			return err
		}
		if pool.Status != PoolAvailable {
			return fmt.Errorf("pool %s is not available", msg.PreferredAsset)
		}
	}

	return nil
}
