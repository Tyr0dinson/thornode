package thorchain

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// runCircuitBreakers runs registered circuit breaker invariants at configurable
// intervals and automatically pauses the chain when insolvency beyond a
// configurable tolerance is detected.
func runCircuitBreakers(ctx cosmos.Context, mgr Manager) {
	keeper := mgr.Keeper()

	// Read global interval from Mimir; disabled when <= 0
	globalInterval, err := keeper.GetMimir(ctx, constants.MimirKeyInvCheckInterval)
	if err != nil {
		ctx.Logger().Error("fail to get InvCheckInterval mimir", "error", err)
		return
	}
	if globalInterval <= 0 {
		return
	}

	blockHeight := ctx.BlockHeight()

	for _, route := range keeper.CircuitBreakerRoutes() {
		// Determine effective interval: per-invariant override or global
		interval := globalInterval
		perInvInterval, err := keeper.GetMimirWithRef(ctx, constants.MimirTemplateInvInterval, route.Route)
		if err != nil {
			ctx.Logger().Error("fail to get per-invariant interval mimir", "error", err, "invariant", route.Route)
		} else if perInvInterval >= 0 {
			interval = perInvInterval
		}

		// Skip if disabled or not time to check
		if interval <= 0 || blockHeight%interval != 0 {
			continue
		}

		// Read tolerance (BPS). Default to 0 until production observations
		// provide enough data to select an explicit per-route threshold.
		toleranceBps, err := keeper.GetMimirWithRef(ctx, constants.MimirTemplateInvTolerance, route.Route)
		if err != nil {
			ctx.Logger().Error("fail to get per-invariant tolerance mimir", "error", err, "invariant", route.Route)
		}
		if toleranceBps < 0 {
			toleranceBps = 0
		}

		// Read auto-halt flag; default disabled when not set or <= 0.
		autoHalt, err := keeper.GetMimirWithRef(ctx, constants.MimirTemplateInvAutoHalt, route.Route)
		if err != nil {
			ctx.Logger().Error("fail to get per-invariant auto-halt mimir", "error", err, "invariant", route.Route)
		}

		// Run the circuit breaker check
		check := route.Invariant(ctx)

		// Insolvency in basis points relative to expected. Solvent and
		// overfunded routes return zero; binary invariants with no meaningful
		// ratio are signalled via Broken instead.
		deviationBps := common.CalculateInsolvencyBps(check.Actual, check.Expected)

		// Broken and Indeterminate bypass tolerance so the finding is always
		// reported; only Broken and beyond-tolerance insolvency are
		// halt-eligible. The comparison stays in arbitrary precision so
		// extreme divergence cannot overflow into a silent skip.
		trigger, halt := circuitBreakerAction(check, deviationBps, toleranceBps, autoHalt)
		if !trigger {
			continue
		}

		// The event and log carry int64 BPS — saturate rather than wrap
		// negative or panic on conversion.
		deviationBpsInt := saturateInt64(deviationBps)

		halted := halt

		// Emit circuit breaker event. Messages are capped so systemic
		// corruption cannot bloat block results with one attribute per
		// offending record.
		evt := NewEventCircuitBreaker(
			route.Route,
			check.Asset,
			check.Actual,
			check.Expected,
			deviationBpsInt,
			toleranceBps,
			halted,
			check.Broken,
			check.Indeterminate,
			capMessages(check.Msgs, maxCircuitBreakerEventMessages),
		)
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit circuit breaker event", "error", err)
		}

		ctx.Logger().Error("circuit breaker triggered",
			"invariant", route.Route,
			"asset", check.Asset,
			"actual", check.Actual.String(),
			"expected", check.Expected.String(),
			"deviation_bps", deviationBpsInt,
			"tolerance_bps", toleranceBps,
			"broken", check.Broken,
			"indeterminate", check.Indeterminate,
			"halted", halted,
			"message_count", len(check.Msgs),
		)

		if halted {
			// Keep a persistent finding halted through its next scheduled check.
			// Otherwise an interval longer than the default pause would create an
			// unprotected gap before the breaker can run again.
			pauseUntil := calculateCircuitBreakerPauseUntil(blockHeight, interval)

			// Take the max with any existing pause height. The dedicated key
			// cannot be shortened by node pause/resume messages.
			key := constants.MimirKeyCircuitBreakerPauseGlobal
			currentPause, err := keeper.GetMimir(ctx, key)
			if err != nil {
				ctx.Logger().Error("fail to get CircuitBreakerPauseGlobal", "error", err)
			}
			if currentPause > pauseUntil {
				pauseUntil = currentPause
			}

			// The system write starts a new recovery decision. Discard node
			// votes retained from an earlier recovery so they cannot be reused
			// to clear this automated halt without a fresh supermajority.
			keeper.DeleteNodeMimirs(ctx, key)
			keeper.SetMimir(ctx, key, pauseUntil)

			mimirEvent := NewEventSetMimir(strings.ToUpper(key), strconv.FormatInt(pauseUntil, 10))
			if err := mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
				ctx.Logger().Error("fail to emit set_mimir event", "error", err)
			}

			ctx.Logger().Error(fmt.Sprintf("circuit breaker halted chain via %s=%d", key, pauseUntil),
				"invariant", route.Route,
			)
		}
	}
}

// circuitBreakerAction decides whether a check result produces a finding
// and whether that finding halts the chain. Broken (a provably violated
// invariant) and Indeterminate (state the check could not read or evaluate)
// both bypass tolerance and always produce a finding. Only Broken and
// beyond-tolerance insolvency are halt-eligible: the system itself
// tolerates unreadable records, so halting on Indeterminate alone would
// make the breaker stricter than the system it protects.
// toleranceBps must be non-negative (the caller clamps it): a negative value
// would convert to a huge unsigned tolerance and disable numeric halts.
func circuitBreakerAction(check common.CircuitBreakerCheck, deviationBps cosmos.Uint, toleranceBps, autoHalt int64) (trigger, halt bool) {
	deviationExceeds := deviationBps.GT(cosmos.NewUint(uint64(toleranceBps)))
	trigger = check.Broken || check.Indeterminate || deviationExceeds
	halt = autoHalt > 0 && (check.Broken || deviationExceeds)
	return trigger, halt
}

// maxCircuitBreakerEventMessages bounds how many finding messages a single
// circuit_breaker event carries; the full count is logged separately.
const maxCircuitBreakerEventMessages = 20

// capMessages truncates msgs to at most max entries, appending a summary of
// how many were omitted, so mass corruption cannot bloat block results with
// one event attribute per offending record.
func capMessages(msgs []string, max int) []string {
	// at max+1 the summary line would replace exactly one message, losing
	// information while saving nothing — pass through instead, which also
	// keeps the omitted count always plural
	if len(msgs) <= max+1 {
		return msgs
	}
	capped := make([]string, 0, max+1)
	capped = append(capped, msgs[:max]...)
	capped = append(capped, fmt.Sprintf("%d more messages omitted", len(msgs)-max))
	return capped
}

// saturateInt64 converts an unsigned BPS value for int64 event fields,
// saturating at MaxInt64 instead of wrapping negative or panicking.
func saturateInt64(u cosmos.Uint) int64 {
	if u.GT(cosmos.NewUint(uint64(math.MaxInt64))) {
		return math.MaxInt64
	}
	return int64(u.Uint64())
}

// calculateCircuitBreakerPauseUntil returns a pause height that covers both
// the minimum recovery window and the effective interval until the next check.
// Saturation prevents a governance-supplied interval from wrapping the height.
func calculateCircuitBreakerPauseUntil(blockHeight, interval int64) int64 {
	pauseBlocks := constants.CircuitBreakerPauseBlocks
	if interval > pauseBlocks {
		pauseBlocks = interval
	}
	if blockHeight > math.MaxInt64-pauseBlocks {
		return math.MaxInt64
	}
	return blockHeight + pauseBlocks
}
