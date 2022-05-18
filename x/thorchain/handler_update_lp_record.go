package thorchain

import (
	"github.com/blang/semver"

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
		return errWithdrawFailValidation
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
	return &cosmos.Result{}, nil
}
