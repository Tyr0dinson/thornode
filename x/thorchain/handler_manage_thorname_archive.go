package thorchain

import (
	"bytes"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (h ManageTHORNameHandler) handleV3_0_0(ctx cosmos.Context, msg MsgManageTHORName) (*cosmos.Result, error) {
	var err error

	enable, _ := h.mgr.Keeper().GetMimir(ctx, "THORNames")
	if enable == 0 {
		return nil, fmt.Errorf("THORNames are currently disabled")
	}

	tn := THORName{Name: msg.Name, Owner: msg.Signer, PreferredAsset: common.EmptyAsset}
	exists := h.mgr.Keeper().THORNameExists(ctx, msg.Name)
	if exists {
		tn, err = h.mgr.Keeper().GetTHORName(ctx, msg.Name)
		if err != nil {
			return nil, err
		}
	}

	registrationFeePaid := cosmos.ZeroUint()
	fundPaid := cosmos.ZeroUint()

	// check if user is trying to extend expiration
	if !msg.Coin.Amount.IsZero() {
		// check that THORName is still valid, can't top up an invalid THORName
		if err = h.validateName(msg.Name); err != nil {
			return nil, err
		}
		var addBlocks int64
		// registration fee is for THORChain addresses only
		if !exists {
			// minus registration fee
			registrationFee := h.mgr.Keeper().GetTHORNameRegisterFee(ctx)
			msg.Coin.Amount = common.SafeSub(msg.Coin.Amount, registrationFee)
			registrationFeePaid = registrationFee
			addBlocks = h.mgr.GetConstants().GetInt64Value(constants.BlocksPerYear) // registration comes with 1 free year
		}
		feePerBlock := h.mgr.Keeper().GetTHORNamePerBlockFee(ctx)
		fundPaid = msg.Coin.Amount
		addBlocks += (int64(msg.Coin.Amount.Uint64()) / int64(feePerBlock.Uint64()))
		if tn.ExpireBlockHeight < ctx.BlockHeight() {
			tn.ExpireBlockHeight = ctx.BlockHeight() + addBlocks
		} else {
			tn.ExpireBlockHeight += addBlocks
		}
	}

	// check if we need to reduce the expire time, upon user request
	if msg.ExpireBlockHeight > 0 && msg.ExpireBlockHeight < tn.ExpireBlockHeight {
		tn.ExpireBlockHeight = msg.ExpireBlockHeight
	}

	// check if we need to update the preferred asset
	if !tn.PreferredAsset.Equals(msg.PreferredAsset) && !msg.PreferredAsset.IsEmpty() {
		tn.PreferredAsset = msg.PreferredAsset
	}

	tn.SetAlias(msg.Chain, msg.Address) // update address
	// Update owner if it has changed
	// Also, if owner has changed, null out the PreferredAsset/Aliases so the new owner is forced to reset it.
	if !msg.Owner.Empty() && !bytes.Equal(msg.Owner, tn.Owner) {
		tn.Owner = msg.Owner
		tn.PreferredAsset = common.EmptyAsset
		tn.Aliases = []types.THORNameAlias{}
	}
	h.mgr.Keeper().SetTHORName(ctx, tn)

	evt := NewEventTHORName(tn.Name, msg.Chain, msg.Address, registrationFeePaid, fundPaid, tn.ExpireBlockHeight, tn.Owner)
	if err = h.mgr.EventMgr().EmitEvent(ctx, evt); nil != err {
		ctx.Logger().Error("fail to emit THORName event", "error", err)
	}

	return &cosmos.Result{}, nil
}
