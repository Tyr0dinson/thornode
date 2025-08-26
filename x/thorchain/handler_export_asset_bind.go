package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ExportAssetBindHandler struct {
	mgr Manager
}

func NewExportAssetBindHandler(mgr Manager) *ExportAssetBindHandler {
	return &ExportAssetBindHandler{
		mgr: mgr,
	}
}

func (h ExportAssetBindHandler) Run(
	ctx cosmos.Context,
	m cosmos.Msg,
) (*cosmos.Result, error) {
	msg, ok := m.(*MsgExportAssetBind)
	if !ok {
		return nil, errInvalidMessage
	}

	ctx.Logger().Info(
		"received export asset bind request",
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

func (h ExportAssetBindHandler) validate(ctx cosmos.Context, msg MsgExportAssetBind) error {
	if !common.IsSupportedExportAsset(msg.Asset) {
		return fmt.Errorf("export asset not supported: %s", msg.Asset.String())
	}

	if msg.Amount.IsZero() {
		return fmt.Errorf("amount must be greater than zero: %s", msg.Amount.String())
	}

	if !msg.Tx.Chain.Equals(msg.Asset.Chain) {
		return fmt.Errorf("tx does not match asset chain")
	}

	return nil
}

func (h ExportAssetBindHandler) handle(ctx cosmos.Context, msg MsgExportAssetBind) error {
	if !common.IsSupportedExportAsset(msg.Asset) {
		return fmt.Errorf("export asset not supported: %s", msg.Asset.String())
	}

	ctx.Logger().Info("handle bind export asset", "asset", msg.Asset.String())

	asset, err := h.mgr.Keeper().GetExportAsset(ctx, msg.Asset)
	if err != nil {
		asset = types.NewExportAsset()
		asset.Asset, err = common.NewAsset("THOR." + msg.Asset.Symbol.String())
		if err != nil {
			return err
		}
	}

	if msg.Asset.Chain.Equals(common.THORChain) {
		return h.handleAsset(ctx, msg)
	} else {
		return h.handleExport(ctx, msg)
	}
}

func (h ExportAssetBindHandler) handleAsset(ctx cosmos.Context, msg MsgExportAssetBind) error {
	asset, err := h.mgr.Keeper().GetExportAsset(ctx, msg.Asset)
	if err != nil {
		asset = types.NewExportAsset()
		asset.Asset = msg.Asset
	}

	asset.AmountBound = asset.AmountBound.Add(msg.Amount)

	return h.mgr.Keeper().SetExportAsset(ctx, asset)
}

func (h ExportAssetBindHandler) handleExport(ctx cosmos.Context, msg MsgExportAssetBind) error {
	export, err := h.mgr.Keeper().GetAssetExport(ctx, msg.Asset)
	fmt.Println(export)
	if err != nil {
		export = types.NewAssetExport()
		export.Asset = msg.Asset

		native, err := common.NewAsset(msg.Asset.Symbol.String())
		if err != nil {
			return err
		}

		token, found := common.AssetExports[native][msg.Asset.Chain]
		if !found {
			return fmt.Errorf("asset %s not found in export %s", native, msg.Asset.String())
		}

		export.Address = token.Address
		export.Decimals = uint64(token.Decimals)
	}

	export.AmountExternal = export.AmountExternal.Add(msg.Amount)

	return h.mgr.Keeper().SetAssetExport(ctx, export)
}
