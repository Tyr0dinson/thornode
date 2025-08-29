package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type ExportAssetRedeemHandler struct {
	mgr Manager
}

func NewExportAssetRedeemHandler(mgr Manager) *ExportAssetRedeemHandler {
	return &ExportAssetRedeemHandler{
		mgr: mgr,
	}
}

func (h ExportAssetRedeemHandler) Run(
	ctx cosmos.Context,
	m cosmos.Msg,
) (*cosmos.Result, error) {
	msg, ok := m.(*MsgExportAssetRedeem)
	if !ok {
		return nil, errInvalidMessage
	}

	ctx.Logger().Info(
		"received export asset redeem request",
		"asset", msg.Asset.String(),
		"tx", msg.Tx,
	)

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error(
			"msg export asset bind failed validation",
			"error", err,
		)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error(
			"fail to process msg export asset bind",
			"error", err,
		)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ExportAssetRedeemHandler) validate(ctx cosmos.Context, msg MsgExportAssetRedeem) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

func (h ExportAssetRedeemHandler) handle(ctx cosmos.Context, msg MsgExportAssetRedeem) error {
	return nil
}
