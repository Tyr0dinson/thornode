package blocktime

import (
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cosmos/cosmos-sdk/telemetry"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// Hook writes the controller's wait into the in-process CometBFT's consensus
// config. CometBFT reads TimeoutCommit in updateToState, right after the app's
// Commit returns and on the same goroutine, so OnCommit must be called
// synchronously from Commit: anything asynchronous would race that read.
type Hook struct {
	binding atomic.Pointer[binding]
	ctrl    Controller
	engaged bool
	clamped int64 // last out-of-range mimir value logged, so each is logged once
}

type binding struct {
	cfg  *cmtcfg.ConsensusConfig
	base time.Duration // timeout_commit from config.toml
}

// Attach binds the hook to the consensus config CometBFT runs with. It may be
// called while consensus is running; the hook stays inert until it is.
func (h *Hook) Attach(cfg *cmtcfg.ConsensusConfig) {
	if cfg == nil {
		return
	}
	h.binding.Store(&binding{cfg: cfg, base: cfg.TimeoutCommit})
}

// Attached reports whether a consensus config is bound.
func (h *Hook) Attached() bool { return h.binding.Load() != nil }

// OnCommit sets the commit wait for the next height from the BLOCKTIMEMS
// mimir (targetMs) and the header time of the block being committed. While
// the mimir is unset (targetMs <= 0) it never touches TimeoutCommit; once it
// is unset after being set, config.toml's timeout_commit is restored. A
// positive target is clamped to [BlockTimeMsMin, BlockTimeMsMax] even though
// MsgMimir validation enforces the same range: a value can reach the store
// without passing it (a vote cast before the check existed reaching
// supermajority later, a migration, a direct store write).
func (h *Hook) OnCommit(logger log.Logger, targetMs int64, header time.Time) {
	b := h.binding.Load()
	if b == nil {
		return
	}
	if !enabled {
		// never engaged, so TimeoutCommit is never touched
		return
	}
	if targetMs <= 0 {
		if h.engaged {
			h.engaged = false
			b.cfg.TimeoutCommit = b.base
			logger.Info("BLOCKTIMEMS unset, restored timeout_commit", "timeout_commit", b.base)
		}
		return
	}

	if bounded := ClampTargetMs(targetMs); bounded != targetMs {
		if h.clamped != targetMs {
			h.clamped = targetMs
			logger.Error("BLOCKTIMEMS out of range, clamped", "mimir", targetMs, "target_ms", bounded)
		}
		targetMs = bounded
	}
	target := time.Duration(targetMs) * time.Millisecond
	if !h.engaged {
		h.engaged = true
		h.ctrl.Reset(target)
	}
	wait, ok := h.ctrl.Next(target, header)
	if !ok {
		logger.Info("block interval out of range, using configured timeout_commit",
			"target", target, "timeout_commit", b.base)
		wait = b.base
	}
	b.cfg.TimeoutCommit = wait
	logger.Debug("commit wait", "target", target, "interval", h.ctrl.Interval(), "wait", wait)
	telemetry.SetGauge(float32(wait.Milliseconds()), "thornode", "blocktime", "commit_wait_ms")
	telemetry.SetGauge(float32(targetMs), "thornode", "blocktime", "target_ms")
}

// ClampTargetMs bounds a positive BLOCKTIMEMS value to
// [BlockTimeMsMin, BlockTimeMsMax].
func ClampTargetMs(ms int64) int64 {
	return min(max(ms, constants.BlockTimeMsMin), constants.BlockTimeMsMax)
}

// enabled is constants.BlockTimeMimirEnabled; tests of the controller on
// networks where it is off override it.
var enabled = constants.BlockTimeMimirEnabled

var std Hook

// Attach binds the process-wide hook; see Hook.Attach.
func Attach(cfg *cmtcfg.ConsensusConfig) { std.Attach(cfg) }

// Active reports whether the process-wide hook is bound and BLOCKTIMEMS is
// enabled on this network, i.e. whether OnCommit can do anything.
func Active() bool { return enabled && std.Attached() }

// OnCommit drives the process-wide hook; see Hook.OnCommit.
func OnCommit(logger log.Logger, targetMs int64, header time.Time) {
	std.OnCommit(logger, targetMs, header)
}
