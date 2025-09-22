package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// MoveLiquidityHandler is to handle move liquidity
type MoveLiquidityHandler struct {
	mgr Manager
}

// NewMoveLiquidityHandler create a new instance of AddLiquidityHandler
func NewMoveLiquidityHandler(mgr Manager) MoveLiquidityHandler {
	return MoveLiquidityHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h MoveLiquidityHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgMoveLiquidity)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("received move liquidity request",
		"asset", msg.Asset.String(),
		"tx", msg.TxIn,
		"amount", msg.Amount)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg move liquidity fail validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg add liquidity", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h MoveLiquidityHandler) validate(ctx cosmos.Context, msg MsgMoveLiquidity) error {
	err := msg.ValidateBasic()
	if err != nil {
		return err
	}

	if h.mgr.Keeper().IsRagnarok(ctx, []common.Asset{msg.Asset}) {
		return fmt.Errorf("cannot move liquidity on Ragnaroked pool (%s)", msg.Asset.String())
	}

	_, err = h.mgr.Keeper().GetPool(ctx, msg.Asset)
	if err != nil {
		return ErrInternal(err, "fail to get pool")
	}

	if h.mgr.Keeper().IsChainHalted(ctx, msg.Asset.Chain) || h.mgr.Keeper().IsLPPaused(ctx, msg.Asset.Chain) {
		return fmt.Errorf("unable to move liquidity while chain has paused LP actions")
	}

	return nil
}

func (h MoveLiquidityHandler) handle(ctx cosmos.Context, msg MsgMoveLiquidity) error {
	pool, err := h.mgr.Keeper().GetPool(ctx, msg.Asset)
	if err != nil {
		return ErrInternal(err, "fail to get pool")
	}

	if pool.IsEmpty() {
		return fmt.Errorf("cannot move liquidity on empty pool")
	}

	oldLp, err := h.mgr.Keeper().GetLiquidityProvider(ctx, msg.Asset, msg.TxIn.FromAddress)
	if err != nil {
		return ErrInternal(err, "fail to get liquidity provider")
	}

	if oldLp.Units.Equal(cosmos.ZeroUint()) {
		return fmt.Errorf("no lp found")
	}

	// ensure deposit type remains
	if oldLp.IsSingleSided() {
		if oldLp.RuneAddress.IsEmpty() && !msg.NewRuneAddress.IsEmpty() {
			return fmt.Errorf("cannot change single sided lp type")
		}

		if oldLp.AssetAddress.IsEmpty() && !msg.NewAssetAddress.IsEmpty() {
			return fmt.Errorf("cannot change single sided lp type")
		}
	} else if msg.NewRuneAddress.IsEmpty() || msg.NewAssetAddress.IsEmpty() {
		return fmt.Errorf("cannot change lp type to single sided")
	}

	address := msg.NewRuneAddress
	if address.IsEmpty() {
		address = msg.NewAssetAddress
	}

	newLp, err := h.mgr.Keeper().GetLiquidityProvider(ctx, msg.Asset, address)
	if err != nil {
		return ErrInternal(err, "fail to get liquidity provider")
	}

	newLp.RuneAddress = msg.NewRuneAddress
	newLp.AssetAddress = msg.NewAssetAddress

	runeAmount := common.GetPercentage(oldLp.RuneDepositValue, msg.Amount)
	assetAmount := common.GetPercentage(oldLp.AssetDepositValue, msg.Amount)
	units := common.GetPercentage(oldLp.Units, msg.Amount)

	one := cosmos.NewUint(1)

	if oldLp.RuneDepositValue.Sub(runeAmount).Equal(one) ||
		oldLp.AssetDepositValue.Sub(assetAmount).Equal(one) {
		runeAmount = oldLp.RuneDepositValue
		assetAmount = oldLp.AssetDepositValue
		units = oldLp.Units
	}

	oldLp.RuneDepositValue = oldLp.RuneDepositValue.Sub(runeAmount)
	oldLp.AssetDepositValue = oldLp.AssetDepositValue.Sub(assetAmount)
	oldLp.Units = oldLp.Units.Sub(units)

	newLp.AssetDepositValue = newLp.AssetDepositValue.Add(assetAmount)
	newLp.RuneDepositValue = newLp.RuneDepositValue.Add(runeAmount)
	newLp.Units = newLp.Units.Add(units)
	newLp.LastAddHeight = oldLp.LastAddHeight

	h.mgr.Keeper().SetLiquidityProvider(ctx, newLp)
	h.mgr.Keeper().SetLiquidityProvider(ctx, oldLp)

	return nil
}
