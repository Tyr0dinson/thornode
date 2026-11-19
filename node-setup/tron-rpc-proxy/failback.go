package main

import "time"

const failbackHealthyDuration = time.Minute

// All recovery fields are protected by modeMu. Windows are deliberately not
// persisted: process startup and explicit external mode require manual admission.
type recoveryWindow struct {
	since, last     time.Time
	start, previous Health
}

type FailbackStatus struct {
	Enabled         bool       `json:"enabled"`
	Eligible        bool       `json:"eligible"`
	HealthySince    *time.Time `json:"healthy_since,omitempty"`
	HealthySeconds  float64    `json:"healthy_seconds"`
	RequiredSeconds int        `json:"required_seconds"`
}

func (p *Proxy) recoveryFresh(now time.Time) bool {
	age := now.Sub(p.recovery.last)
	return !p.recovery.last.IsZero() && age >= 0 && age < time.Duration(p.cfg.PollSeconds*3)*time.Second
}

func (p *Proxy) failbackStatus() FailbackStatus {
	p.modeMu.Lock()
	defer p.modeMu.Unlock()
	s := FailbackStatus{Enabled: p.cfg.AutoFailbackEnabled, Eligible: p.failbackEligible && !p.unsafe.Load(), RequiredSeconds: 60}
	if !p.recovery.since.IsZero() && p.recoveryFresh(p.now()) && p.fresh(p.snapshot()) && !p.unsafe.Load() {
		since := p.recovery.since.UTC()
		s.HealthySince = &since
		// Report verified elapsed time, not time waiting for the next check.
		s.HealthySeconds = p.recovery.last.Sub(p.recovery.since).Seconds()
	}
	return s
}

func (p *Proxy) resetRecoveryLocked(check checkResult, h Health) {
	if !p.recovery.since.IsZero() {
		p.recordDiagnostic("failback_reset", check, h, "external", "external")
	}
	p.recovery = recoveryWindow{}
}

func (p *Proxy) updateRecoveryPolicyLocked(mode, previous string, check checkResult) {
	// A concurrent operator command or late read failure invalidates probe
	// evidence, even when the durable mode is already external.
	p.modeEpoch++
	if mode == "local" && check.Stage == "auto_failback" {
		p.recovery = recoveryWindow{}
	} else {
		p.resetRecoveryLocked(check, p.snapshot())
	}
	manual := check.Reason == reasonOperator || check.Reason == reasonStartup
	p.failbackEligible = p.cfg.AutoFailbackEnabled && mode == "external" && !manual && (previous == "local" || p.failbackEligible)
}

func (p *Proxy) considerFailback(h Health, epoch uint64) {
	p.modeMu.Lock()
	defer p.modeMu.Unlock()
	if epoch != p.modeEpoch {
		return
	}
	check := checkResult{Upstream: "both", Stage: "auto_failback", Reason: reasonUnverified}
	if !p.cfg.AutoFailbackEnabled || !p.failbackEligible || p.unsafe.Load() || !h.Local || !h.External || !h.ChainCheck.OK || !p.fresh(h) || p.currentMode() != "external" {
		p.resetRecoveryLocked(check, h)
		return
	}
	now := p.now()
	previous := p.recovery.previous
	if !p.recovery.since.IsZero() && (!p.recoveryFresh(now) || h.LocalHeight < previous.LocalHeight || h.ExternalHeight < previous.ExternalHeight || h.CommonHeight < previous.CommonHeight) {
		p.resetRecoveryLocked(check, h)
	}
	if p.recovery.since.IsZero() {
		p.recovery = recoveryWindow{since: now, last: now, start: h, previous: h}
		check.OK, check.Reason = true, reasonOK
		p.recordDiagnostic("failback_started", check, h, "external", "external")
		return
	}
	p.recovery.last, p.recovery.previous = now, h
	// Fresh timestamps alone are insufficient: both heads and the shared
	// finalized point must actually advance during the observation window.
	if now.Sub(p.recovery.since) < failbackHealthyDuration || h.LocalHeight <= p.recovery.start.LocalHeight || h.ExternalHeight <= p.recovery.start.ExternalHeight || h.CommonHeight <= p.recovery.start.CommonHeight {
		return
	}
	check.OK, check.Reason = true, reasonOK
	if p.setModeLocked("local", check) {
		p.autoFailbacks.Add(1)
	}
}
