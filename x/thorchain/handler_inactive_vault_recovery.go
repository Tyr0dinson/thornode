package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// InactiveVaultRecoveryHandler handles MsgInactiveVaultRecovery messages
type InactiveVaultRecoveryHandler struct {
	mgr Manager
}

// NewInactiveVaultRecoveryHandler creates a new instance of InactiveVaultRecoveryHandler
func NewInactiveVaultRecoveryHandler(mgr Manager) InactiveVaultRecoveryHandler {
	return InactiveVaultRecoveryHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to execute inactive vault recovery logic
func (h InactiveVaultRecoveryHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgInactiveVaultRecovery)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive msg inactive vault recovery",
		"inactive_vault_pubkey", msg.InactiveVaultPubKey,
		"recover_address", msg.RecoverAddress,
		"recover_amount", msg.RecoverAmount.String(),
		"asset", msg.Asset.String(),
		"to_address", msg.ToAddress,
		"memo", msg.Memo)

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg inactive vault recovery failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg inactive vault recovery", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h InactiveVaultRecoveryHandler) validate(ctx cosmos.Context, msg MsgInactiveVaultRecovery) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.0.0")):
		return h.validateV3_0_0(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h InactiveVaultRecoveryHandler) validateV3_0_0(_ cosmos.Context, msg MsgInactiveVaultRecovery) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Additional validation can be added here
	return nil
}

// handle processes MsgInactiveVaultRecovery by emitting an InactiveVaultRecovery event
// The actual recovery logic is handled by Bifrost when it observes this event
func (h InactiveVaultRecoveryHandler) handle(ctx cosmos.Context, msg MsgInactiveVaultRecovery) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.0.0")):
		return h.handleV3_0_0(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h InactiveVaultRecoveryHandler) handleV3_0_0(ctx cosmos.Context, msg MsgInactiveVaultRecovery) error {
	// The handler's only job is to emit an event for Bifrost to observe
	// It does not contain complex business logic as per the security requirements

	// Create and emit the InactiveVaultRecovery event
	evt := NewEventInactiveVaultRecovery(
		msg.InactiveVaultPubKey,
		msg.RecoverAddress,
		msg.RecoverAmount,
		msg.Asset,
		msg.ToAddress,
		msg.Memo,
	)

	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit inactive vault recovery event: %w", err)
	}

	ctx.Logger().Info("successfully emitted inactive vault recovery event",
		"inactive_vault_pubkey", msg.InactiveVaultPubKey,
		"recover_address", msg.RecoverAddress,
		"recover_amount", msg.RecoverAmount.String(),
		"asset", msg.Asset.String(),
		"to_address", msg.ToAddress,
		"memo", msg.Memo)

	return nil
}
