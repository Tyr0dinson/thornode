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

	return nil
}

func (h ExportAssetBindHandler) handle(ctx cosmos.Context, msg MsgExportAssetBind) error {
	if !common.IsSupportedExportAsset(msg.Asset) {
		return fmt.Errorf("export asset not supported: %s", msg.Asset.String())
	}

	ledger, err := h.mgr.Keeper().GetExportLedger(ctx, msg.Asset)
	if err != nil {
		return err
	}

	if ledger.Asset.IsEmpty() {
		ledger.Asset = msg.Asset

	}

	gasAmountRune := cosmos.ZeroUint()
	gasAmountL1 := cosmos.ZeroUint()

	if msg.Asset.Chain.Equals(common.THORChain) {
		ledger.BalanceNative = ledger.BalanceNative.Add(msg.AssetAmount)
		ledger.GasReserveRune = ledger.GasReserveRune.Add(msg.GasAmount)
		gasAmountRune = msg.GasAmount
	} else {
		ledger.BalanceExternal = ledger.BalanceExternal.Add(msg.AssetAmount)
		ledger.GasReserveL1 = ledger.GasReserveL1.Add(msg.GasAmount)
		gasAmountL1 = msg.GasAmount
	}

	err = h.mgr.Keeper().SetExportLedger(ctx, ledger)
	if err != nil {
		return err
	}

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
