package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// BondYieldHandler processes bond provider yield claims and auto-claim settings.
type BondYieldHandler struct {
	mgr Manager
}

// NewBondYieldHandler creates the handler for bond yield claim and auto-claim messages.
func NewBondYieldHandler(mgr Manager) BondYieldHandler {
	return BondYieldHandler{mgr: mgr}
}

// Run validates and applies a MsgBondYield transaction.
func (h BondYieldHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBondYield)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgBondYield",
		"node address", msg.NodeAddress,
		"request hash", msg.TxIn.ID,
		"amount", msg.Amount,
		"auto", msg.AutoClaim,
	)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg bond yield fail validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg bond yield", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

// validate checks basic state, claim settings, and payout destination constraints.
func (h BondYieldHandler) validate(ctx cosmos.Context, msg MsgBondYield) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if isBondYieldPaused(ctx, h.mgr) {
		return ErrInternal(nil, "bond yield has been paused")
	}

	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get node account(%s)", msg.NodeAddress))
	}
	if nodeAccount.Status == NodeUnknown {
		return ErrInternal(nil, "node account status is unknown")
	}

	if _, err = msg.TxIn.FromAddress.AccAddress(); err != nil {
		return cosmos.ErrInvalidAddress("bond yield signer must be a THORChain address")
	}

	if msg.AutoClaim {
		if msg.AutoClaimBps > 10000 {
			return cosmos.ErrUnknownRequest("auto claim basis points cannot exceed 10000")
		}
		// Empty payout settings mean the provider keeps the default RUNE payout to its bond address.
		if msg.PayoutAsset.IsEmpty() {
			if !msg.PayoutAddress.IsEmpty() {
				return cosmos.ErrUnknownRequest("payout address requires a payout asset")
			}
			return nil
		}
		if msg.PayoutAsset.IsRune() {
			if !msg.PayoutAddress.IsEmpty() {
				if !msg.PayoutAddress.IsChain(common.THORChain) {
					return cosmos.ErrUnknownRequest("rune payout address must be a THORChain address")
				}
				if _, err = msg.PayoutAddress.AccAddress(); err != nil {
					return cosmos.ErrInvalidAddress("invalid rune payout address")
				}
			}
			return nil
		}
		if msg.PayoutAddress.IsEmpty() {
			return cosmos.ErrUnknownRequest("payout address is required for non-rune payout asset")
		}
		if !msg.PayoutAddress.IsChain(msg.PayoutAsset.GetChain()) {
			return cosmos.ErrUnknownRequest("payout address chain does not match payout asset")
		}
		if !h.mgr.Keeper().PoolExist(ctx, msg.PayoutAsset) {
			return fmt.Errorf("pool %s does not exist", msg.PayoutAsset)
		}
		pool, err := h.mgr.Keeper().GetPool(ctx, msg.PayoutAsset)
		if err != nil {
			return err
		}
		if pool.Status != PoolAvailable {
			return fmt.Errorf("pool %s is not available", msg.PayoutAsset)
		}
		return nil
	}

	if msg.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("bond yield claim amount cannot be zero")
	}
	return nil
}

// handle stores auto-claim settings or executes an immediate bond yield claim.
func (h BondYieldHandler) handle(ctx cosmos.Context, msg MsgBondYield) error {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get node account(%s)", msg.NodeAddress))
	}

	providerAcc, err := msg.TxIn.FromAddress.AccAddress()
	if err != nil {
		return ErrInternal(err, "fail to parse bond provider address")
	}

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.NodeAddress))
	}
	if err = passiveBackfill(ctx, h.mgr, nodeAccount, &bp); err != nil {
		return err
	}
	bp.Adjust(nodeAccount.Bond)
	if !bp.Has(providerAcc) {
		return cosmos.ErrUnknownRequest("bond provider address is not valid for node account")
	}

	if msg.AutoClaim {
		if !bp.SetAutoClaim(providerAcc, msg.AutoClaimBps, msg.PayoutAsset, msg.PayoutAddress) {
			return cosmos.ErrUnknownRequest("bond provider address is not valid for node account")
		}
		return h.mgr.Keeper().SetBondProviders(ctx, bp)
	}

	if _, err = claimBondYield(ctx, h.mgr, msg.TxIn, &nodeAccount, &bp, providerAcc, msg.Amount, 0); err != nil {
		return err
	}

	if err = h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save node account(%s)", nodeAccount.String()))
	}
	if err = h.mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	return nil
}
