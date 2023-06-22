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

	minCR := fetchConfigInt64(ctx, h.mgr, constants.ColdCollateralizationRatioMin)
	if msg.CollateralizationRatio > 0 && minCR > msg.CollateralizationRatio {
		return fmt.Errorf("collateralization ratio cannot be less than %d", minCR)
	}

	acct, err := h.mgr.Keeper().GetColdAccount(ctx, msg.Operator, msg.Asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get cold account(%s)", msg.Operator))
	}
	// ensure that multiple bonds simultaneous don't trigger the network to
	// send too much in funds
	if !acct.PendingAsset.IsZero() {
		return fmt.Errorf("cannot bond while pending assets is non-zero")
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

	if acct.Status == ColdUnknown {
		if msg.PubKey.IsEmpty() {
			fmt.Errorf("invalid pubkey")
		}
		acct = NewColdAccount(msg.Operator, msg.Asset, msg.PubKey)
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent("new_cold_node",
				cosmos.NewAttribute("operator", msg.Operator.String()),
				cosmos.NewAttribute("pubkey", msg.PubKey.String()),
				cosmos.NewAttribute("asset", msg.Asset.String()),
			))
	}

	// when a bond provider bonding, that individual should pay for gas fees to
	// send more L1 asset to the cold node. So to account for this, we reduce
	// the bond provider's bond
	maxGas, err := h.mgr.GasMgr().GetMaxGas(ctx, acct.Asset.GetChain())
	if err != nil {
		return err
	}
	pool, err := h.mgr.Keeper().GetPool(ctx, acct.Asset)
	if err != nil {
		return err
	}
	maxGasInRune := pool.RuneValueInAsset(maxGas.Amount)
	bondSize := common.SafeSub(msg.Bond.Amount, maxGasInRune)

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, acct.Operator)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.Operator))
	}
	bp.Adjust(h.mgr.GetVersion(), acct.Bond) // re-distribute bond as needed
	if !bp.Has(msg.Signer) {
		bp.Providers = append(bp.Providers, NewBondProvider(msg.Signer))
	}
	bp.BondWithHeight(bondSize, msg.Signer, ctx.BlockHeight())
	acct.Bond = acct.Bond.Add(msg.Bond.Amount)

	// make updates to the acct/bp
	if acct.Operator.Equals(msg.Signer) {
		minCR := fetchConfigInt64(ctx, h.mgr, constants.ColdCollateralizationRatioMin)
		if msg.CollateralizationRatio > 0 && minCR < msg.CollateralizationRatio {
			acct.CollateralizationRatio = msg.CollateralizationRatio
		}
		if msg.OperatorFee > -1 && msg.OperatorFee <= 10000 {
			bp.NodeOperatorFee = cosmos.NewUint(uint64(msg.OperatorFee))
		}
	}

	vault, err := h.mgr.Keeper().GetVault(ctx, acct.PubKey)
	if err != nil {
		return err
	}
	coin := vault.GetCoin(acct.Asset)
	bondValudeInAsset := pool.RuneValueInAsset(acct.Bond)
	maxAssetAmount := common.GetSafeShare(constants.MaxBasisPoints, cosmos.NewUintFromInt64(acct.CollateralizationRatio), bondValudeInAsset)
	coin.Amount = common.SafeSub(maxAssetAmount, coin.Amount.Add(acct.PendingAsset))

	//////////////////////////////////////////////////////////////////////////
	///////////// ensure sent amount isn't trivial and burns gas /////////////
	//////////////////////////////////////////////////////////////////////////
	minOutbound := fetchConfigInt64(ctx, h.mgr, constants.ColdMinOutboundThreshold)
	minOut := maxGas.Amount.MulUint64(uint64(minOutbound))
	if minOut.LT(coin.Amount) {
		return nil
	}
	//////////////////////////////////////////////////////////////////////////

	//////////////////////////////////////////////////////////////////////////
	///////////////// send out layer1 asset to the cold node /////////////////
	//////////////////////////////////////////////////////////////////////////
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
	acct.PendingAsset = acct.PendingAsset.Add(coin.Amount) // track pending asset being sent out
	//////////////////////////////////////////////////////////////////////////

	// save changes
	if err := h.mgr.Keeper().SetColdAccount(ctx, acct); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save cold account(%s)", acct.String()))
	}
	if err := h.mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	return nil
}
