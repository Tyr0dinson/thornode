package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// Create a new handler for donate message
func NewDonateHandler(mgr Manager) TypedHandler[MsgDonate] {
	return BaseHandler[MsgDonate]{
		mgr: mgr,
		validators: NewValidators[MsgDonate]().
			Register("0.80.0", DonateValidateV80),
		handlers: NewHandlers[MsgDonate]().
			Register("0.1.0", DonateHandleV1),
	}
}

func DonateValidateV80(mgr Manager, ctx cosmos.Context, msg MsgDonate) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	if msg.Asset.IsSyntheticAsset() {
		ctx.Logger().Error("asset cannot be synth", "error", errInvalidMessage)
		return errInvalidMessage
	}
	return nil
}

// handle process MsgDonate, MsgDonate add asset and RUNE to the asset pool
// it simply increase the pool asset/RUNE balance but without taking any of the pool units

func DonateHandleV1(mgr Manager, ctx cosmos.Context, msg MsgDonate) (*cosmos.Result, error) {
	pool, err := mgr.Keeper().GetPool(ctx, msg.Asset)
	if err != nil {
		return nil, ErrInternal(err, fmt.Sprintf("fail to get pool for (%s)", msg.Asset))
	}
	if pool.Asset.IsEmpty() {
		return nil, cosmos.ErrUnknownRequest(fmt.Sprintf("pool %s not exist", msg.Asset.String()))
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(msg.AssetAmount)
	pool.BalanceRune = pool.BalanceRune.Add(msg.RuneAmount)

	if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
		return nil, ErrInternal(err, fmt.Sprintf("fail to set pool(%s)", pool))
	}
	// emit event
	donateEvt := NewEventDonate(pool.Asset, msg.Tx)
	if err := mgr.EventMgr().EmitEvent(ctx, donateEvt); err != nil {
		return nil, cosmos.Wrapf(errFailSaveEvent, "fail to save donate events: %w", err)
	}
	return &cosmos.Result{}, nil
}
