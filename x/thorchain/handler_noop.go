package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// Handler for the donate message
func NewNoOpHandler(mgr Manager) TypedHandler[MsgNoOp] {
	return BaseHandler[MsgNoOp]{
		mgr: mgr,
		validators: NewValidators[MsgNoOp]().
			Register("0.1.0", NoOpValidateV1),
		handlers: NewHandlers[MsgNoOp]().
			Register("0.1.0", NoOpHandleV1),
	}
}

func NoOpValidateV1(mgr Manager, ctx cosmos.Context, msg MsgNoOp) error {
	return msg.ValidateBasic()
}

// handle process MsgNoOp, MsgNoOp add asset and RUNE to the asset pool
// it simply increase the pool asset/RUNE balance but without taking any of the pool units

func NoOpHandleV1(mgr Manager, ctx cosmos.Context, msg MsgNoOp) (*cosmos.Result, error) {
	action := msg.GetAction()
	if len(action) == 0 {
		return &cosmos.Result{}, nil
	}
	if !strings.EqualFold(action, "novault") {
		return &cosmos.Result{}, nil
	}
	vault, err := mgr.Keeper().GetVault(ctx, msg.ObservedTx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err, "pub_key", msg.ObservedTx.ObservedPubKey)
		return &cosmos.Result{}, nil
	}
	// subtract the coins from vault , as it has been added to
	vault.SubFunds(msg.ObservedTx.Tx.Coins)
	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to save vault", "error", err)
	}
	return &cosmos.Result{}, nil
}
