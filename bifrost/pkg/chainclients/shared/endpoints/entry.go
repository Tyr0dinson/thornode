package endpoints

import (
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

// Entry tracks the health of a single endpoint. All methods are safe for concurrent use.
//
// Two independent counters feed eviction and never reset each other:
//   - inline errors: consecutive transport/HTTP failures reported by the routers. An
//     inline success resets them; two failures more than error_window apart restart
//     the run.
//   - probe strikes: consecutive failed, lagging, or minority-hash health probes. Only
//     a passing, non-lagging probe resets them, so a responsive-but-stale endpoint is
//     still evicted.
type Entry struct {
	Name     string
	Priority int

	mu  sync.Mutex
	now func() time.Time

	state          State
	disabledReason string

	inlineErrors int
	lastInlineAt time.Time
	probeStrikes int

	recoveryChecks int
	cooldownUntil  time.Time

	identity         string
	identityVerified bool
	identityRequired bool

	lastHeight       int64
	lastProbeLatency time.Duration
	lastProbeAt      time.Time
	lastErrorKind    ErrorKind
	nextRecoveryAt   time.Time
	nonHealthySince  time.Time
	integrityEvicted bool
}

// Snapshot is a read-only copy of an Entry's state for status output.
type Snapshot struct {
	Name             string `json:"name"`
	Priority         int    `json:"priority"`
	State            string `json:"state"`
	Active           bool   `json:"active"`
	Height           int64  `json:"height,omitempty"`
	ProbeLatencyMs   int64  `json:"probe_latency_ms,omitempty"`
	InCooldown       bool   `json:"in_cooldown,omitempty"`
	IdentityVerified bool   `json:"identity_verified"`
	InlineErrors     int    `json:"inline_errors,omitempty"`
	ProbeStrikes     int    `json:"probe_strikes,omitempty"`
	LastErrorKind    string `json:"last_error_kind,omitempty"`
	DisabledReason   string `json:"disabled_reason,omitempty"`
}

// NewEntry returns a Healthy entry.
func NewEntry(name string, priority int) *Entry {
	return &Entry{Name: name, Priority: priority, now: time.Now, state: StateHealthy}
}

// SetClock replaces the time source (tests only).
func (e *Entry) SetClock(now func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = now
}

// RequireIdentity makes identity verification a precondition for Healthy.
func (e *Entry) RequireIdentity(required bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.identityRequired = required
}

// State returns the current state.
func (e *Entry) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// Eligible reports whether readers may use this endpoint right now.
func (e *Entry) Eligible() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state == StateHealthy && !e.inCooldownLocked()
}

// Usable reports whether the endpoint may be talked to at all: it is not disabled,
// and its chain identity is verified when verification is required. An endpoint of
// an unknown network must never serve reads, writes or verification, even when
// every other endpoint is down: observing the wrong chain is worse than going dark.
func (e *Entry) Usable() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateDisabled {
		return false
	}
	return !e.identityRequired || e.identityVerified
}

// InCooldown reports whether the endpoint is backing off after a rate limit.
func (e *Entry) InCooldown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inCooldownLocked()
}

func (e *Entry) inCooldownLocked() bool {
	return !e.cooldownUntil.IsZero() && e.now().Before(e.cooldownUntil)
}

func (e *Entry) markUnhealthyLocked() {
	if e.state == StateHealthy {
		e.nonHealthySince = e.now()
	}
	e.state = StateUnhealthy
	e.inlineErrors = 0
	e.lastInlineAt = time.Time{}
	e.probeStrikes = 0
	e.recoveryChecks = 0
}

// MarkUnhealthy moves a Healthy or Recovering entry to Unhealthy directly (used at
// bootstrap for unreachable or unverified endpoints).
func (e *Entry) MarkUnhealthy(kind ErrorKind) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastErrorKind = kind
	if kind == ErrorKindIntegrity {
		e.integrityEvicted = true
	}
	if e.state == StateHealthy || e.state == StateRecovering {
		e.markUnhealthyLocked()
	}
}

// RecordError counts one inline error. Consecutive errors no more than error_window
// apart accumulate; reaching error_threshold moves the entry to Unhealthy. It returns
// true when the call moved the entry out of Healthy or Recovering.
func (e *Entry) RecordError(cfg config.ChainFailoverConfig, kind ErrorKind) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastErrorKind = kind
	switch e.state {
	case StateRecovering:
		e.markUnhealthyLocked()
		return true
	case StateUnhealthy, StateDisabled:
		return false
	}
	now := e.now()
	if !e.lastInlineAt.IsZero() && now.Sub(e.lastInlineAt) > cfg.ErrorWindow {
		e.inlineErrors = 0
	}
	e.inlineErrors++
	e.lastInlineAt = now
	if e.inlineErrors >= cfg.ErrorThreshold {
		e.markUnhealthyLocked()
		return true
	}
	return false
}

// RecordSuccess resets the inline error run of a Healthy entry. Probe strikes are
// untouched. For non-healthy entries it is a no-op; use RecordHealthCheck.
func (e *Entry) RecordSuccess() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateHealthy {
		e.inlineErrors = 0
		e.lastInlineAt = time.Time{}
	}
}

// RecordProbeStrike counts one failed, lagging, or inconsistent health probe against
// a Healthy entry; reaching error_threshold moves it to Unhealthy. A Recovering entry
// goes back to Unhealthy. Returns true when the state changed.
func (e *Entry) RecordProbeStrike(cfg config.ChainFailoverConfig, kind ErrorKind) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastErrorKind = kind
	switch e.state {
	case StateRecovering:
		e.markUnhealthyLocked()
		return true
	case StateUnhealthy, StateDisabled:
		return false
	}
	if kind == ErrorKindIntegrity {
		// sticky: lastErrorKind is overwritten by the next transport error or rate
		// limit, and recovery must still require a matching hash
		e.integrityEvicted = true
	}
	e.probeStrikes++
	if e.probeStrikes >= cfg.ErrorThreshold {
		e.markUnhealthyLocked()
		return true
	}
	return false
}

// IntegrityEvicted reports whether the endpoint has an outstanding block-hash
// disagreement that it must clear before it may serve traffic again.
func (e *Entry) IntegrityEvicted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.integrityEvicted
}

// RecordProbePass clears probe strikes of a Healthy entry.
func (e *Entry) RecordProbePass() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateHealthy {
		e.probeStrikes = 0
	}
}

// RecordRateLimited puts the endpoint in cooldown without changing its state.
func (e *Entry) RecordRateLimited(backoff time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastErrorKind = ErrorKindRateLimit
	if backoff > 0 {
		e.cooldownUntil = e.now().Add(backoff)
	}
}

// RecordHealthCheck applies one recovery check result to an Unhealthy or Recovering
// entry and returns the states before and after. A passing check on an entry whose
// identity is required but unverified counts as failed. Healthy and Disabled entries
// are not moved here.
func (e *Entry) RecordHealthCheck(ok bool, cfg config.ChainFailoverConfig) (from, to State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	from = e.state
	if ok && e.identityRequired && !e.identityVerified {
		ok = false
	}
	switch e.state {
	case StateUnhealthy:
		if ok {
			e.state = StateRecovering
			e.recoveryChecks = 1
			if e.recoveryChecks >= cfg.MinRecoveryChecks {
				e.state = StateHealthy
				e.recoveryChecks = 0
			}
		}
	case StateRecovering:
		if ok {
			e.recoveryChecks++
			if e.recoveryChecks >= cfg.MinRecoveryChecks {
				e.state = StateHealthy
				e.recoveryChecks = 0
				e.inlineErrors = 0
				e.lastInlineAt = time.Time{}
				e.probeStrikes = 0
			}
		} else {
			e.state = StateUnhealthy
			e.recoveryChecks = 0
		}
	}
	if e.state == StateHealthy {
		e.nonHealthySince = time.Time{}
		e.integrityEvicted = false
	}
	return from, e.state
}

// NonHealthyFor returns how long the endpoint has been continuously out of the
// Healthy state, or 0 while it is healthy.
func (e *Entry) NonHealthyFor() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateHealthy || e.nonHealthySince.IsZero() {
		return 0
	}
	return e.now().Sub(e.nonHealthySince)
}

// Disable permanently removes the endpoint from rotation.
func (e *Entry) Disable(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateHealthy {
		e.nonHealthySince = e.now()
	}
	e.state = StateDisabled
	e.disabledReason = reason
}

// SetIdentity records the verified chain identity (genesis hash, chain id, ...).
func (e *Entry) SetIdentity(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.identity = id
	e.identityVerified = true
}

// Identity returns the recorded identity ("" when unverified).
func (e *Entry) Identity() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.identity
}

// IdentityVerified reports whether SetIdentity has been called.
func (e *Entry) IdentityVerified() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.identityVerified
}

// RecordProbe stores the latest successful probe height and latency.
func (e *Entry) RecordProbe(height int64, latency time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastHeight = height
	e.lastProbeLatency = latency
	e.lastProbeAt = e.now()
}

// LastErrorKind returns the kind of the most recent recorded error ("" if none).
func (e *Entry) LastErrorKind() ErrorKind {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErrorKind
}

// LastHeight returns the height from the latest successful probe (0 if none).
func (e *Entry) LastHeight() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastHeight
}

// RecoveryDue reports whether a non-healthy entry should be probed now, and if so
// schedules the next probe interval later.
func (e *Entry) RecoveryDue(interval time.Duration) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if now.Before(e.nextRecoveryAt) {
		return false
	}
	e.nextRecoveryAt = now.Add(interval)
	return true
}

// Snapshot returns a copy for status output. Active is filled in by the Manager.
func (e *Entry) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		Name:             e.Name,
		Priority:         e.Priority,
		State:            e.state.String(),
		Height:           e.lastHeight,
		ProbeLatencyMs:   e.lastProbeLatency.Milliseconds(),
		InCooldown:       e.inCooldownLocked(),
		IdentityVerified: e.identityVerified,
		InlineErrors:     e.inlineErrors,
		ProbeStrikes:     e.probeStrikes,
		LastErrorKind:    string(e.lastErrorKind),
		DisabledReason:   e.disabledReason,
	}
}
