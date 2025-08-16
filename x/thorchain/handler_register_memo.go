package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// RegisterMemoHandler handles MsgRegisterMemo messages
type RegisterMemoHandler struct {
	mgr Manager
}

// NewRegisterMemoHandler creates a new RegisterMemoHandler
func NewRegisterMemoHandler(mgr Manager) RegisterMemoHandler {
	return RegisterMemoHandler{mgr: mgr}
}

// Run processes the MsgRegisterMemo message
func (h RegisterMemoHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgRegisterMemo)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgRegisterMemo failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("Failed to process memo registration", "error", err)
		return nil, err
	}

	return result, nil
}

// validate performs validation of the register memo message
func (h RegisterMemoHandler) validate(ctx cosmos.Context, msg types.MsgRegisterMemo) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.0.0")):
		return h.validateV3_0_0(ctx, msg)
	default:
		return errInvalidVersion
	}
}

// validateV3_0_0 performs v3.0.0-specific validation
func (h RegisterMemoHandler) validateV3_0_0(ctx cosmos.Context, msg types.MsgRegisterMemo) error {
	// Basic validation
	if err := msg.ValidateBasicEnhanced(); err != nil {
		return err
	}

	// Check if memoless protocol is enabled (check both Mimir and Constants)
	enabled, err := h.mgr.Keeper().GetMimir(ctx, constants.MemolessEnabled.String())
	if err != nil || enabled == -1 {
		// If not set in Mimir or error, check constants as fallback
		enabled = h.mgr.GetConstants().GetInt64Value(constants.MemolessEnabled)
	}
	if enabled == 0 {
		return fmt.Errorf("memoless protocol is disabled")
	}

	// Validate using keeper's comprehensive validation
	keeper, ok := h.mgr.Keeper().(interface {
		ValidateMemoRegistration(ctx cosmos.Context, msg types.MsgRegisterMemo, constantProvider constants.ConstantValues) error
	})
	if ok {
		return keeper.ValidateMemoRegistration(ctx, msg, h.mgr.GetConstants())
	}

	// Fallback to basic validation if keeper doesn't support enhanced validation
	return nil
}

// handle processes the validated message
func (h RegisterMemoHandler) handle(ctx cosmos.Context, msg types.MsgRegisterMemo) (*cosmos.Result, error) {
	// Check for chain halt
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to register memo while THORChain is halted")
	}

	ctx.Logger().Info("Processing memo registration",
		"memo_length", len(msg.Memo),
		"signer", msg.Signer,
		"ttl", msg.Ttl,
		"chain_id", msg.ChainId,
	)

	// Skip memo validation - memoless protocol accepts arbitrary memo content
	// The memo will be validated when it's actually used later

	// Generate reference with collision handling
	reference, err := h.generateReference(ctx, msg.Memo, msg.ChainId)
	if err != nil {
		return nil, fmt.Errorf("failed to generate reference: %w", err)
	}

	// Calculate expiry
	expiresAt := h.calculateExpiry(ctx, msg.Ttl)

	// Create registration
	registration := types.NewRegisteredMemo(
		reference,
		msg.Memo,
		msg.ChainId,
		ctx.BlockHeight(),
		expiresAt,
	)

	// Store registration using keeper
	if err := h.storeRegistration(ctx, registration); err != nil {
		return nil, fmt.Errorf("failed to store registration: %w", err)
	}

	// Update statistics
	h.updateStats(ctx)

	// Emit events
	h.emitEvents(ctx, registration)

	// Record telemetry
	h.recordTelemetry(ctx, msg.ChainId)

	ctx.Logger().Info("Memo registration completed",
		"reference", reference,
		"chain_id", msg.ChainId,
		"expires_at", expiresAt,
	)

	return &cosmos.Result{
		Data:   []byte(reference),
		Events: ctx.EventManager().Events().ToABCIEvents(),
	}, nil
}

// validateMemo validates memo using existing THORChain memo parser
func (h RegisterMemoHandler) validateMemo(ctx cosmos.Context, memo string) error {
	// Use the existing memo parser to validate the memo
	_, err := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), memo)
	if err != nil {
		return fmt.Errorf("memo parsing failed: %w", err)
	}
	return nil
}

// generateReference generates a reference using incremental counter
func (h RegisterMemoHandler) generateReference(ctx cosmos.Context, memo string, chainID string) (string, error) {
	// Use incremental counter approach (NEW ARCHITECTURE)
	keeper, ok := h.mgr.Keeper().(interface {
		GetNextReferenceID(ctx cosmos.Context, chainID string) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("keeper does not support incremental counter operations")
	}

	// Get next sequential reference ID
	reference, err := keeper.GetNextReferenceID(ctx, chainID)
	if err != nil {
		return "", fmt.Errorf("failed to generate incremental reference: %w", err)
	}

	return reference, nil
}

// calculateExpiry calculates the expiry block height
func (h RegisterMemoHandler) calculateExpiry(ctx cosmos.Context, ttl int64) int64 {
	if ttl <= 0 {
		// Use default TTL from constants
		defaultTTL := h.mgr.GetConstants().GetInt64Value(constants.MemolessTTL)
		if defaultTTL <= 0 {
			defaultTTL = 144000 // Default 24 hours in blocks (5 second blocks)
		}
		ttl = defaultTTL
	}

	return ctx.BlockHeight() + ttl
}

// storeRegistration stores the memo registration using the keeper
func (h RegisterMemoHandler) storeRegistration(ctx cosmos.Context, registration types.RegisteredMemo) error {
	keeper, ok := h.mgr.Keeper().(interface {
		SetRegisteredMemo(ctx cosmos.Context, memo types.RegisteredMemo) error
	})
	if !ok {
		return fmt.Errorf("keeper does not support memoless operations")
	}

	return keeper.SetRegisteredMemo(ctx, registration)
}

// updateStats updates memoless protocol statistics
func (h RegisterMemoHandler) updateStats(ctx cosmos.Context) {
	keeper, ok := h.mgr.Keeper().(interface {
		IncrementRegistrations(ctx cosmos.Context) error
	})
	if ok {
		if err := keeper.IncrementRegistrations(ctx); err != nil {
			ctx.Logger().Error("Failed to update memoless stats", "error", err)
		}
	}
}

// emitEvents emits blockchain events for the registration
func (h RegisterMemoHandler) emitEvents(ctx cosmos.Context, registration types.RegisteredMemo) {
	ctx.EventManager().EmitEvent(
		cosmos.NewEvent(
			"memo_registered",
			cosmos.NewAttribute("reference", registration.Reference),
			cosmos.NewAttribute("chain_id", registration.ChainId),
			cosmos.NewAttribute("expires_at", fmt.Sprintf("%d", registration.ExpiresAt)),
			cosmos.NewAttribute("registered_at", fmt.Sprintf("%d", registration.RegisteredAt)),
			cosmos.NewAttribute("status", registration.Status),
		),
	)

	// Emit module-specific event for monitoring
	ctx.EventManager().EmitEvent(
		cosmos.NewEvent(
			"memoless_protocol",
			cosmos.NewAttribute("action", "register"),
			cosmos.NewAttribute("reference", registration.Reference),
			cosmos.NewAttribute("chain_id", registration.ChainId),
		),
	)
}

// recordTelemetry records telemetry metrics
func (h RegisterMemoHandler) recordTelemetry(ctx cosmos.Context, chainID string) {
	// Record registration counter
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "memoless", "registrations"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain_id", chainID),
		},
	)

	// Record current active memos gauge
	keeper, ok := h.mgr.Keeper().(interface {
		GetActiveMemoCount(ctx cosmos.Context) int64
	})
	if ok {
		activeCount := keeper.GetActiveMemoCount(ctx)
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "memoless", "active_memos"},
			float32(activeCount),
			[]metrics.Label{
				telemetry.NewLabel("chain_id", chainID),
			},
		)
	}
}
