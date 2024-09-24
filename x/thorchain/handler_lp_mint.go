package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// LpMintHandler is handler to process MsgLpMint
type LpMintHandler struct {
	mgr Manager
}

// NewLpMintHandler create a new instance of LpMintHandler
func NewLpMintHandler(mgr Manager) LpMintHandler {
	return LpMintHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for LpMintHandler
func (h LpMintHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgLpMint)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgLpMint failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgLpMint", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h LpMintHandler) validate(ctx cosmos.Context, msg MsgLpMint) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h LpMintHandler) validateV137(ctx cosmos.Context, msg MsgLpMint) error {
	return msg.ValidateBasic()
}

func (h LpMintHandler) handle(ctx cosmos.Context, msg MsgLpMint) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return errBadVersion
	}
}

// handle process MsgLpMint
func (h LpMintHandler) handleV137(ctx cosmos.Context, msg MsgLpMint) error {
	_, err := h.mgr.LpTokenManager().Mint(ctx, msg.Asset, msg.RuneAddress, msg.AssetAddress, msg.Amount, msg.Recipient, msg.Tx.ID)
	if err != nil {
		ctx.Logger().Error("fail to handle LP Token Mint", "error", err)
		return err
	}
	return nil
}
