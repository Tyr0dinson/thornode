package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"github.com/hashicorp/go-multierror"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// UpdateLPRecordHandler processes requests to update the addresses of an LP record.
type UpdateLPRecordHandler struct {
	mgr Manager
}

// NewUpdateLPRecordHandler creates a new instance of UpdateLPRecordHandler, which updates addresses of LP records
func NewUpdateLPRecordHandler(mgr Manager) UpdateLPRecordHandler {
	return UpdateLPRecordHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of updating an LP record
func (h UpdateLPRecordHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgUpdateLpRecord)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgUpdateLP", "pool", msg.PoolAsset, "new address", msg.NewAddress)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgUpdateLP failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg update LP request", "error", err)
		return nil, err
	}
	return result, err
}

func (h UpdateLPRecordHandler) validate(ctx cosmos.Context, msg MsgUpdateLpRecord) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV90(ctx, msg)
	}
	return errBadVersion
}

func (h UpdateLPRecordHandler) validateV90(ctx cosmos.Context, msg MsgUpdateLpRecord) error {
	if err := msg.ValidateBasic(); err != nil {
		return errUpdateLpRecordFailValidation
	}

	if isChainHalted(ctx, h.mgr, msg.Tx.Chain) {
		return fmt.Errorf("unable to update LP record while chain is halted")
	}

	// Check update memo is enabled for this chain
	mimirKey := fmt.Sprintf("EnableUpdateMemo%s", msg.Tx.Chain)
	updateEnabled, err := h.mgr.Keeper().GetMimir(ctx, mimirKey)
	if err == nil && updateEnabled != 1 {
		return fmt.Errorf("update memo not enabled for this chain")
	}

	// Can't request with synthetic asset
	if msg.PoolAsset.IsSyntheticAsset() {
		ctx.Logger().Error("asset cannot be synth", "error", errUpdateLpRecordFailValidation)
		return errUpdateLpRecordFailValidation
	}

	// Ensure pool exists
	poolExists := h.mgr.Keeper().PoolExist(ctx, msg.PoolAsset)
	if !poolExists {
		return fmt.Errorf("pool doesn't exist")
	}

	// Validate signer has an LP position in requested Pool
	lp, err := h.mgr.Keeper().GetLiquidityProvider(ctx, msg.PoolAsset, msg.Tx.FromAddress)
	if err != nil {
		return multierror.Append(errFailGetLiquidityProvider, err)
	}

	if lp.Units.IsZero() && (lp.PendingAsset.IsZero() && lp.PendingRune.IsZero()) {
		return fmt.Errorf("LP record has no units, skipping")
	}

	// Sanity check: make sure the tx from address matches the LP record
	if msg.Tx.Chain.IsTHORChain() && !msg.Tx.FromAddress.Equals(lp.RuneAddress) {
		return fmt.Errorf("From address doesn't match LP record rune address")
	}

	if !msg.Tx.Chain.IsTHORChain() && !msg.Tx.FromAddress.Equals(lp.AssetAddress) {
		return fmt.Errorf("From address doesn't match LP record asset address")
	}

	return nil
}

func (h UpdateLPRecordHandler) handle(ctx cosmos.Context, msg MsgUpdateLpRecord) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("0.1.0")):
		return h.handleV90(ctx, msg)
	}
	return nil, errBadVersion
}

func (h UpdateLPRecordHandler) handleV90(ctx cosmos.Context, msg MsgUpdateLpRecord) (*cosmos.Result, error) {
	lp, err := h.mgr.Keeper().GetLiquidityProvider(ctx, msg.PoolAsset, msg.Tx.FromAddress)
	if err != nil {
		return &cosmos.Result{}, multierror.Append(errFailGetLiquidityProvider, err)
	}

	if msg.NewAddress.IsChain(common.THORChain) {
		lp.RuneAddress = msg.NewAddress
	} else if msg.NewAddress.GetChain().Equals(lp.Asset.GetChain()) {
		lp.AssetAddress = msg.NewAddress
	}

	h.mgr.Keeper().SetLiquidityProvider(ctx, lp)

	// TODO: emit update address event for midgard

	return &cosmos.Result{}, nil
}
