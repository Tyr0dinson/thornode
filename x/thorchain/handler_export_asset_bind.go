package thorchain

import (
	"fmt"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
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

	if !msg.GasAmount.IsZero() {
		if msg.Tx.Chain.Equals(common.THORChain) && msg.Asset.Chain.Equals(common.THORChain) {
			return fmt.Errorf("native asset cannot be funded with RUNE")
		}

		if !msg.Tx.Chain.Equals(common.THORChain) && !msg.Tx.Chain.Equals(msg.Asset.Chain) {
			return fmt.Errorf("gas asset must match asset chain")
		}
	}

	if !msg.AssetAmount.IsZero() {
		if !msg.Tx.Chain.Equals(msg.Asset.Chain) {
			return fmt.Errorf("tx does not match asset chain")
		}
	}

	return nil
}

func (h ExportAssetBindHandler) handle(ctx cosmos.Context, msg MsgExportAssetBind) error {
	if !common.IsSupportedExportAsset(msg.Asset) {
		return fmt.Errorf("export asset not supported: %s", msg.Asset.String())
	}

	ctx.Logger().Info("handle bind export asset", "asset", msg.Asset.String())

	export, err := h.mgr.Keeper().GetAssetExport(ctx, msg.Asset)
	if err != nil {
		return err
	}

	ctx.Logger().Info("export: " + export.String())

	if export.Asset.IsEmpty() {
		export.Asset = msg.Asset
	}

	gasAmountRune := cosmos.ZeroUint()
	gasAmountL1 := cosmos.ZeroUint()

	if msg.Asset.Chain.Equals(common.THORChain) {
		export.AmountNative = export.AmountNative.Add(msg.AssetAmount)
		export.GasReserveRune = export.GasReserveRune.Add(msg.GasAmount)
		gasAmountRune = msg.GasAmount
	} else {
		export.AmountExternal = export.AmountExternal.Add(msg.AssetAmount)
		export.GasReserveL1 = export.GasReserveL1.Add(msg.GasAmount)
		gasAmountL1 = msg.GasAmount
	}

	err = h.mgr.Keeper().SetAssetExport(ctx, export)
	if err != nil {
		return err
	}

	ctx.Logger().Info("ledger set")

	if !msg.AssetAmount.IsZero() {
		event := NewEventBindExportAsset(msg.Asset, msg.AssetAmount)
		if err = h.mgr.EventMgr().EmitEvent(ctx, event); err != nil {
			return ErrInternal(err, "fail to emit bind export asset event")
		}
	}

	if !msg.GasAmount.IsZero() {
		event := NewEventFundExportAsset(msg.Asset, gasAmountL1, gasAmountRune)
		if err = h.mgr.EventMgr().EmitEvent(ctx, event); err != nil {
			return ErrInternal(err, "fail to emit fund export asset event")
		}
	}

	return nil
}
