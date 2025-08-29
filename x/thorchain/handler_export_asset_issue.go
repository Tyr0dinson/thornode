package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type ExportAssetIssueHandler struct {
	mgr Manager
}

func NewExportAssetIssueHandler(mgr Manager) *ExportAssetIssueHandler {
	return &ExportAssetIssueHandler{
		mgr: mgr,
	}
}

func (h ExportAssetIssueHandler) Run(
	ctx cosmos.Context,
	m cosmos.Msg,
) (*cosmos.Result, error) {
	msg, ok := m.(*MsgExportAssetIssue)
	if !ok {
		return nil, errInvalidMessage
	}

	ctx.Logger().Info(
		"received export asset issue request",
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
			"fail to process msg export asset issue",
			"error", err,
		)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ExportAssetIssueHandler) validate(ctx cosmos.Context, msg MsgExportAssetIssue) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

func (h ExportAssetIssueHandler) handle(ctx cosmos.Context, msg MsgExportAssetIssue) error {
	ctx.Logger().Info("handle issue export asset", "asset", msg.Asset.String())

	gasAsset := msg.Asset.Chain.GetGasAsset()

	pools := make([]Pool, 2)
	for i, asset := range []common.Asset{gasAsset, msg.Asset} {
		pool, err := h.mgr.Keeper().GetPool(ctx, asset)
		if err != nil {
			return fmt.Errorf("failed to get pool %s: %w", asset, err)
		}

		if pool.IsEmpty() {
			return fmt.Errorf("pool not found: %s", asset)
		}

		pools[i] = pool
	}

	var (
		gasPool   = pools[0]
		assetPool = pools[1]
	)

	export, err := h.mgr.Keeper().GetAssetExport(ctx, msg.Asset)
	if err != nil {
		return fmt.Errorf("failed to get export %s: %w", msg.Asset, err)
	}

	if export.AmountExternal.LT(msg.Amount) {
		return fmt.Errorf("amount of external asset is less than the amount provided")
	}

	maxGas, err := h.mgr.GasMgr().GetMaxGas(ctx, msg.Asset.Chain)
	if err != nil {
		return fmt.Errorf("failed to get outbound fee for asset %s: %w", msg.Asset.String(), err)
	}

	if msg.Asset.IsGasAsset() {
		return fmt.Errorf("cannot issue to gas asset: %s", msg.Asset.String())
	}

	// calculate amount of RUNE needed to cover gas

	numerator := gasPool.BalanceRune.Mul(maxGas.Amount)
	denominator := gasPool.BalanceAsset.Sub(maxGas.Amount)

	if denominator.LTE(cosmos.ZeroUint()) {
		return fmt.Errorf("denominator <= 0")
	}

	amountRune := numerator.Quo(denominator)

	// calculate amount of ASSET needed for amountRune

	numerator = assetPool.BalanceAsset.Mul(amountRune)
	denominator = assetPool.BalanceRune.Sub(amountRune)

	if denominator.LTE(cosmos.ZeroUint()) {
		return fmt.Errorf("denominator <= 0")
	}

	amountAsset := numerator.Quo(denominator)

	if msg.Amount.LTE(amountAsset) {
		return fmt.Errorf("gas costs are larger than asset amount")
	}

	if gasPool.BalanceAsset.LTE(amountAsset) {
		return fmt.Errorf("pool has not enough funds for gas: %s", msg.Asset.String())
	}

	if assetPool.BalanceRune.LTE(amountRune) {
		return fmt.Errorf("pool has not enough funds for gas: %s", msg.Asset.String())
	}

	assetPool.BalanceRune = common.SafeSub(assetPool.BalanceRune, amountRune)
	assetPool.BalanceAsset = assetPool.BalanceAsset.Add(amountAsset)
	gasPool.BalanceRune = gasPool.BalanceRune.Add(amountRune)

	amountOut := msg.Amount.Sub(amountAsset)

	export.AmountNative = export.AmountNative.Add(amountOut)
	export.AmountExternal = export.AmountExternal.Sub(amountOut)

	err = h.mgr.Keeper().SetAssetExport(ctx, export)
	if err != nil {
		return err
	}

	asset, err := h.mgr.Keeper().GetExportAsset(ctx, msg.Asset)
	if err != nil {
		return err
	}

	asset.AmountBound = asset.AmountBound.Add(amountOut)
	asset.AmountExported = asset.AmountExported.Add(amountOut)

	err = h.mgr.Keeper().SetExportAsset(ctx, asset)
	if err != nil {
		return err
	}

	txOutItem := TxOutItem{
		Chain:     msg.Asset.GetChain(),
		InHash:    common.BlankTxID,
		ToAddress: msg.Address,
		Coin:      common.NewCoin(msg.Asset, amountOut),
	}

	ok, err := h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, txOutItem, cosmos.ZeroUint())
	if err != nil {
		return ErrInternal(err, "fail to add outbound tx")
	}
	if !ok {
		return errFailAddOutboundTx
	}

	return nil
}
