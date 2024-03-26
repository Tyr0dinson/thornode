package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// BondAllocationHandler a handler to process MsgNetworkFee messages
type BondAllocationHandler struct {
	mgr Manager
}

// NewBondAllocationHandler create a new instance of network fee handler
func NewBondAllocationHandler(mgr Manager) BondAllocationHandler {
	return BondAllocationHandler{mgr: mgr}
}

// Run is the main entry point for network fee logic
func (h BondAllocationHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBondAllocation)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgBondAllocation failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgBondAllocation", "error", err)
	}
	return result, err
}

func (h BondAllocationHandler) validate(ctx cosmos.Context, msg MsgBondAllocation) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.131.0")):
		return h.validateV131(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h BondAllocationHandler) validateV131(ctx cosmos.Context, msg MsgBondAllocation) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

// handle process MsgBondAllocation
func (h BondAllocationHandler) handle(ctx cosmos.Context, msg MsgBondAllocation) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.131.0")):
		return h.handleV131(ctx, msg)
	default:
		return nil, errBadVersion
	}
}

// handle process MsgBondAllocation
func (h BondAllocationHandler) handleV131(ctx cosmos.Context, msg MsgBondAllocation) (*cosmos.Result, error) {

	return &cosmos.Result{}, nil
}
