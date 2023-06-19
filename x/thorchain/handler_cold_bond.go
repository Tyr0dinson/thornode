package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	tmtypes "github.com/tendermint/tendermint/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// ColdBondHandler a handler to process bond
type ColdBondHandler struct {
	mgr Manager
}

// NewColdBondHandler create new ColdBondHandler
func NewColdBondHandler(mgr Manager) ColdBondHandler {
	return ColdBondHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h ColdBondHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgColdBond)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgColdBond",
		"operator", msg.Operator,
		"bond", msg.Bond,
		"signer", msg.Signer)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg cold bond fail validation", "error", err)
		return nil, err
	}

	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg cold bond", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ColdBondHandler) validate(ctx cosmos.Context, msg MsgColdBond) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return h.validateV115(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h ColdBondHandler) validateV115(ctx cosmos.Context, msg MsgColdBond) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if fetchConfigInt64(ctx, h.mgr, constants.PauseBond) > 0 {
		return fmt.Errorf("bonding has been paused")
	}

	// sanity check, make sure our operator is not a node account
	na, _ := h.mgr.Keeper().GetNodeAccount(ctx, msg.Operator)
	if !na.IsEmpty() {
		return fmt.Errorf("cannot cold bond to a node account")
	}

	minCR := fetchConfigInt64(ctx, h.mgr, constants.MinColdCollateralizationRatio)
	if minCR > msg.CollateralizationRatio {
		return fmt.Errorf("collateralization cannot exceed limits")
	}

	return nil
}

func (h ColdBondHandler) handle(ctx cosmos.Context, msg MsgColdBond) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return h.handleV115(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h ColdBondHandler) handleV115(ctx cosmos.Context, msg MsgColdBond) error {
	acct, err := h.mgr.Keeper().GetColdAccount(ctx, msg.Operator, msg.Asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get cold account(%s)", msg.Operator))
	}

	if acct.Operator.Equals(msg.Signer) {
		minCR := fetchConfigInt64(ctx, h.mgr, constants.MinColdCollateralizationRatio)
		if minCR < msg.CollateralizationRatio {
			acct.CollateralizationRatio = msg.CollateralizationRatio
		}
	}

	if acct.Status == ColdUnknown {
		acct = NewColdAccount(msg.Operator, msg.Asset, msg.PubKey)
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent("new_cold_node",
				cosmos.NewAttribute("operator", msg.Operator.String()),
				cosmos.NewAttribute("pubkey", msg.PubKey.String()),
				cosmos.NewAttribute("asset", msg.Asset.String()),
			))
	}

	// Get the bond providers initially in order before adding the msg.Bond to the original bond.
	bp, err := h.mgr.Keeper().GetBondProviders(ctx, acct.Operator)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.Operator))
	}

	// Re-distribute current bond if needed
	bp.Adjust(h.mgr.GetVersion(), acct.Bond)

	acct.Bond = acct.Bond.Add(msg.Bond)
	if !bp.Has(msg.Signer) {
		bp.Providers = append(bp.Providers, NewBondProvider(msg.Signer))
	}
	bp.Bond(msg.Bond, msg.Signer)

	// hard coded operator fee
	bp.NodeOperatorFee = cosmos.NewUint(2000)

	if err := h.mgr.Keeper().SetColdAccount(ctx, acct); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save cold account(%s)", acct.String()))
	}

	if err := h.mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	vault, err := h.mgr.Keeper().GetVault(ctx, acct.PubKey)
	if err != nil {
		return err
	}
	coin := vault.GetCoin(acct.Asset)
	pool, err := h.mgr.Keeper().GetPool(ctx, acct.Asset)
	if err != nil {
		return err
	}

	bondValudeInAsset := pool.RuneValueInAsset(acct.Bond)
	maxAssetAmount := common.GetSafeShare(cosmos.NewUint(10_000), cosmos.NewUint(uint64(acct.CollateralizationRatio)), bondValudeInAsset)
	coin.Amount = common.SafeSub(maxAssetAmount, coin.Amount)

	//////////////////////////////////////////////////////////////////////////
	///////////// ensure sent amount isn't trivial and burns gas /////////////
	//////////////////////////////////////////////////////////////////////////
	maxGas, err := h.mgr.GasMgr().GetMaxGas(ctx, acct.Asset.GetChain())
	if err != nil {
		return err
	}
	minOutbound := fetchConfigInt64(ctx, h.mgr, constants.MinOutboundForCold)
	minOut := maxGas.Amount.MulUint64(uint64(minOutbound))
	if minOut.LT(coin.Amount) {
		return nil
	}
	//////////////////////////////////////////////////////////////////////////

	// send L1 to operator
	hash := tmtypes.Tx(ctx.TxBytes()).Hash()
	txID, err := common.NewTxID(fmt.Sprintf("%X", hash))
	if err != nil {
		return fmt.Errorf("fail to get tx hash: %w", err)
	}
	toAddress, err := acct.PubKey.GetAddress(acct.Asset.GetChain())
	if err != nil {
		return err
	}
	txOutItem := TxOutItem{
		Chain:     acct.Asset.GetChain(),
		ToAddress: toAddress,
		InHash:    txID,
		Coin:      coin,
	}
	_, err = h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, txOutItem, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add outbound tx: %w", err)
	}

	return nil
}
