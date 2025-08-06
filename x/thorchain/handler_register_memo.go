package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
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
	case version.GTE(semver.MustParse("1.0.0")):
		return h.validateV1(ctx, msg)
	default:
		return errInvalidVersion
	}
}

// validateV1 performs v1-specific validation
func (h RegisterMemoHandler) validateV1(ctx cosmos.Context, msg types.MsgRegisterMemo) error {
	// Basic validation
	if err := msg.ValidateBasicEnhanced(); err != nil {
		return err
	}

	// Check if memoless protocol is enabled
	enabled := h.mgr.GetConstants().GetInt64Value(constants.MemolessEnabled)
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
		"ttl", msg.TTL,
		"chain_id", msg.ChainID,
	)

	// Validate memo format using existing memo parser
	if err := h.validateMemo(ctx, msg.Memo); err != nil {
		return nil, fmt.Errorf("invalid memo format: %w", err)
	}

	// Generate reference with collision handling
	reference, err := h.generateReference(ctx, msg.Memo, msg.ChainID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate reference: %w", err)
	}

	// Calculate expiry
	expiresAt := h.calculateExpiry(ctx, msg.TTL)

	// Create registration
	registration := types.NewRegisteredMemo(
		reference,
		msg.Memo,
		msg.ChainID,
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
	h.recordTelemetry(ctx, msg.ChainID)

	ctx.Logger().Info("Memo registration completed",
		"reference", reference,
		"chain_id", msg.ChainID,
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
	_, err := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), memo, common.Tx{}, h.mgr.GetVersion())
	if err != nil {
		return fmt.Errorf("memo parsing failed: %w", err)
	}
	return nil
}

// generateReference generates a reference with collision handling
func (h RegisterMemoHandler) generateReference(ctx cosmos.Context, memo string, chainID string) (string, error) {
	// Generate base reference
	baseReference := keeperv1.GenerateReference(memo, ctx.BlockHeight())

	// Check for collision
	keeper, ok := h.mgr.Keeper().(interface {
		HasRegisteredMemo(ctx cosmos.Context, reference string) bool
		ResolveCollision(ctx cosmos.Context, memo string, blockHeight int64, constantProvider constants.ConstantValues) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("keeper does not support memoless operations")
	}

	if !keeper.HasRegisteredMemo(ctx, baseReference) {
		return baseReference, nil
	}

	// Handle collision
	ctx.Logger().Debug("Reference collision detected, resolving", "reference", baseReference)
	
	resolvedReference, err := keeper.ResolveCollision(ctx, memo, ctx.BlockHeight(), h.mgr.GetConstants())
	if err != nil {
		return "", fmt.Errorf("collision resolution failed: %w", err)
	}

	return resolvedReference, nil
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
			cosmos.NewAttribute("chain_id", registration.ChainID),
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
			cosmos.NewAttribute("chain_id", registration.ChainID),
		),
	)
}

// recordTelemetry records telemetry metrics
func (h RegisterMemoHandler) recordTelemetry(ctx cosmos.Context, chainID string) {
	// Record registration counter
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "memoless", "registrations"},
		1,
		[]telemetry.Label{
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
			[]telemetry.Label{
				telemetry.NewLabel("chain_id", chainID),
			},
		)
	}
}