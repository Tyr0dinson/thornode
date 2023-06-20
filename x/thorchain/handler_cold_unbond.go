package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	tmtypes "github.com/tendermint/tendermint/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// ColdUnBondHandler a handler to process unbond request
type ColdUnBondHandler struct {
	mgr Manager
}

// NewColdUnBondHandler create new ColdUnBondHandler
func NewColdUnBondHandler(mgr Manager) ColdUnBondHandler {
	return ColdUnBondHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h ColdUnBondHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgColdUnBond)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgColdUnBond",
		"operator", msg.Operator,
		"asset", msg.Asset,
		"amount", msg.Amount)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg cold unbond fail validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("msg cold unbond fail handler", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ColdUnBondHandler) validate(ctx cosmos.Context, msg MsgColdUnBond) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return h.validateV115(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h ColdUnBondHandler) validateV115(ctx cosmos.Context, msg MsgColdUnBond) error {
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

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, msg.Operator)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.Operator))
	}
	provider := bp.Get(msg.Signer)
	if provider.IsEmpty() {
		return fmt.Errorf("bond provider not found")
	}
	if msg.Amount.GT(provider.Bond) {
		return fmt.Errorf("bond provider does not have enough bond to unbond that much")
	}

	// check that the bond has reached maturity
	minMaturity := fetchConfigInt64(ctx, h.mgr, constants.ColdBondMaturity)
	maturity := (provider.LastBond + minMaturity) - ctx.BlockHeight()
	if maturity > 0 {
		return fmt.Errorf("bond has not reached maturity: %d blocks remaining", maturity)
	}

	// Check that unbonding doesn't cause the cold node to go below min CR
	acct, err := h.mgr.Keeper().GetColdAccount(ctx, msg.Operator, msg.Asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get cold account(%s)", msg.Operator))
	}
	vault, err := h.mgr.Keeper().GetVault(ctx, acct.PubKey)
	if err != nil {
		return err
	}
	pool, err := h.mgr.Keeper().GetPool(ctx, acct.Asset)
	if err != nil {
		return err
	}
	minCR := fetchConfigInt64(ctx, h.mgr, constants.MinColdCollateralizationRatio)
	coin := vault.GetCoin(acct.Asset)
	bondValudeInAsset := pool.RuneValueInAsset(common.SafeSub(acct.Bond, msg.Amount))
	maxAssetAmount := common.GetSafeShare(constants.MaxBasisPoints, cosmos.NewUintFromInt64(acct.CollateralizationRatio), bondValudeInAsset)
	if maxAssetAmount.GT(coin.Amount.Add(acct.PendingAsset)) {
		return fmt.Errorf("unbonding this amount would cause the collateralization ratio to drop below safe (%d) values", minCR)
	}

	return nil
}

func (h ColdUnBondHandler) handle(ctx cosmos.Context, msg MsgColdUnBond) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return h.handleV115(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h ColdUnBondHandler) handleV115(ctx cosmos.Context, msg MsgColdUnBond) error {
	acct, err := h.mgr.Keeper().GetColdAccount(ctx, msg.Operator, msg.Asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get cold account(%s)", msg.Operator))
	}
	acct.Bond = common.SafeSub(acct.Bond, msg.Amount)

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, acct.Operator)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", acct.Operator))
	}
	bp.Adjust(h.mgr.GetVersion(), acct.Bond) // re-distribute bond as needed
	bp.UnbondWithHeight(msg.Amount, msg.Signer, ctx.BlockHeight())
	provider := bp.Get(msg.Signer)

	// save changes
	if err := h.mgr.Keeper().SetColdAccount(ctx, acct); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save cold account(%s)", acct.String()))
	}
	if err := h.mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	// refund bond
	hash := tmtypes.Tx(ctx.TxBytes()).Hash()
	txID, err := common.NewTxID(fmt.Sprintf("%X", hash))
	if err != nil {
		return fmt.Errorf("fail to get tx hash: %w", err)
	}

	toAddress, err := common.NewAddress(provider.BondAddress.String())
	if err != nil {
		return fmt.Errorf("fail to parse bond address: %w", err)
	}

	txOutItem := TxOutItem{
		Chain:      common.RuneAsset().Chain,
		ToAddress:  toAddress,
		InHash:     txID,
		Coin:       common.NewCoin(common.RuneAsset(), msg.Amount),
		ModuleName: ColdBondName,
	}
	_, err = h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, txOutItem, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add outbound tx: %w", err)
	}

	return nil
}
