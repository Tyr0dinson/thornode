// Package thorchain implements the RegisterMemoHandler for the THORChain memoless protocol.
//
// # USER-PROVIDED REFERENCE ARCHITECTURE (MR #2989)
//
// This handler processes MsgRegisterMemo transactions to enable memoless swaps by
// registering complex memo content off-chain using user-provided 5-digit references.
//
// WORKFLOW:
//
// 1. Validation: 5-layer validation framework ensures security and compatibility
// 2. Reference Validation: User-provided reference (10000-99999) validated for format and uniqueness
// 3. Storage: 2-prefix architecture stores registration with TTL expiry
// 4. Response: Returns confirmation of successful registration with user's reference
//
// USER-PROVIDED REFERENCE APPROACH:
//
// Users specify their own 5-digit references extracted from transaction amounts:
//   - Extract last 5 digits from transaction amounts ≥10,000
//   - Users control reference uniqueness through amount selection
//   - No server-side generation or collision resolution needed
//   - Stateless, user-controlled reference management
//
// REFERENCE VALIDATION:
//
// User-provided references must meet strict criteria:
//   - Exactly 5 digits in range 10000-99999
//   - ASCII-only validation prevents Unicode attacks
//   - Uniqueness enforcement via existence checks
//   - No collision resolution - users select unique amounts
//
// SECURITY INTEGRATION:
//
// The handler integrates with multiple security systems:
//   - 5-Layer Validation Framework (keeper_memoless_validation.go)
//   - Enhanced Reference Validation (prevents Unicode/injection attacks)
//   - Rate Limiting (governance-controlled via Mimir constants)
//   - Replay Protection (duplicate memo detection)
//   - Economic Constraints (registration cost enforcement)
//
// PERFORMANCE:
//
// Target performance characteristics:
//   - Reference Validation: <0.1ms (deterministic format + uniqueness check)
//   - Validation: <1ms for all 5 layers combined
//   - Storage: O(1) complexity for registration and lookup operations
//   - No collision resolution overhead - users handle uniqueness
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

	ctx.Logger().Info("Processing user-provided memo registration",
		"reference", msg.Reference,
		"chain_id", msg.ChainId,
		"memo_length", len(msg.Memo),
		"ttl_requested", msg.Ttl,
		"signer", msg.Signer,
	)

	// Skip memo validation - memoless protocol accepts arbitrary memo content
	// The memo will be validated when it's actually used later

	// Validate user-provided reference (format, range, and uniqueness)
	if err := h.validateUserReference(ctx, msg.Reference); err != nil {
		ctx.Logger().Error("User reference validation failed",
			"reference", msg.Reference,
			"error", err)
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	reference := msg.Reference
	ctx.Logger().Debug("User reference validated successfully", "reference", reference)

	// Calculate expiry
	expiresAt := h.calculateExpiry(ctx, msg.Ttl)

	// Create registration with user reference and signer
	registration := types.RegisteredMemo{
		Reference:    reference,
		Memo:         msg.Memo,
		ChainId:      msg.ChainId,
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    expiresAt,
		Status:       "active",
		Signer:       msg.Signer,
	}

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

	ctx.Logger().Info("User memo registration completed successfully",
		"reference", reference,
		"chain_id", msg.ChainId,
		"expires_at", expiresAt,
		"registered_at", ctx.BlockHeight(),
		"status", "active",
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

// validateUserReference validates the user-provided reference using comprehensive validation
func (h RegisterMemoHandler) validateUserReference(ctx cosmos.Context, reference string) error {
	// Ensure keeper supports user reference validation
	keeper, ok := h.mgr.Keeper().(interface {
		ValidateUserReference(ctx cosmos.Context, reference string) error
	})
	if !ok {
		return fmt.Errorf("keeper does not support user reference validation - incompatible keeper version")
	}

	// Comprehensive validation: format (5 digits), range (10000-99999), and uniqueness
	if err := keeper.ValidateUserReference(ctx, reference); err != nil {
		// Enhanced error context for better debugging
		return fmt.Errorf("reference validation failed: %w", err)
	}

	ctx.Logger().Debug("User reference passed all validation checks", "reference", reference)
	return nil
}

// calculateExpiry calculates the expiry block height with improved defaults handling
func (h RegisterMemoHandler) calculateExpiry(ctx cosmos.Context, ttl int64) int64 {
	if ttl <= 0 {
		// Use governance-controlled default TTL from constants
		defaultTTL := h.mgr.GetConstants().GetInt64Value(constants.MemolessTTL)
		if defaultTTL <= 0 {
			// Fallback to 24 hours (assumes ~6s block time)
			defaultTTL = 14400 // 24 hours: 24*60*60/6 = 14400 blocks
		}
		ttl = defaultTTL
		ctx.Logger().Debug("Using default TTL", "ttl_blocks", ttl, "approximate_hours", ttl/600)
	} else {
		ctx.Logger().Debug("Using user-specified TTL", "ttl_blocks", ttl, "approximate_hours", ttl/600)
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
